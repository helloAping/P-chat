# P-Chat

Agent-driven chat application (Go + Vue 3 + Vite + SQLite).

## Architecture

```
                    ┌─────────────────┐
                    │   ChatMessage   │ ← protocol-agnostic
                    │ (llm package)   │   persist & internal
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
       ┌──────▼──────┐ ┌─────▼──────┐      │
       │ OpenAI      │ │ Anthropic  │      │
       │ Adapter     │ │ Adapter    │      │
       └──────┬──────┘ └─────┬──────┘      │
              │              │              │
        ┌─────▼──────┐ ┌─────▼──────┐      │
        │ /chat/     │ │ /v1/       │      │
        │ completions│ │ messages   │      │
        └────────────┘ └────────────┘      │
                                           │
                              ┌────────────▼────────────┐
                              │     memory.Store         │
                              │  SQLite ~/.p-chat/store  │
                              │  (columns: id, role,     │
                              │   content, metadata)     │
                              └─────────────────────────┘
```

### ChatMessage format

Protocol-agnostic, one message per logical unit:

| Type        | Role       | Content                    |
|-------------|-----------|----------------------------|
| text        | user/assistant | plain text             |
| image       | user      | raw base64                 |
| tool_call   | assistant | JSON tool input (ToolInput)|
| tool_result | tool      | tool output text           |
| thinking    | assistant | agent-internal only        |

Images and file attachments are **separate message rows** — never mixed into MultiContent arrays.

### Protocol adapters

```go
type ProtocolAdapter interface {
    Build([]ChatMessage, model, tools, system) → ProtocolRequest
    ParseStream(io.Reader) → <-chan StreamChunk
}
```

Adapters skip agent-internal types (thinking, sub-agent messages).

### Modules overview

```
cmd/
  pchat/          → CLI (Go REPL)
  pchat-server/   → HTTP server (Gin)
  pchat-gui/      → Vue 3 frontend + Wails v2

internal/
  agent/         → ReAct tool loop, message parts, attachment expansion
  config/        → ~/.p-chat/config.json management
  llm/           → ChatMessage, ProtocolAdapter, StreamChunk, error classification
  memory/        → SQLite conversation store (chat history + metadata)
  server/        → Gin HTTP handlers (sessions, messages, uploads, providers)
  tool/          → Tool registry (exec, read/write, sub-agent)
  cli/           → REPL, commands, model handling
  subagent/      → Nested agent runner
  style/         → Personality style management
  agents/        → AGENTS.md instructions loader
  rules/         → .rules/ directory watcher
  skill/         → .skills/ directory loader
  sandbox/       → Tool execution guards
  project/       → Project directory registry
  httpcli/       → HTTP client for remote REPL
  paths/         → ~/.p-chat directory resolution
  knowledge/     → Knowledge retrieval
  recall/        → Memory recall augment
  serverproc/    → Server process lifecycle
  project/       → Project directory registry (~/.p-chat/projects.json)
```

## Project system

Users can register project directories. Each project has:

| File | Path |
|------|------|
| Project config | `<project>/.p-chat/config.json` (merged atop global config) |
| Project AGENTS instructions | `<project>/AGENTS.md` (merged with global AGENTS.md) |
| Project skills | `<project>/.p-chat/skills/` |
| Project rules | `<project>/.p-chat/rules/` |

Sessions belong to a project (or global). When a session has `project_path` set in metadata:
- `config.LoadWithProjectRoot("", projectRoot)` merges the project's `.p-chat/config.json` over the global config
- `agents.LoadAllWithRoot(projectRoot)` includes the project's `AGENTS.md`
- The agent's `buildStaticSystemPrompt` includes project root in its cache signature

API: `GET/POST/DELETE /api/v1/projects`, sessions filter by `?project_path=`.

## Frontend modal constraints

All `NModal` instances **must** use `preset="card"` — plain `NModal` has a transparent backdrop that is invisible against the theme background. Card preset provides the proper `var(--bg-2)` / `var(--border)` themed rendering.

## Build commands

```powershell
# Go backend
go build -o bin/pchat-server.exe ./cmd/pchat-server
go build -o bin/pchat.exe ./cmd/pchat

# Frontend
cd cmd/pchat-gui/frontend
npm run build

# Sync SPA files
powershell -File scripts/sync-web.ps1

# Full test suite
go test ./...

# Process management
Get-Process -Name "pchat-server*" | Stop-Process -Force
```
