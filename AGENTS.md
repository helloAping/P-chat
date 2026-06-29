# P-Chat

Agent-driven chat application (Go + Vue 3 + Vite + SQLite).

## Architecture

```
   ┌──────────┐       HTTP SSE        ┌──────────────┐
   │ CLI REPL │ ────────────────────> │ pchat-server │
   │ (ChatUI) │ <── SSE events ─────  │   (Gin)      │
   └──────────┘                       └──────┬───────┘
   ┌──────────┐                              │
   │ Vue GUI  │ ──── HTTP SSE ───────────────┘
   │ (browser)│
   └──────────┘

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

**CLI and GUI now share the same code path**: both connect to pchat-server via HTTP SSE. The CLI no longer runs the agent in-process — it auto-starts pchat-server as a child process (via `serverproc.Start`) and sends messages through `POST /api/v1/sessions/:id/messages`. The `ChatUI` renders SSE events the same way it always did, just over HTTP instead of direct channel reads.

## Frontend architecture (Vue 3 + Vite)

Served by the Go server at `/app/index.html`, same SPA for browser and Wails desktop.

```
cmd/pchat-gui/frontend/src/
├── main.ts                  → createApp + Naive UI + router
├── App.vue                  → root layout (sidebar + chat pane)
├── api/
│   └── client.ts            → HTTP client (SSE streaming, JSON fetch, types)
├── stores/
│   └── chat.ts              → Pinia store (sessions, messages, state.streaming)
└── components/
    ├── ChatWindow.vue       → message list + InputArea
    ├── InputArea.vue        → text input, attachments, plan/execute toggle
    ├── MessageBubble.vue    → renders one Message from parts[]
    ├── TypedText.vue        → live text render (marked.parse + blinking caret)
    ├── ThinkingBlock.vue    → collapsible thinking panel (<button> + v-show)
    ├── ToolCallCard.vue     → tool call card (name, args, result, elapsed)
    ├── SubAgentCard.vue     → nested sub-agent run with own parts[]
    ├── SessionSidebar.vue   → session list, project selector, settings
    ├── CommandPalette.vue   → / slash command inline autocomplete
    ├── TodoPanel.vue        → pending todos from agent loop (interactive: check, edit, remove)
    ├── QuestionModal.vue    → multi-choice question dialog (LLM asks, user answers)
    ├── ImageLightbox.vue    → full-screen image viewer
    ├── LoadingDots.vue      → sub-agent loading spinner
    └── AppSettingsModal.vue → provider/model/style management
```

### Message parts model

Assistant messages are a flat `parts[]` array, one entry per logical unit:

| Kind       | Component         | Notes |
|------------|-------------------|-------|
| `text`     | TypedText / static md | Live text part uses TypedText (blinking caret); static parts use `marked.parse()` |
| `thinking` | ThinkingBlock     | Collapsible panel; open by default when streaming, collapsed otherwise |
| `tool`     | ToolCallCard     | Shows name, args, status (start/ok/error), result, elapsed |
| `sub_agent` | SubAgentCard    | Nested card with own parts[]; recursive but capped (sub-agents can't spawn sub-agents) |

User/system messages use legacy `content` string → `marked.parse()`.

### Streaming data flow

```
LLM (Go agent.ChatWithTools)
  │ ChatStreamChunk → chan
  ▼
Gin handler (c.Stream + X-Accel-Buffering: no)
  │ SSE data: {"type":"content","content":"Hello"}
  ▼
client.ts streamMessages()
  │ fetch() → ReadableStream reader
  │ for each event: onEvent(ev) → await setTimeout(0)  ← yields event loop
  ▼
chat store (appendStreamEvent)
  │ appends content to trailing TextPart.text
  │ appends thinking to trailing ThinkingPart.text
  │ creates ToolPart / SubAgentPart on tool events
  │ emits "question" event → QuestionModal opens (user answers → POST /question-response → tool unblocks)
  ▼
