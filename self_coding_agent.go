package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/invopop/jsonschema"
)

const systemPrompt = `You are the Self-Coding Agent for the "how-to-build-a-coding-agent" workshop.

Your purpose is to assist the team in two specific tasks:

1. BUILDING THE GIT ROADMAP (ROADMAP.md)
   - Read git history with the git_log tool to understand what has shipped.
   - Maintain ROADMAP.md as the single source of truth for what's done,
     what's in flight, and what's planned next.
   - Group items by milestone (e.g. "Shipped", "In Progress", "Planned").
   - Reference the relevant Go files (chat.go, read.go, list_files.go,
     bash_tool.go, edit_tool.go, code_search_tool.go, self_coding_agent.go)
     when describing milestones.

2. KEEPING THE README CURRENT (README.md)
   - When a new agent stage or tool is added, reflect it in README.md.
   - Preserve the existing tone (friendly, emoji-rich, step-by-step).
   - Keep the mermaid diagrams in sync with the actual progression of files.

You also have full self-coding ability: you can read, search, edit, and run
the codebase that defines you (self_coding_agent.go). When the team asks for
a new capability, propose the change, edit the file, then ` + "`go build`" + ` to verify.

Workflow guidelines:
- Always start a roadmap or README task by reading the current file first.
- Prefer surgical edits over rewrites.
- When unsure, list files and read the relevant source before editing.
- After any edit to a Go file, run ` + "`go build <file>.go`" + ` via the bash tool.
- Keep responses concise; let the diffs and tool output speak for you.
`

func main() {
	verbose := flag.Bool("verbose", false, "enable verbose logging")
	flag.Parse()

	if *verbose {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags | log.Lshortfile)
		log.Println("Verbose logging enabled")
	} else {
		log.SetOutput(os.Stdout)
		log.SetFlags(0)
		log.SetPrefix("")
	}

	client := anthropic.NewClient()
	if *verbose {
		log.Println("Anthropic client initialized")
	}

	scanner := bufio.NewScanner(os.Stdin)
	getUserMessage := func() (string, bool) {
		if !scanner.Scan() {
			return "", false
		}
		return scanner.Text(), true
	}

	tools := []ToolDefinition{
		ReadFileDefinition,
		ListFilesDefinition,
		BashDefinition,
		EditFileDefinition,
		CodeSearchDefinition,
		GitLogDefinition,
	}
	if *verbose {
		log.Printf("Initialized %d tools", len(tools))
	}
	agent := NewAgent(&client, getUserMessage, tools, *verbose)
	err := agent.Run(context.TODO())
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
	}
}

func NewAgent(
	client *anthropic.Client,
	getUserMessage func() (string, bool),
	tools []ToolDefinition,
	verbose bool,
) *Agent {
	return &Agent{
		client:         client,
		getUserMessage: getUserMessage,
		tools:          tools,
		verbose:        verbose,
	}
}

type Agent struct {
	client         *anthropic.Client
	getUserMessage func() (string, bool)
	tools          []ToolDefinition
	verbose        bool
}

