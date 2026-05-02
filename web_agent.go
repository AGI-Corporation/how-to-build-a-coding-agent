package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/invopop/jsonschema"
)

//go:embed static
var staticFS embed.FS

const systemPrompt = `You are the Self-Coding Agent for the
"how-to-build-a-coding-agent" workshop, exposed over a web UI with
voice input.

Your jobs:
  1. Help the team maintain ROADMAP.md from real git history.
  2. Keep README.md aligned with the actual code.
  3. Read, search, and edit any file in the working directory.

Because the user may be speaking, transcripts can be lossy or
ambiguous. When intent is unclear, ask one short clarifying question
before acting. Keep replies concise and skimmable — they may be read
aloud by text-to-speech.
`

// ---------- Tool plumbing ---------- //

type ToolDefinition struct {
	Name        string                         `json:"name"`
	Description string                         `json:"description"`
	InputSchema anthropic.ToolInputSchemaParam `json:"input_schema"`
	Function    func(input json.RawMessage) (string, error)
}

type ReadFileInput struct {
	Path string `json:"path" jsonschema_description:"Relative path of a file in the working directory."`
}
type ListFilesInput struct {
	Path string `json:"path,omitempty" jsonschema_description:"Optional relative path. Defaults to current directory."`
}
type BashInput struct {
	Command string `json:"command" jsonschema_description:"Bash command to execute."`
}
type EditFileInput struct {
	Path   string `json:"path" jsonschema_description:"The path to the file"`
	OldStr string `json:"old_str" jsonschema_description:"Text to search for - must match exactly and be unique"`
	NewStr string `json:"new_str" jsonschema_description:"Text to replace old_str with"`
}
type CodeSearchInput struct {
	Pattern       string `json:"pattern" jsonschema_description:"Search pattern or regex"`
	Path          string `json:"path,omitempty" jsonschema_description:"Optional path to search in"`
	FileType      string `json:"file_type,omitempty" jsonschema_description:"Optional file type filter"`
	CaseSensitive bool   `json:"case_sensitive,omitempty" jsonschema_description:"Case-sensitive search"`
}
type GitLogInput struct {
	Limit int    `json:"limit,omitempty" jsonschema_description:"Max commits (default 20, cap 200)"`
	Path  string `json:"path,omitempty" jsonschema_description:"Optional path to scope log to"`
}

func GenerateSchema[T any]() anthropic.ToolInputSchemaParam {
	r := jsonschema.Reflector{AllowAdditionalProperties: false, DoNotReference: true}
	var v T
	s := r.Reflect(v)
	return anthropic.ToolInputSchemaParam{Properties: s.Properties}
}