Vue reactivity → re-render affected DOM nodes
```

**Critical**: `setTimeout(0)` after each content/thinking event forces Vue to flush between events. Without it, all events in one TCP packet are processed in a single microtask, Vue batches into one frame, and text "appears all at once".

### TypedText (live text rendering)

Thin wrapper: `marked.parse(text)` → v-html + CSS `::after` caret. No rAF loop, no artificial speed — the "typewriter feel" is the natural SSE cadence. The caret blinks on the trailing text part of an actively-streaming message.

### ThinkingBlock (thinking process)

- **Collapsible panel**: `<button>` header + `v-show` body (not native `<details>`)
- **Auto-open during streaming**: `defaultOpen` → `open` ref, sticky after user toggles manually
- **Header**: triangle caret (rotates 90° on open), icon (gear/lightbulb), label ("思考中…" / "思考过程"), character count
- **Content**: `<pre>` with `white-space: pre-wrap; word-break: break-all` for auto-wrapping
- **Visual**: minimal border, accent-tinted background when open + streaming

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
  pchat/          → CLI (Go REPL, thin SSE client)
  pchat-server/   → HTTP server (Gin)
  pchat-gui/      → Vue 3 frontend + Wails v2

internal/
  agent/         → ReAct tool loop, message parts, attachment expansion
  config/        → ~/.p-chat/config.json management
  llm/           → ChatMessage, ProtocolAdapter, StreamChunk, error classification
  memory/        → SQLite conversation store (chat history + metadata)
  server/        → Gin HTTP handlers (sessions, messages, uploads, providers, projects, skills, archive)
  tool/          → Tool registry (exec, read/write, todo_write, question, sub-agent)
  cli/           → REPL, commands, ChatUI terminal rendering, SSE event adapter
  subagent/      → Nested agent runner
  style/         → Personality style management
  agents/        → AGENTS.md instructions loader
  rules/         → .rules/ directory watcher
  skill/         → .skills/ directory loader
  sandbox/       → Tool execution guards
  project/       → Project directory registry
  httpcli/       → HTTP client for CLI REPL (shared SSE path)
  paths/         → ~/.p-chat directory resolution
  knowledge/     → Knowledge retrieval
  recall/        → Memory recall augment
  serverproc/    → Server process lifecycle
  project/       → Project directory registry (~/.p-chat/projects.json)
```

## Server API

All endpoints under `/api/v1/`.

### Core
| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check |
| GET | `/styles` | List styles |
| GET/POST/PATCH/DELETE | `/styles/:id` | CRUD styles (identity + soul) |
| GET/POST/PATCH/DELETE | `/providers` / `/:name` | Provider management |
| POST/PUT/DELETE | `/providers/:name/models` / `/:model` | Per-model CRUD |
| POST | `/providers/:name/models/:model/default` | Set default model |
| PATCH | `/providers/:name/models/:model/capabilities` | Set model capabilities (vision, thinking) |

### Sessions
| Method | Path | Description |
|--------|------|-------------|
| GET/POST | `/sessions` | List / create (filter by `?project_path=`) |
| GET/PATCH/DELETE | `/sessions/:id` | Get / update meta / archive |
| GET | `/sessions/:id/messages` | History |
| POST | `/sessions/:id/messages` | **Send message (SSE stream)** |
| POST | `/sessions/:id/compress` | Compress conversation |
| PATCH | `/sessions/:id/reasoning-effort` | Set DeepSeek/OpenAI thinking level |
| POST | `/sessions/:id/system-message` | Save custom system prompt |
| GET | `/sessions/:id/todos` | Get pending todos |
| POST | `/sessions/:id/question-response` | Submit answer to pending question |
| DELETE | `/sessions/:id/messages` | Clear session messages |

### Archive
| Method | Path | Description |
|--------|------|-------------|
| POST | `/sessions/:id/archive` | Archive (soft delete) |
| POST | `/sessions/:id/unarchive` | Restore from archive |
| GET | `/sessions/archived` | List archived sessions |
| DELETE | `/sessions/:id/permanent` | Permanent delete |

### Projects
| Method | Path | Description |
|--------|------|-------------|
| GET/POST/DELETE | `/projects` | List / add / remove project directories |
| POST | `/dialog/folder` | Open native folder picker dialog |

### Skills
| Method | Path | Description |
|--------|------|-------------|
| GET | `/skills` | List installed skills |
| GET | `/skills/:name` | Get skill detail |
| POST | `/skills/install` | Install skill (`{name, url}`) |
| DELETE | `/skills/:name` | Uninstall skill |
| GET | `/skills/search?q=` | Search skill repos |
| GET/POST/DELETE | `/skills/repos` | Manage skill source repos |