func (a *Agent) Run(ctx context.Context) error {
	conversation := []anthropic.MessageParam{}

	if a.verbose {
		log.Println("Starting self-coding agent session")
	}
	fmt.Println("Self-Coding Agent ready. Ask for help with ROADMAP.md, README.md, or new tools.")
	fmt.Println("(use 'ctrl-c' to quit)")

	for {
		fmt.Print("[94mYou[0m: ")
		userInput, ok := a.getUserMessage()
		if !ok {
			if a.verbose {
				log.Println("User input ended, breaking from chat loop")
			}
			break
		}

		if userInput == "" {
			if a.verbose {
				log.Println("Skipping empty message")
			}
			continue
		}

		if a.verbose {
			log.Printf("User input received: %q", userInput)
		}

		userMessage := anthropic.NewUserMessage(anthropic.NewTextBlock(userInput))
		conversation = append(conversation, userMessage)

		message, err := a.runInference(ctx, conversation)
		if err != nil {
			if a.verbose {
				log.Printf("Error during inference: %v", err)
			}
			return err
		}
		conversation = append(conversation, message.ToParam())

		for {
			var toolResults []anthropic.ContentBlockParamUnion
			var hasToolUse bool

			if a.verbose {
				log.Printf("Processing %d content blocks from Claude", len(message.Content))
			}

			for _, content := range message.Content {
				switch content.Type {
				case "text":
					fmt.Printf("[93mClaude[0m: %s\n", content.Text)
				case "tool_use":
					hasToolUse = true
					toolUse := content.AsToolUse()
					if a.verbose {
						log.Printf("Tool use detected: %s with input: %s", toolUse.Name, string(toolUse.Input))
					}
					fmt.Printf("[96mtool[0m: %s(%s)\n", toolUse.Name, string(toolUse.Input))

					var toolResult string
					var toolError error
					var toolFound bool
					for _, tool := range a.tools {
						if tool.Name == toolUse.Name {
							toolResult, toolError = tool.Function(toolUse.Input)
							fmt.Printf("[92mresult[0m: %s\n", toolResult)
							if toolError != nil {
								fmt.Printf("[91merror[0m: %s\n", toolError.Error())
							}
							toolFound = true
							break
						}
					}

					if !toolFound {
						toolError = fmt.Errorf("tool '%s' not found", toolUse.Name)
						fmt.Printf("[91merror[0m: %s\n", toolError.Error())
					}

					if toolError != nil {
						toolResults = append(toolResults, anthropic.NewToolResultBlock(toolUse.ID, toolError.Error(), true))
					} else {
						toolResults = append(toolResults, anthropic.NewToolResultBlock(toolUse.ID, toolResult, false))
					}
				}
			}

			if !hasToolUse {
				break
			}

			toolResultMessage := anthropic.NewUserMessage(toolResults...)
			conversation = append(conversation, toolResultMessage)

			message, err = a.runInference(ctx, conversation)
			if err != nil {
				if a.verbose {
					log.Printf("Error during followup inference: %v", err)
				}
				return err
			}
			conversation = append(conversation, message.ToParam())
		}
	}

	if a.verbose {
		log.Println("Self-coding agent session ended")
	}
	return nil
}

func (a *Agent) runInference(ctx context.Context, conversation []anthropic.MessageParam) (*anthropic.Message, error) {
	anthropicTools := []anthropic.ToolUnionParam{}
	for _, tool := range a.tools {
		anthropicTools = append(anthropicTools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        tool.Name,
				Description: anthropic.String(tool.Description),
				InputSchema: tool.InputSchema,
			},
		})
	}

	if a.verbose {
		log.Printf("Making API call to Claude with model: %s and %d tools", anthropic.ModelClaudeOpus4_6, len(anthropicTools))
	}

	message, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeOpus4_6,
		MaxTokens: int64(4096),
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: conversation,
		Tools:    anthropicTools,
	})

	if a.verbose {
		if err != nil {
			log.Printf("API call failed: %v", err)
		} else {
			log.Printf("API call successful, response received")
		}
	}

	return message, err
}

type ToolDefinition struct {
	Name        string                         `json:"name"`
	Description string                         `json:"description"`
	InputSchema anthropic.ToolInputSchemaParam `json:"input_schema"`
	Function    func(input json.RawMessage) (string, error)
}

var ReadFileDefinition = ToolDefinition{
	Name:        "read_file",
	Description: "Read the contents of a given relative file path. Use this when you want to see what's inside a file. Do not use this with directory names.",
	InputSchema: ReadFileInputSchema,
	Function:    ReadFile,
}

var ListFilesDefinition = ToolDefinition{
	Name:        "list_files",
	Description: "List files and directories at a given path. If no path is provided, lists files in the current directory.",
	InputSchema: ListFilesInputSchema,
	Function:    ListFiles,
}

var BashDefinition = ToolDefinition{
	Name:        "bash",
	Description: "Execute a bash command and return its output. Use this to run shell commands, build the agent, or run tests.",
	InputSchema: BashInputSchema,
	Function:    Bash,
}

