# Sub-Agent System

P-Chat's `task` tool lets the parent LLM spawn a focused sub-agent
to handle a multi-step sub-task — in parallel with other sub-tasks
or in isolation from the parent's context. This document covers
the architecture, the user-facing config format, the built-in
agents, the comparison with Claude Code and opencode's
implementations, and the roadmap of features we still plan to add.

For the high-level chat architecture see
[`AGENTS.md`](../AGENTS.md). This file is the deep dive on
sub-agents only.

---

## 1. Architecture

```
┌────────────────────────────────────────────────────────────────┐
│ Parent Agent (ReAct loop in internal/agent/agent.go)           │
│                                                                │
│   LLM emits: tool_call{name:"task", args:{                    │
│     description:"...task...",                                  │
│     subagent_type:"explore",         ← which agent to spawn    │
│     task_id:"audit-2025-01-15",     ← resume-by-id (optional)  │
│     model:"gpt-4o-mini",            ← per-call override        │
│     prompt:"You are a custom ...",  ← system prompt override   │
│   }}                                                          │
│                                                                │
│   tool dispatcher launches goroutine → tool handler            │
│     in internal/subagent/subagent.go:Tool()                    │
│                                                                │
│     ┌──────────────────────────────────────────────────┐       │
│     │ Runner (Default struct)                          │       │
│     │   - resolve subagent_type via Registry           │       │
│     │   - check (TaskID, SubagentType, Model) in Cache │       │
│     │     → return cached Result on hit               │       │
│     │   - emit synthetic start event (with metadata)   │       │
│     │   - build child tool registry:                   │       │
│     │       * drop `task` and `recall` (recursion)     │       │
│     │       * apply global allowed/denied list         │       │
│     │       * apply per-agent whitelist                │       │
│     │   - create fresh in-memory memory.Store          │       │
│     │   - invoke agent.New(...).ChatWithTools(...)     │       │
│     │   - forward every chunk to parent's eventCh      │       │
│     │   - emit synthetic ok/err event                  │       │
│     │   - cache the Result for future resume           │       │
│     └──────────────────────────────────────────────────┘       │
│                                                                │
│   every chunk tagged: chunk.SubAgent=true                     │
│                       chunk.SubAgentType=...                  │
│                       chunk.SubAgentColor=...                 │
│                       chunk.SubAgentModel=...                 │
│                       chunk.SubAgentTaskID=...                │
└────────────────────────┬───────────────────────────────────────┘
                         │ per-tool eventCh (chan ChatStreamChunk)
                         ▼
┌────────────────────────────────────────────────────────────────┐
│ Parent's forwarder goroutine (agent.go:1064-1114)              │
│                                                                │
│   - partsAccumulator.update(ev) — nest into the sub_agent part │
│   - ch <- ev                                                  │
│                                                                │
│   → SSE → frontend SubAgentCard.vue / CLI progress.go         │
└────────────────────────────────────────────────────────────────┘
```

The sub-agent gets:

- **A fresh `agent.Agent` instance** built with the parent's
  `Cfg`, `LLM`, `StyleMgr`, and a filtered copy of the parent's
  tool registry. The `task` and `recall` tools are always
  removed to prevent recursion; the per-agent `Tools` list
  (from `Registry.Get(name)`) further narrows the set.
- **A fresh in-memory `memory.Store`** so the sub-agent starts
  with no shared conversation history.
- **A fresh system prompt** (the parent's normal prompt + the
  agent's `Prompt` field, OR the request's `prompt` override
  when set). The system prompt is the only piece of context
  the sub-agent sees from the parent.
- **A per-call timeout** (default 5 minutes, configurable via
  `subagent.timeout` in `~/.p-chat/config.json`).
- **A fresh event channel** so the parent's UI can stream
  the sub-agent's progress in real time.

The sub-agent does NOT see:

- The parent's message history (no prior turns, no system
  messages, no assistant replies).
- The parent's compressed summary.
- The parent's reasoning effort / per-session state.
- The parent's session ID — the chat request is constructed
  without `SessionID`, so any tool that depends on it
  (e.g. `todo_write` persistence) will fail.
- The parent's attachments, plan mode, or skill context.

The tool description in `subagent.go` warns the parent LLM
about this isolation:

> "The sub-agent has no knowledge of the parent
> conversation, so include all necessary context."

---

## 2. Tool schema