### Other
| Method | Path | Description |
|--------|------|-------------|
| POST | `/uploads` | File upload (multipart) |
| GET | `/uploads/:id` | Serve uploaded file |
| GET | `/commands` | List slash commands |
| POST | `/commands/:name` | Execute slash command |

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

## Agent loop

The agent runs a ReAct-style tool-use loop (`internal/agent/agent.go` `ChatWithTools`):

```go
// Infinite loop — the LLM decides when to terminate:
//   - len(toolCalls) == 0 → natural completion
//   - context >120 meaningful messages → auto-stop with suggestion
//   - user cancels (ctx.Err() != nil) → abort
for round := 1; maxRounds == 0 || round <= maxRounds; round++ {
    // 1. Call LLM (streaming)
    // 2. Parse tool calls from response (native or markdown)
    // 3. Clean markdown tool_call blocks from text content
    // 4. If no tool calls → done, return
    // 5. Execute tools (parallel for same-round calls)
    // 6. Append tool results to context for next round
    // 7. Convert tool results to User role (DeepSeek compat)
    // 8. persistAssistant() snapshots parts AFTER tool execution
}
```

### Loop exit conditions

| Condition | Phase | Behavior |
|-----------|-------|----------|
| `len(toolCalls) == 0` | `done` | LLM finished naturally |
| `meaningful > 80` | `context_warn` | Warning only |
| `meaningful > 120` | `context_warn` | Auto-stop, suggest /compress |
| `ctx.Err() != nil` | (error path) | User cancelled |

### Plan Mode

Per-session toggle (`🔨 构建` / `📋 计划`) stored in session metadata as `plan_mode`.
When enabled:
- Only `todo_write` and `question` tools are available (execution tools disabled)
- `maxRounds = 1` (single turn)
- The LLM produces a step-by-step plan, optionally broke down via `todo_write`, and may
  ask clarifying questions with `question` if requirements are vague.
- User reviews, then switches back to build mode to execute.

### Tool result persistence

`persistAssistant()` is called AFTER tool execution (not after LLM response), so the persisted snapshot includes thinking + text + tool start + tool results for the full round.

### DeepSeek compatibility

`normalizeToolResults()` converts ToolResult role→User role so DeepSeek models accept the tool-result messages. Applied in `agent.go` and `handler.go`.

### Question tool (pause/resume)

The `question` tool allows the LLM to pause execution and ask the user a
multiple-choice question before continuing. Architecture:

1. **LLM calls `question` tool** with `{ questions: [...] }` (1-4 questions, 2-4 options each)
2. **Tool handler** (`internal/tool/question.go:handleQuestion`):
   - Sends question payload through the tool event channel as `ChatStreamChunk{QuestionJSON: ...}`
   - Registers a response channel keyed by session ID
   - **Blocks** on the response channel (with 5-minute timeout)
3. **SSE handler** maps `QuestionJSON` → `StreamEvent{type: "question"}`, forwarded to frontend
4. **Frontend** (`QuestionModal.vue`): renders modal with radio/checkbox options + navigation
5. **User answers** → `submitQuestionAnswer()` → `POST /api/v1/sessions/:id/question-response`
6. **Server** delivers answer to the blocked tool handler's channel
7. **Tool handler** resumes, returns answer as tool result, LLM continues the loop

### Todo persistence

Todo lists are now persisted to SQLite (`todo_items` table) via the
`tool.PersistTodos` hook. On `GET /sessions/:id/todos`, the handler
hydrates the in-memory cache from SQLite on cold start, ensuring todos
survive server restarts.

### System prompt instructions

The static system prompt now includes:
- **Task Planning**: instructions encouraging `todo_write` use for complex multi-step tasks
- **Asking the User**: instructions for when to use the `question` tool (requirements clarification, architecture decisions, plan confirmation)

### CLI/terminal display (TODO)

The `question` tool currently only surfaces the question modal in the Vue GUI.
Terminal (CLI) rendering via the `cli/` package is not yet implemented — the
CLI REPL should render questions inline with keyboard navigation. This is
deferred to a future iteration.

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
