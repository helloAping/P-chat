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
  httpcli/       → HTTP client for remote REPL
  paths/         → ~/.p-chat directory resolution
  knowledge/     → Knowledge retrieval
  recall/        → Memory recall augment
  serverproc/    → Server process lifecycle
```

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