var EditFileDefinition = ToolDefinition{
	Name: "edit_file",
	Description: `Make edits to a text file.

Replaces 'old_str' with 'new_str' in the given file. 'old_str' and 'new_str' MUST be different from each other.

If the file specified with path doesn't exist, it will be created.
`,
	InputSchema: EditFileInputSchema,
	Function:    EditFile,
}

var CodeSearchDefinition = ToolDefinition{
	Name: "code_search",
	Description: `Search for code patterns using ripgrep (rg).

Use this to find code patterns, function definitions, variable usage, or any text in the codebase.`,
	InputSchema: CodeSearchInputSchema,
	Function:    CodeSearch,
}

var GitLogDefinition = ToolDefinition{
	Name: "git_log",
	Description: `Read git commit history. Use this to understand what has shipped recently
when building or updating ROADMAP.md. Returns a one-line summary per commit.`,
	InputSchema: GitLogInputSchema,
	Function:    GitLog,
}

type ReadFileInput struct {
	Path string `json:"path" jsonschema_description:"The relative path of a file in the working directory."`
}

var ReadFileInputSchema = GenerateSchema[ReadFileInput]()

type ListFilesInput struct {
	Path string `json:"path,omitempty" jsonschema_description:"Optional relative path to list files from. Defaults to current directory if not provided."`
}

var ListFilesInputSchema = GenerateSchema[ListFilesInput]()

type BashInput struct {
	Command string `json:"command" jsonschema_description:"The bash command to execute."`
}

var BashInputSchema = GenerateSchema[BashInput]()

type EditFileInput struct {
	Path   string `json:"path" jsonschema_description:"The path to the file"`
	OldStr string `json:"old_str" jsonschema_description:"Text to search for - must match exactly and must only have one match exactly"`
	NewStr string `json:"new_str" jsonschema_description:"Text to replace old_str with"`
}

var EditFileInputSchema = GenerateSchema[EditFileInput]()

type CodeSearchInput struct {
	Pattern       string `json:"pattern" jsonschema_description:"The search pattern or regex to look for"`
	Path          string `json:"path,omitempty" jsonschema_description:"Optional path to search in (file or directory)"`
	FileType      string `json:"file_type,omitempty" jsonschema_description:"Optional file extension to limit search to (e.g., 'go', 'md')"`
	CaseSensitive bool   `json:"case_sensitive,omitempty" jsonschema_description:"Whether the search should be case sensitive (default: false)"`
}

var CodeSearchInputSchema = GenerateSchema[CodeSearchInput]()

type GitLogInput struct {
	Limit int    `json:"limit,omitempty" jsonschema_description:"Maximum number of commits to show (default 20, max 200)"`
	Path  string `json:"path,omitempty" jsonschema_description:"Optional file or directory path to scope the log to"`
}

var GitLogInputSchema = GenerateSchema[GitLogInput]()

func ReadFile(input json.RawMessage) (string, error) {
	readFileInput := ReadFileInput{}
	if err := json.Unmarshal(input, &readFileInput); err != nil {
		panic(err)
	}

	log.Printf("Reading file: %s", readFileInput.Path)
	content, err := os.ReadFile(readFileInput.Path)
	if err != nil {
		log.Printf("Failed to read file %s: %v", readFileInput.Path, err)
		return "", err
	}
	log.Printf("Successfully read file %s (%d bytes)", readFileInput.Path, len(content))
	return string(content), nil
}