The `task` tool's JSON schema is built dynamically from the
registry at startup. Every parent LLM call sees an
up-to-date list of available agents appended to the
description.

```json
{
  "name": "task",
  "description": "Spawn an isolated sub-agent to handle a focused, well-defined sub-task. ...\n\nAvailable agent types:\n- explore: Fast read-only agent specialized for exploring codebases. ...\n- general-purpose: General-purpose agent for multi-step tasks. ...\n- plan: Read-only architect that produces a step-by-step implementation plan. ...\n\nPrefer specialized agents (explore, plan) over general-purpose when they fit.",
  "parameters": {
    "type": "object",
    "properties": {
      "description":     { "type": "string", "description": "Clear, self-contained description of the sub-task. ..." },
      "subagent_type":   { "type": "string", "description": "Optional. Name of a registered sub-agent (e.g. 'explore', 'plan', 'general-purpose', or a custom agent from .p-chat/agent/*.md). Defaults to 'general-purpose' when omitted." },
      "model":           { "type": "string", "description": "Optional model name override. Defaults to the parent's model." },
      "prompt":          { "type": "string", "description": "Optional system prompt override. Replaces the parent's prompt for this run." },
      "style":           { "type": "string", "enum": ["cute","guofeng","tech"], "description": "Optional personality for the sub-agent." },
      "provider":        { "type": "string", "description": "Optional LLM provider name from the parent config." },
      "task_id":         { "type": "string", "description": "Optional stable identifier. Two calls with the same (task_id, subagent_type, model) return the cached result of the first run without re-executing the sub-agent." }
    },
    "required": ["description"]
  }
}
```

### Argument resolution order

When the LLM calls the tool, the runner resolves each
argument in this order (first non-empty wins):

| Argument       | Source priority                                  |
|----------------|--------------------------------------------------|
| `description`  | required                                         |
| `subagent_type`| Args.subagent_type → `"general-purpose"` default |
| `model`        | Args.model → AgentInfo.model → parent model      |
| `prompt`       | Args.prompt → AgentInfo.prompt → parent prompt   |
| `style`        | Args.style → parent style                        |
| `provider`     | Args.provider → parent provider                  |
| `task_id`      | Args.task_id (no fallback)                       |

The agent's `Model` and `Prompt` are only applied when the
request didn't pass its own override. This lets the LLM
"borrow" a specialized agent's whitelist and tool set while
still customizing the model or system prompt for one specific
call.

---

## 3. Built-in agents

Three built-in agents ship with P-Chat, defined as Go
constants in `internal/subagent/builtins.go`:

### `general-purpose` (default)

- **Color**: `#5B9BD5` (blue)
- **Model**: inherits parent
- **Tools**: inherits parent's filtered set
- **Use when**: no specialized agent fits
- **Prompt**: short — "focused sub-agent, return a single
  text response, do not call task yourself"

### `explore`

- **Color**: `#44BA81` (green)
- **Model**: inherits parent
- **Tools**: `read_file`, `list_files`, `exec_command`
  (the prompt forbids write operations)
- **Use when**: searches, file lookups, codebase Q&A
- **Prompt**: file search specialist — "use list_files for
  directory contents, use read_file for individual files,
  use exec_command for grep/find/ls/git/cat/head/tail only"

### `plan`

- **Color**: `#E8A33D` (orange)
- **Model**: inherits parent
- **Tools**: same as `explore` (read-only)
- **Use when**: need a design before executing
- **Prompt**: read-only architect — "produce a numbered
  step-by-step plan, list critical files, do NOT execute
  the plan"

These three are intentionally short. opencode and Claude
Code ship much longer prompts (20-200+ lines), but P-Chat's
parent already has rich style + AGENTS + rules + skills
context, so the sub-agent prompt is an overlay, not a
complete instruction set.

---

## 4. User-defined agents (`.p-chat/agent/*.md`)

Users can declare custom agents in two locations, layered
in this order (last wins):

1. **Global**: `~/.p-chat/agent/*.md`
2. **Per-project**: `<project>/.p-chat/agent/*.md`

