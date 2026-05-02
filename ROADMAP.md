# 🗺️ Roadmap

This document tracks the evolution of the workshop. The
[`self_coding_agent.go`](./self_coding_agent.go) is the canonical tool for
keeping this file (and the README) in sync with the actual codebase — run it
and ask it to "update the roadmap from git history" whenever a milestone ships.

> Maintained by the team. The Self-Coding Agent helps draft updates from
> `git log`, but humans approve the merges.

---

## ✅ Shipped

Milestones already in `trunk`. Each item links to the file that introduced the
capability and the original commit (oldest first).

| Stage | Capability | File | Notes |
| ----- | ---------- | ---- | ----- |
| 1 | Basic chat loop with Claude | [`chat.go`](./chat.go) | Initial event loop, no tools |
| 2 | Read files from disk | [`read.go`](./read.go) | First tool: `read_file` |
| 3 | List directory contents | [`list_files.go`](./list_files.go) | Adds `list_files` |
| 4 | Run shell commands | [`bash_tool.go`](./bash_tool.go) | Adds `bash` |
| 5 | Edit and create files | [`edit_tool.go`](./edit_tool.go) | Adds `edit_file` |
| 6 | Code search via ripgrep | [`code_search_tool.go`](./code_search_tool.go) | Adds `code_search` |
| 7 | Self-coding agent for docs | [`self_coding_agent.go`](./self_coding_agent.go) | All previous tools + `git_log`, system prompt focused on ROADMAP.md and README.md |
| 8 | Web UI with voice | [`web_agent.go`](./web_agent.go) + [`static/`](./static) | HTTP server with embedded frontend, Web Speech API for voice in/out, per-session conversation state |

---

## 🚧 In Progress

Work that's been started but isn't ready to merge yet. Move items here when a
branch exists, and out of here when it lands in `trunk`.

- _(empty — add as branches open)_

---

## 🧭 Planned

Ideas the team has agreed are worth doing, in rough priority order. Anything
here is fair game for a contributor to pick up.

- **Persistent memory** — let the agent remember context across sessions
  (likely a `memory.json` next to the binary).
- **Tool chaining helper** — a higher-level tool that composes search → read →
  edit in one call to reduce round-trips.
- **Streaming responses** — switch `/api/chat` to SSE/WebSocket so the
  web UI shows tokens as they arrive instead of waiting for the full reply.
- **Server-side TTS** — replace browser SpeechSynthesis with a higher-quality
  voice from a TTS API for more natural responses.
- **Test harness** — golden-file tests for each tool's `Function` so refactors
  don't silently break behaviour.
- **Multi-model support** — pluggable backend so the workshop can demonstrate
  the same loop against other model families.

---

## 💡 Backlog / Ideas

Unprioritised. Promote to "Planned" once the team agrees it's worth doing.

- Custom tool: HTTP fetch / web scraper.
- Custom tool: API caller for arbitrary REST endpoints.
- Sandbox mode for the bash tool (deny-list of destructive commands).
- Streaming responses to the terminal instead of waiting for the full message.
- A `/changelog` slash-style command inside the agent.

---

## 🔄 How to Update This Roadmap

1. `go run self_coding_agent.go`
2. Ask: _"Read the last 20 commits and propose updates to ROADMAP.md."_
3. Review the proposed diff, then accept or refine.
4. Commit alongside the change that motivated the roadmap update.

The agent has a `git_log` tool plus full read/edit access, so it can do all of
the above in one session.