func ReadFile(input json.RawMessage) (string, error) {
	in := ReadFileInput{}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	b, err := os.ReadFile(in.Path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func ListFiles(input json.RawMessage) (string, error) {
	in := ListFilesInput{}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	dir := "."
	if in.Path != "" {
		dir = in.Path
	}
	var files []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if info.IsDir() && (rel == ".devenv" || strings.HasPrefix(rel, ".devenv/") ||
			rel == ".git" || strings.HasPrefix(rel, ".git/")) {
			return filepath.SkipDir
		}
		if rel != "." {
			if info.IsDir() {
				files = append(files, rel+"/")
			} else {
				files = append(files, rel)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(files)
	return string(out), nil
}

func Bash(input json.RawMessage) (string, error) {
	in := BashInput{}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	cmd := exec.Command("bash", "-c", in.Command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Command failed: %s\nOutput: %s", err.Error(), string(out)), nil
	}
	return strings.TrimSpace(string(out)), nil
}

func EditFile(input json.RawMessage) (string, error) {
	in := EditFileInput{}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	if in.Path == "" || in.OldStr == in.NewStr {
		return "", fmt.Errorf("invalid input parameters")
	}
	content, err := os.ReadFile(in.Path)
	if err != nil {
		if os.IsNotExist(err) && in.OldStr == "" {
			dir := path.Dir(in.Path)
			if dir != "." {
				if err := os.MkdirAll(dir, 0755); err != nil {
					return "", err
				}
			}
			if err := os.WriteFile(in.Path, []byte(in.NewStr), 0644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Created %s", in.Path), nil
		}
		return "", err
	}
	old := string(content)
	var next string
	if in.OldStr == "" {
		next = old + in.NewStr
	} else {
		c := strings.Count(old, in.OldStr)
		if c == 0 {
			return "", fmt.Errorf("old_str not found in file")
		}
		if c > 1 {
			return "", fmt.Errorf("old_str found %d times, must be unique", c)
		}
		next = strings.Replace(old, in.OldStr, in.NewStr, 1)
	}
	if err := os.WriteFile(in.Path, []byte(next), 0644); err != nil {
		return "", err
	}
	return "OK", nil
}

func CodeSearch(input json.RawMessage) (string, error) {
	in := CodeSearchInput{}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	args := []string{"--line-number", "--with-filename", "--color=never"}
	if !in.CaseSensitive {
		args = append(args, "--ignore-case")
	}
	if in.FileType != "" {
		args = append(args, "--type", in.FileType)
	}
	args = append(args, in.Pattern)
	if in.Path != "" {
		args = append(args, in.Path)
	} else {
		args = append(args, ".")
	}
	cmd := exec.Command("rg", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "No matches found", nil
		}
		return "", fmt.Errorf("search failed: %w", err)
	}
	res := strings.TrimSpace(string(out))
	lines := strings.Split(res, "\n")
	if len(lines) > 50 {
		res = strings.Join(lines[:50], "\n") + fmt.Sprintf("\n... (showing first 50 of %d matches)", len(lines))
	}
	return res, nil
}

func GitLog(input json.RawMessage) (string, error) {
	in := GitLogInput{}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	args := []string{"log", fmt.Sprintf("-%d", limit), "--oneline", "--no-decorate"}
	if in.Path != "" {
		args = append(args, "--", in.Path)
	}
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("git log failed: %s\nOutput: %s", err.Error(), string(out)), nil
	}
	return strings.TrimSpace(string(out)), nil
}

func defaultTools() []ToolDefinition {
	return []ToolDefinition{
		{Name: "read_file", Description: "Read a file from the working directory.", InputSchema: GenerateSchema[ReadFileInput](), Function: ReadFile},
		{Name: "list_files", Description: "List files in a directory.", InputSchema: GenerateSchema[ListFilesInput](), Function: ListFiles},
		{Name: "bash", Description: "Execute a bash command.", InputSchema: GenerateSchema[BashInput](), Function: Bash},
		{Name: "edit_file", Description: "Edit or create a file by replacing old_str with new_str.", InputSchema: GenerateSchema[EditFileInput](), Function: EditFile},
		{Name: "code_search", Description: "Ripgrep-powered code search.", InputSchema: GenerateSchema[CodeSearchInput](), Function: CodeSearch},
		{Name: "git_log", Description: "Read git commit history (one line per commit).", InputSchema: GenerateSchema[GitLogInput](), Function: GitLog},
	}
}

// ---------- Server ---------- //

type Session struct {
	mu           sync.Mutex
	Conversation []anthropic.MessageParam
	LastUsed     time.Time
}

type Server struct {
	client   *anthropic.Client
	tools    []ToolDefinition
	sessions sync.Map
	verbose  bool
}

type ChatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type ToolCallView struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type ChatResponse struct {
	SessionID string         `json:"session_id"`
	Response  string         `json:"response"`
	ToolCalls []ToolCallView `json:"tool_calls,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("s-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	hasKey := os.Getenv("ANTHROPIC_API_KEY") != ""
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"has_api_key":   hasKey,
		"tools":         len(s.tools),
		"sessions_open": s.countSessions(),
	})
}

func (s *Server) countSessions() int {
	n := 0
	s.sessions.Range(func(_, _ any) bool { n++; return true })
	return n
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.SessionID != "" {
		s.sessions.Delete(req.SessionID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid JSON: " + err.Error()})
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "empty message"})
		return
	}

	sessionID := req.SessionID
	var sess *Session
	if sessionID == "" {
		sessionID = newSessionID()
		sess = &Session{}
		s.sessions.Store(sessionID, sess)
	} else if v, ok := s.sessions.Load(sessionID); ok {
		sess = v.(*Session)
	} else {
		sess = &Session{}
		s.sessions.Store(sessionID, sess)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.LastUsed = time.Now()

	sess.Conversation = append(sess.Conversation, anthropic.NewUserMessage(anthropic.NewTextBlock(req.Message)))

	ctx := r.Context()
	message, err := s.runInference(ctx, sess.Conversation)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error()})
		return
	}
	sess.Conversation = append(sess.Conversation, message.ToParam())

	var toolCalls []ToolCallView
	var responseText strings.Builder

	for {
		var toolResults []anthropic.ContentBlockParamUnion
		var hasToolUse bool
		for _, content := range message.Content {
			switch content.Type {
			case "text":
				if content.Text != "" {
					if responseText.Len() > 0 {
						responseText.WriteString("\n")
					}
					responseText.WriteString(content.Text)
				}
			case "tool_use":
				hasToolUse = true
				tu := content.AsToolUse()
				view := ToolCallView{Name: tu.Name, Input: string(tu.Input)}
				var result string
				var toolErr error
				found := false
				for _, t := range s.tools {
					if t.Name == tu.Name {
						result, toolErr = t.Function(tu.Input)
						found = true
						break
					}
				}
				if !found {
					toolErr = fmt.Errorf("tool '%s' not found", tu.Name)
				}
				if toolErr != nil {
					view.Error = toolErr.Error()
					toolResults = append(toolResults, anthropic.NewToolResultBlock(tu.ID, toolErr.Error(), true))
				} else {
					view.Result = result
					toolResults = append(toolResults, anthropic.NewToolResultBlock(tu.ID, result, false))
				}
				toolCalls = append(toolCalls, view)
			}
		}
		if !hasToolUse {
			break
		}
		sess.Conversation = append(sess.Conversation, anthropic.NewUserMessage(toolResults...))
		message, err = s.runInference(ctx, sess.Conversation)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error()})
			return
		}
		sess.Conversation = append(sess.Conversation, message.ToParam())
	}

	writeJSON(w, http.StatusOK, ChatResponse{
		SessionID: sessionID,
		Response:  responseText.String(),
		ToolCalls: toolCalls,
	})
}

func (s *Server) runInference(ctx context.Context, conv []anthropic.MessageParam) (*anthropic.Message, error) {
	tools := []anthropic.ToolUnionParam{}
	for _, t := range s.tools {
		tools = append(tools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: t.InputSchema,
			},
		})
	}
	if s.verbose {
		log.Printf("inference: %d msgs, %d tools", len(conv), len(tools))
	}
	return s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeOpus4_6,
		MaxTokens: int64(4096),
		System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
		Messages:  conv,
		Tools:     tools,
	})
}

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	verbose := flag.Bool("verbose", false, "enable verbose logging")
	flag.Parse()

	if *verbose {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	} else {
		log.SetOutput(os.Stdout)
		log.SetFlags(log.LstdFlags)
	}

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Println("WARNING: ANTHROPIC_API_KEY is not set; /api/chat will fail until you export it.")
	}

	client := anthropic.NewClient()
	srv := &Server{
		client:  &client,
		tools:   defaultTools(),
		verbose: *verbose,
	}

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("embed: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/health", srv.handleHealth)
	mux.HandleFunc("/api/chat", srv.handleChat)
	mux.HandleFunc("/api/reset", srv.handleReset)

	log.Printf("Self-Coding Agent web UI listening on http://localhost%s", *addr)
	log.Printf("Open the URL in Chrome or Edge for voice input support.")
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