A project-level agent with the same name as a global agent
replaces it (project wins, mirroring the config layering
rule). Built-in agents can also be overridden by user-defined
files of the same name (rare, but useful for customizing
`explore` to your codebase's conventions).

### File format

```markdown
---
name: code-reviewer            # required; defaults to the file stem
description: Reviews staged changes for correctness and style.
model: gpt-4o-mini             # optional; "model name" (no provider prefix)
color: "#E67E22"               # optional; "#RRGGBB" or CSS color name
tools: [read_file, exec_command, todo_write]
hidden: false                  # optional; exclude from the parent LLM's tool description
---
You are a senior code reviewer.

When invoked:
1. Run `git diff` to see what's staged.
2. Read each changed file in full.
3. Check for: correctness, naming, error handling, test coverage.
4. Write a numbered list of issues with file:line references.
5. End with a verdict line: VERDICT: approve | request-changes | needs-discussion
```

### Frontmatter schema

| Field         | Type     | Required | Notes |
|---------------|----------|----------|-------|
| `name`        | string   | no*      | *defaults to the file stem |
| `description` | string   | **yes**  | surfaced to the parent LLM |
| `model`       | string   | no       | model name only; no provider prefix |
| `color`       | string   | no       | hex or CSS color name |
| `tools`       | string[] | no       | per-agent tool whitelist |
| `hidden`      | bool     | no       | exclude from the tool description |

The body of the file (after the closing `---`) is the
agent's system prompt. It REPLACES the parent's prompt
when non-empty.

### YAML subset supported

The loader is a tiny purpose-built scanner (not a full
YAML library) to keep the binary small. It handles:

- `key: value` (string, with/without single or double quotes)
- `key: [a, b, c]` (inline string list)
- `key: true | false` (bool)
- `key:` (empty → `""`)
- `# comment` (comment, ignored)

Nested maps, block lists, and multi-line strings are NOT
supported. If a user needs those, the file is rejected
with a clear error suggesting the supported subset. We
can swap in `gopkg.in/yaml.v3` later if the simpler
parser becomes a problem.

### Loader behavior

- A missing directory is silently skipped (fresh install
  doesn't need to seed the agent directory).
- One bad file doesn't abort the whole load — the error
  is logged to stderr and the loader moves on.
- Subdirectories are NOT recursed (the opencode v1
  convention that allowed nested agents via
  `agents/team/build.md` → `team/build`; we keep the flat
  layout for v1 — easier to audit, easier to document).

---

## 5. Cache & resume

Sub-agent results are cached in-process. The cache is
keyed two ways:

### Legacy key: `(description, style, provider)`

The original cache key, kept for backward compatibility.
Two calls with the same wording return the cached result
without re-running. TTL controlled by
`subagent.cache_ttl` in `~/.p-chat/config.json`
(`"5m"`, `"1h"`, etc.; `""` or `"0"` disables).

### New key: `(task_id, subagent_type, model, style, provider)`

The new key supports explicit resumption. The LLM passes
a `task_id` in the tool call, and a second call with the
same `task_id` (and the same agent + model) returns the
cached result without re-running. This is what the
parent LLM uses to dedupe retries and resume interrupted
runs.

```python
# The parent LLM might do this:
result1 = task(description="Audit auth", subagent_type="explore", task_id="audit-auth-2025-01-15")
# ... later, after a context-window truncation ...
result2 = task(description="Continue the audit", subagent_type="explore", task_id="audit-auth-2025-01-15")
# → returns result1's output without re-running
```

The cache is best-effort: a cache miss just runs the
sub-agent normally. Cache hits are returned with `ok=true`
from `Cache.Get` / `Cache.GetByKey`.

### The `task_id` badge in the GUI

The `task_id` is surfaced in the SubAgentCard footer as a
small monospace chip. Clicking the chip copies it to the
clipboard so the user can pass it back to the LLM (or into
a different session) for re-invocation. The card also
shows:

- The agent's name (e.g. "explore", "plan") as a colored
  badge, using the agent's accent color.
- The model in use as a small chip (e.g. "gpt-4o-mini").
- The full task description as the card's primary label.
- The status (运行中…, 已完成, 失败) and elapsed time.

---

## 6. Permission model

Three layers of tool isolation, applied in priority order
(each can further narrow but not widen):

1. **Hard exclusions** (always applied):
   - `task` — prevents sub-agents from spawning sub-agents
   - `recall` — coordination tool that should stay at the
     top level (not yet implemented; reservation)

2. **Global allow/deny** (`subagent.allowed_tools` /
   `subagent.denied_tools` in `~/.p-chat/config.json`):
   - If `allowed_tools` is non-empty, only those tools
     pass (whitelist wins).
   - Otherwise, `denied_tools` removes the listed tools
     (blacklist).
   - Default `denied_tools`: `["exec_command"]` (sub-agents
     can't shell out by default; `explore` / `plan` opt
     back in via their per-agent `Tools` list).

3. **Per-agent whitelist** (`Tools` field in the agent's
   `.md` frontmatter or `Builtins()`): when non-empty,
   only those tools are exposed (still minus the hard
   exclusions in layer 1).

   ```yaml
   # Example: a research agent that can ONLY read.
   ---
   name: research
   description: Read-only research agent.
   tools: [read_file, list_files, exec_command]
   ---
   ```

### Recursion depth

**1.** A sub-agent cannot call `task` (layer-1 exclusion).
The recursion guard is structural, not enforced by a
counter. This means the worst case is one level of
nesting, no exponential blowup.

---

## 7. Stream protocol

The sub-agent's events flow back to the parent through
a **per-tool event channel** (`toolEventChanKey{}` in
`agent.go`). Every chunk the sub-agent emits is:

1. Tagged with `SubAgent=true`, `SubAgentTask=<description>`,
   `SubAgentType=<agent name>`, `SubAgentColor=<color>`,
   `SubAgentModel=<model>`, `SubAgentTaskID=<id>`.
2. Pushed to the parent's per-tool `eventCh` (buffer 16).
3. Picked up by a per-tool forwarder goroutine in the
   parent loop.
4. Fed into the parent's `partsAccumulator` (so the
   nested card survives a session reload).
5. Forwarded to the main SSE channel for the live UI.

### Synthetic lifecycle events

The runner emits two synthetic events that bracket the
sub-agent's stream:

| Phase                  | SubAgentStatus | SubAgentModel | Emitted when |
|------------------------|----------------|---------------|--------------|
| `"sub_agent_start"`    | `"start"`      | empty         | before the sub-agent runs |
| `"sub_agent_ok"`       | `"ok"`         | resolved      | after the sub-agent's stream drains, success |
| `"sub_agent_err"`      | `"err"`        | resolved      | after the sub-agent's stream drains, failure |

In between, every chunk the sub-agent produces carries
`SubAgent=true` and the agent's metadata. The wire
format mirrors opencode's `SubtaskPart` schema.

---

## 8. UI rendering

### GUI (`SubAgentCard.vue`)

The card has two display modes:

**Collapsed (default after completion):** 36px single-row strip
- Agent name (colored badge, e.g. "explore")
- Task description
- Model chip (e.g. "gpt-4o-mini")
- Status label (运行中…, 已完成, 失败)
- Elapsed time
- Caret (▸/▾)

**Expanded (default while running, or on click):** full
nested card
- Same header as collapsed
- Inner stream: ThinkingBlock + ToolCallCard + text
- `task_id` footer (when set): click to copy

The agent's accent color drives the left-border tint, the
icon background, and the agent-name badge background —
all via CSS custom properties (`--sub-accent`,
`--sub-accent-soft`) so a single `agentColor` field
re-themes the whole card without component changes.

### CLI (`progress.go`)

The CLI renders sub-agent events indented under the
parent tool call:

```
  ↳ explore Audit auth code
    ↳ LLM: round 1
    ↳ read_file(auth.ts)
    ↳ ✓ read_file
    ↳ exec_command(git diff)
    ↳ ✓ exec_command
    ↳ LLM: round 2
    ↳ read_file(handlers.ts)
    ↳ ✓ read_file
  ↳ ✓ explore (4.2s) [gpt-4o-mini]
```

Text deltas are no longer suppressed (the previous design
let the parent's task result show the full final text,
but made sub-agents feel opaque). Now the text is
printed with `color.FgHiBlack` and an indent so it
doesn't drown out the parent's stream.

---

## 9. Comparison: Claude Code, opencode, P-Chat

| Feature                              | Claude Code               | opencode                        | P-Chat                          |
|--------------------------------------|---------------------------|---------------------------------|---------------------------------|
| **Tool name**                        | `Agent` (legacy `Task`)   | `task`                          | `task`                          |
| **Sub-agent config**                 | `.claude/agents/*.md`     | `.opencode/agent/*.md`          | `.p-chat/agent/*.md`            |
| **Built-in agents**                  | 6 (general, statusline, Explore, Plan, claude-code-guide, verification) | 6 (build, plan, general, explore, compaction, title) | 3 (general-purpose, explore, plan) |
| **Markdown frontmatter**            | name, description, tools, model, color, maxTurns, permissionMode, skills, mcpServers, hooks, memory, isolation | name, description, prompt, model, temperature, top_p, tools, color, mode, hidden, steps, options, permission | name, description, model, color, tools, hidden |
| **Model per sub-agent**              | `model: sonnet\|opus\|haiku\|inherit\|...` | `model: providerID/modelID` (e.g. `opencode/claude-haiku-4-5`) | `model: <model name>` (no provider prefix) |
| **Per-agent system prompt**          | `getSystemPrompt(agent)` (body of .md) | body of .md → `prompt` field | body of .md → `Prompt` field |
| **Per-agent tool filter**            | `tools: ["*"]` + `disallowedTools: ["Agent", "Edit", "Write", ...]` | legacy `tools: { "*": false, "github-pr-search": true }` (migrated to permission) OR `permission:` ruleset | `tools: ["read_file", "list_files"]` (whitelist) |
| **Permission model**                 | per-agent `permissionMode` (default, acceptEdits, plan, dontAsk, bubble, bypassPermissions) | permission ruleset (allow/deny/ask by pattern) + `deriveSubagentSessionPermission` (parent deny + external_directory + subagent's own) | hard exclusions + global allow/deny + per-agent whitelist |
| **Recursion depth**                  | 1 (hard-coded via `ALL_AGENT_DISALLOWED_TOOLS`) | 1 by default (opt-in via `task: allow`) | 1 (hard exclusion) |
| **Async / background mode**          | ✅ `run_in_background: true` → `status: 'async_launched'` + output file + `task-notification` injected back | ✅ `background: true` → `BackgroundJob` + `injectBackgroundResult` synthetic assistant text | ❌ not yet (roadmap) |
| **Resumption by task_id**            | ✅ `task_id` resumes from disk JSONL sidechain transcript | ✅ `task_id` resumes the same `Session` row (linked via `parentID`) | ✅ `task_id` resumes from in-process cache (no disk persistence yet) |
| **Clickable card → child session**   | ✅ full child session in sidebar; tab routing | ✅ child session in sidebar; `task-tool-card` has chevron → navigate | ❌ card shows metadata but doesn't navigate (cache-only, no child session) |
| **Worktree isolation**               | ✅ `isolation: 'worktree'` → temporary git worktree | ❌ | ❌ (roadmap) |
| **Per-agent memory**                 | ✅ `memory: user\|project\|local` → persistent file in `~/.claude/agent-memory/<name>/` | ❌ | ❌ (roadmap) |
| **Per-agent MCP servers**            | ✅ `mcpServers: [name]` (frontmatter) → agent-scoped MCP | ❌ (parent's MCP is shared) | ❌ (parent's MCP is shared) |
| **Per-agent preloaded skills**       | ✅ `skills: [name1, name2]` (frontmatter) → skill content as first user message | ❌ | ❌ (roadmap) |
| **Per-agent hooks**                  | ✅ `hooks: { ... }` (frontmatter) → SubagentStart/SubagentStop | ❌ | ❌ (roadmap) |
| **Fork mode**                        | ✅ `/fork <directive>` → backgrounded sub-agent with cache-stable parent prefix | ❌ | ❌ (roadmap) |
| **Teammates / swarm**                | ✅ `team_name` + `name` → split-pane, mailbox, `SendMessage` | ❌ | ❌ (out of scope — single-session focus) |
| **Card UI: agent color**             | ✅ 8 named colors (red/blue/...) | ✅ any `#RRGGBB` (per-agent) | ✅ any color (per-agent) |
| **Card UI: tool count + tokens**     | ✅ `5 tool uses · 12,345 tokens` in `AgentProgressLine` | ❌ (CLI-only) | partial — `task_id` + agent type + model; tool count not yet |
| **Card UI: status spinner**          | ✅ `<Spinner />` while running | ❌ | ✅ shimmer gradient header |
| **Dynamic tool description**         | ✅ agent listing + per-call args | ✅ `describeTask` → list of registered agents | ✅ `Registry.Describe()` → list of registered agents |

### Key design choices we made (and why)

**Built-in prompts are short.** opencode ships 18-44 line
prompts, Claude Code ships 200+ line prompts. P-Chat's
parent already has rich style + AGENTS + rules + skills
context, so the sub-agent prompt is an overlay, not a
complete instruction set. The `general-purpose` prompt
is 8 lines.

**Tool whitelist over deny-everything.** P-Chat's tool
registry is small (~12 names). An explicit `tools: [...]`
whitelist is easier to audit and translate to `/agents`
than the opencode "deny everything, then allow X" pattern.
The `explore` and `plan` agents explicitly include
`exec_command` so they can run `ls`/`grep`/`cat` — the
prompt forbids write operations, the whitelist is just a
backstop.

**No model = "provider/model".** opencode's `Model.parseModel`
splits a "providerID/modelID" string. P-Chat's LLM client
takes the provider and model as separate parameters
(matching the existing config schema), so the per-agent
`Model` is just a model name. Switching providers is done
via the `provider` arg. This is simpler but means
per-agent model selection is constrained to the parent's
provider.

**No per-agent memory in v1.** Claude Code's `agentMemory`
writes a persistent file per agent in `~/.claude/agent-memory/`.
opencode has no equivalent. P-Chat currently has no agent
memory — the sub-agent starts fresh every call. We can
add this later if it becomes a pain point (likely yes for
code-review / code-audit agents).

**In-process cache only.** Claude Code persists the
sub-agent's full transcript to a sidechain JSONL file on
disk and reloads it on `task_id` resume. opencode links
the child to the parent via `Session.parentID` so the
child is a real session row in the DB. P-Chat's cache
is in-process (`subagent.Cache`, a `map[string]cacheEntry`
behind a mutex). Survives a server restart? No. The
trade-off: simpler code, but resuming a sub-agent after
a server restart is a cache miss. We can add disk
persistence when the user actually needs it.

---

## 10. Roadmap

Prioritized list of features we plan to add, grouped by
effort. See [`AGENTS.md`](../AGENTS.md) for the project's
overall roadmap.

### Phase A (low effort, high value)

1. **Async / background mode** (opencode `background: true`).
   - The sub-agent tool emits an `async_launched` chunk with
     a `task_id` immediately and returns. The actual work
     happens in a goroutine.
   - When the background sub-agent finishes, the result is
     injected back into the parent as a synthetic assistant
     text part (`injectBackgroundResult`-style).
   - The card shows a "running in background" pill that the
     user can click to navigate to / wait for.

2. **Tool count + token count in the card** (Claude Code
   `AgentProgressLine`).
   - SubAgentCard header shows `5 tools · 12,345 tokens`
     next to the status. Already partially implemented —
     `Result` carries `TokensIn/TokensOut` and the
     `partsAccumulator` tracks tool calls. Just need a UI
     pass.

3. **Clickable card → child session in GUI**.
   - The card gets a chevron icon (opencode style). On
     click, opens a side panel with the sub-agent's full
     timeline. Reuses the existing chat UI.
   - Requires: sub-agent's events need to be persisted
     somewhere the panel can load. Either: (a) cache to
     disk like Claude Code, or (b) keep in-process and
     just not support cross-session navigation.

4. **Color picker for built-in agents** (so users can
   customize `explore` / `plan` colors without writing a
   `.md` file). Simple settings field in the Wails GUI.

5. **`/agents` slash command** in the CLI — list all
   registered agents with their description and source
   (`builtin`, `~/.p-chat/agent/foo.md`, project path).

### Phase B (medium effort)

6. **Disk-persisted cache** (Claude Code sidechain JSONL).
   - Each completed sub-agent run writes a JSONL file to
     `~/.p-chat/subagents/<task_id>.jsonl`.
   - The runner's `Cache.GetByKey` checks disk first,
     in-memory second.
   - Resumption after a server restart becomes possible.
   - Required for: sub-agent navigation, async-mode
     background result injection, audit logs.

7. **Preloaded skills per agent** (Claude Code
   `skills: [...]`).
   - YAML frontmatter gains a `skills: [name1, name2]`
     field. The runner loads the skill bodies and inserts
     them as the first user message (before the task).
   - Useful for: code-review agent with a "review-style"
     skill preloaded; doc-writer agent with a "tone-of-voice"
     skill preloaded.

8. **MCP servers per agent** (Claude Code `mcpServers: [...]`).
   - The sub-agent's tool registry includes the agent's
     MCP servers in addition to the parent's.
   - Useful for: github-pr-triage agent with a `github-mcp`
     server only available to that agent.

9. **Worktree isolation** (Claude Code `isolation: worktree`).
   - The sub-agent runs in a temporary git worktree.
   - The worktree is deleted on success (no changes), or
     returned to the parent on failure so the user can
     review what the agent did.
   - Useful for: code-edit agents that should not touch
     the user's working tree.

### Phase C (large effort, strategic)

10. **Per-agent persistent memory** (Claude Code
    `memory: user|project|local`).
    - Each agent can have a persistent `AGENT.md`-like file
      loaded at startup. Scopes:
      - `user`: `~/.p-chat/agent-memory/<name>/MEMORY.md`
        (shared across all projects)
      - `project`: `<project>/.p-chat/agent-memory/<name>/MEMORY.md`
      - `local`: `~/.p-chat/agent-memory/<name>/<project-hash>/MEMORY.md`
        (per-user, per-project)
    - The agent can read and write to its memory file via
      a new `memory_read` / `memory_write` tool.
    - Useful for: long-running audit agents that build up
      context over multiple invocations.

11. **Teammates / multi-agent collaboration** (Claude Code
    `team_name` + `name`).
    - Sub-agents can be given a stable `name` and can
      communicate with each other via a `SendMessage` tool.
    - The GUI shows a split-pane view of the running
      teammates (Claude Code's tmux/swarm pattern).
    - **Out of scope for v2**: this is a major architectural
      change. Defer until we see real demand.

12. **Fork mode** (Claude Code `/fork <directive>`).
    - A sub-agent can be backgrounded mid-run with a
      cache-stable prefix of the parent's conversation.
    - Useful for: parallel exploration of "what if" branches
      from a single point in the conversation.
    - **Out of scope for v2**: requires careful prompt
      caching and a new UX surface.

### Out of scope

The following are upstream features we do NOT plan to
adopt because they don't fit P-Chat's architecture:

- **Claude Code's `verification` agent** — adversarial
  verifier with `VERDICT: approve|request-changes|...`
  output. P-Chat's plan mode already covers the "review
  before executing" use case.

- **opencode's `compaction` / `title` / `summary` agents** —
  these are opencode-internal session features (history
  compaction, title generation, share-page summary). P-Chat
  handles those internally without a sub-agent.

- **Claude Code's `claude-code-guide` agent** — a docs
  fetcher over `code.claude.com/docs`. P-Chat has no
  equivalent docs surface.

- **opencode's `@file` / `@agent` reference syntax in
  prompts** (`@src/foo.ts`, `@general-purpose`) — these
  are slash-command-only features. P-Chat's slash commands
  are simpler; we can revisit if needed.

---

## 11. Reference

### File map

| File | Purpose |
|------|---------|
| `internal/subagent/registry.go` | `AgentInfo` + `Registry` (read-only catalog) |
| `internal/subagent/builtins.go` | Built-in agents (`general-purpose`, `explore`, `plan`) |
| `internal/subagent/markdown.go` | YAML frontmatter parser for `.p-chat/agent/*.md` |
| `internal/subagent/subagent.go` | `Default` runner, `Tool()` factory, cache, `Run()` |
| `internal/subagent/adapter.go` | Adapter from `Registry` → `agent.SubagentRegistry` interface |
| `internal/subagent/registry_test.go` | Unit tests for registry, markdown loader, cache |
| `internal/agent/agent.go:90-98` | `Agent.SetSubagentRegistry()` setter |
| `internal/agent/agent.go:608-710` | `toolEventChanKey{}` + `subagentRegistryCtxKey{}` context plumbing |
| `internal/agent/agent.go:323-393` | `ChatStreamChunk` with new `SubAgentType/Color/Model/TaskID` fields |
| `internal/agent/parts.go:124-220` | `partsAccumulator` sub-agent start/ok/err handling + metadata backfill |
| `cmd/pchat-server/main.go:92-135` | Sub-agent registry build + `task` tool registration |
| `frontend/src/components/SubAgentCard.vue` | Card UI (v2: agent name + color + model + task_id copy) |
| `frontend/src/stores/chat.ts:569-619` | `findOrCreateSubAgent` + `backfillSubAgentMetadata` |
| `frontend/src/api/client.ts:125-145` | `SubAgentPart` TS type with new fields |
| `internal/cli/progress.go:127-235` | `handleSubAgentEvent` (CLI rendering) |
| `internal/server/handler.go:412-428` | `StreamEvent` with new sub-agent fields |
| `internal/server/handler.go:1481-1484` | `chunkToEvent` field pass-through |