func ListFiles(input json.RawMessage) (string, error) {
	listFilesInput := ListFilesInput{}
	if err := json.Unmarshal(input, &listFilesInput); err != nil {
		panic(err)
	}

	dir := "."
	if listFilesInput.Path != "" {
		dir = listFilesInput.Path
	}

	log.Printf("Listing files in directory: %s", dir)
	var files []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if info.IsDir() && (relPath == ".devenv" || strings.HasPrefix(relPath, ".devenv/") ||
			relPath == ".git" || strings.HasPrefix(relPath, ".git/")) {
			return filepath.SkipDir
		}
		if relPath != "." {
			if info.IsDir() {
				files = append(files, relPath+"/")
			} else {
				files = append(files, relPath)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	result, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func Bash(input json.RawMessage) (string, error) {
	bashInput := BashInput{}
	if err := json.Unmarshal(input, &bashInput); err != nil {
		return "", err
	}

	log.Printf("Executing bash command: %s", bashInput.Command)
	cmd := exec.Command("bash", "-c", bashInput.Command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Command failed with error: %s\nOutput: %s", err.Error(), string(output)), nil
	}
	return strings.TrimSpace(string(output)), nil
}

func EditFile(input json.RawMessage) (string, error) {
	editFileInput := EditFileInput{}
	if err := json.Unmarshal(input, &editFileInput); err != nil {
		return "", err
	}

	if editFileInput.Path == "" || editFileInput.OldStr == editFileInput.NewStr {
		return "", fmt.Errorf("invalid input parameters")
	}

	content, err := os.ReadFile(editFileInput.Path)
	if err != nil {
		if os.IsNotExist(err) && editFileInput.OldStr == "" {
			return createNewFile(editFileInput.Path, editFileInput.NewStr)
		}
		return "", err
	}

	oldContent := string(content)

	var newContent string
	if editFileInput.OldStr == "" {
		newContent = oldContent + editFileInput.NewStr
	} else {
		count := strings.Count(oldContent, editFileInput.OldStr)
		if count == 0 {
			return "", fmt.Errorf("old_str not found in file")
		}
		if count > 1 {
			return "", fmt.Errorf("old_str found %d times in file, must be unique", count)
		}
		newContent = strings.Replace(oldContent, editFileInput.OldStr, editFileInput.NewStr, 1)
	}

	if err := os.WriteFile(editFileInput.Path, []byte(newContent), 0644); err != nil {
		return "", err
	}
	return "OK", nil
}

func createNewFile(filePath, content string) (string, error) {
	dir := path.Dir(filePath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create directory: %w", err)
		}
	}
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	return fmt.Sprintf("Successfully created file %s", filePath), nil
}

func CodeSearch(input json.RawMessage) (string, error) {
	codeSearchInput := CodeSearchInput{}
	if err := json.Unmarshal(input, &codeSearchInput); err != nil {
		return "", err
	}
	if codeSearchInput.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	args := []string{"--line-number", "--with-filename", "--color=never"}
	if !codeSearchInput.CaseSensitive {
		args = append(args, "--ignore-case")
	}
	if codeSearchInput.FileType != "" {
		args = append(args, "--type", codeSearchInput.FileType)
	}
	args = append(args, codeSearchInput.Pattern)
	if codeSearchInput.Path != "" {
		args = append(args, codeSearchInput.Path)
	} else {
		args = append(args, ".")
	}

	cmd := exec.Command("rg", args...)
	output, err := cmd.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
			return "No matches found", nil
		}
		return "", fmt.Errorf("search failed: %w", err)
	}

	result := strings.TrimSpace(string(output))
	lines := strings.Split(result, "\n")
	if len(lines) > 50 {
		result = strings.Join(lines[:50], "\n") + fmt.Sprintf("\n... (showing first 50 of %d matches)", len(lines))
	}
	return result, nil
}

func GitLog(input json.RawMessage) (string, error) {
	gitLogInput := GitLogInput{}
	if err := json.Unmarshal(input, &gitLogInput); err != nil {
		return "", err
	}

	limit := gitLogInput.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	args := []string{"log", fmt.Sprintf("-%d", limit), "--oneline", "--no-decorate"}
	if gitLogInput.Path != "" {
		args = append(args, "--", gitLogInput.Path)
	}

	log.Printf("Running git %s", strings.Join(args, " "))
	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("git log failed: %s\nOutput: %s", err.Error(), string(output)), nil
	}
	return strings.TrimSpace(string(output)), nil
}

func GenerateSchema[T any]() anthropic.ToolInputSchemaParam {
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	var v T
	schema := reflector.Reflect(v)
	return anthropic.ToolInputSchemaParam{
		Properties: schema.Properties,
	}
}
