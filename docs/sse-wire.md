# SSE Wire Format Reference

> **Audience**: integrators writing non-web clients (VS Code extension, Slack
> bot, CLI tooling, debug curl) that need to consume the P-Chat chat
> streaming endpoint.
>
> **Source of truth**: `internal/server/stream_adapter.go` (chunk → wire
> mapping) and `internal/server/handler.go::StreamEvent` (Go struct →
> JSON shape). When this doc disagrees with the code, **the code wins**;
> please open a PR to fix the doc.

---

## 1. Endpoint

```
POST /api/v1/sessions/:id/messages
Content-Type: application/json
```

Request body (`SendOptions`):

```jsonc
{
  "content": "string, the user message text",
  "trace_id": "T-9f3c4a2b",  // optional; minted server-side if absent
  "regen_of": 1234,           // optional; SQLite row id of the user msg
                              // being regenerated, for P1-4 regen history
  "attachments": [            // optional; multi-modal uploads
    { "id": "abc", "name": "x.png", "mime": "image/png" }
  ],
  "auto_continue": true,      // optional; per-session P0-3 toggle
  "plan_mode": false          // optional; plan mode = no tools, 1 turn
}
```

Response: `Content-Type: text/event-stream` (chunked, no `Content-Length`).
The response also carries an `X-Trace-Id: T-9f3c4a2b` header mirroring
the first event's `trace_id` for easy log correlation from curl.

---

## 2. SSE frame format

Every event is two lines, blank-line terminated, exactly as the
Server-Sent Events spec mandates:

```
data: {"type":"content","content":"hello","seq":1,"trace_id":"T-9f3c4a2b"}\n
id: 1\n
\n
```

- `data:` — one JSON object (newline-stripped on parse; see §6 for
  multi-line payloads).
- `id:` — the per-stream monotonic counter, identical to the JSON
  `seq` field. Surfaced on its own line so the browser's native
  `EventSource.lastEventId` and our fetch-path parser can both use
  it as a resume cursor.
- `\n\n` — frame terminator.

There is no `event:` line; we only emit the default `message` event
type. Clients that need event-type routing should switch on the JSON
`type` field instead.

---

## 3. `StreamEvent` schema

The full Go type lives at `internal/server/handler.go:590`. Below is
the wire shape, grouped by `type`.

### 3.1 `content` — assistant text delta

```json
{
  "type": "content",
  "content": "partial text…",
  "seq": 7,
  "trace_id": "T-9f3c4a2b"
}
```

Frontend contract: **append** to the trailing text part of the
assistant message. Empty `content` is a no-op.

### 3.2 `thinking` — reasoning / chain-of-thought delta

```json
{ "type": "thinking", "thinking": "Let me think…", "seq": 6 }
```

Only emitted by LLM clients that surface a separate reasoning stream
(Anthropic `thinking` blocks, DeepSeek `reasoning_content`, OpenAI
`o1` reasoning). Frontend: **append** to the trailing thinking part.

### 3.3 `tool` — tool call lifecycle

```json
{
  "type": "tool",
  "tool_id": "call-1",
  "tool_name": "read_file",
  "tool_status": "start",  // "start" | "ok" | "warn" | "error"
  "tool_args": "{\"path\":\"/etc/hosts\"}",  // optional, JSON string
  "tool_result": "127.0.0.1 localhost\n…",   // ≤ 300 chars preview
  "tool_result_full": "...",                  // full payload (newlines preserved)
  "tool_error": "",                           // populated on status="error"
  "tool_elapsed": "120ms",                    // set on ok / error only
  "seq": 8
}
```

The status is parsed from the agent's internal `call-N-status` step
field (see `internal/agent/parts.go::ToolStatusFromStep` for the
canonical mapping). Frontend: status drives the ToolCallCard colour
and the OK/error icon; `tool_result_full` is preferred over
`tool_result` when the frontend needs to JSON.parse the output
(`todo_write` and `question` do).

### 3.4 `phase` — system status / sub-agent lifecycle

```json
{
  "type": "phase",
  "phase": "sub_agent_ok",       // see table below
  "step": "call-3-ok",           // optional
  "message": "task complete",    // optional human-readable
  "sub_agent": true,             // only on sub-agent events
  "sub_agent_task": "list repo",
  "sub_agent_status": "ok",
  "sub_agent_type": "explore",
  "sub_agent_color": "#5b9bd5",
  "sub_agent_model": "gpt-4o-mini",
  "sub_agent_task_id": "T-abc",
  "sub_agent_description": "Read-only repo survey…",
  "sub_agent_failure_reason": "",  // set on sub_agent_err only
  "seq": 12
}
```

`phase` values the frontend should know about:

| `phase` | Meaning |
|---|---|
| `llm` | Mid-LLM-call heartbeat (`step="round-N"`) |
| `tool` | Internal step counter (no UI action) |
| `sub_agent_start` | Sub-agent run began |
| `sub_agent_ok` | Sub-agent run finished successfully |
| `sub_agent_err` | Sub-agent run failed; check `sub_agent_failure_reason` |
| `auto-continue` | P0-3 guard re-injected a "未完成" prompt; `message` is the user-facing line |
| `system`, `memory`, `plan` | Internal bookkeeping; safe to ignore |

### 3.5 `error` — LLM or transport error

```json
{
  "type": "error",
  "error": "upstream returned 401",
  "error_kind": "auth_error",   // see §5
  "suggestion": "check the API key for provider 'cs' in ~/.p-chat/config.yaml",
  "seq": 14
}
```

Frontend contract: mark the assistant message as failed, render the
error text, and surface the "复制 trace id" button using the last
seen `trace_id`.

### 3.6 `done` — stream end

```json
{
  "type": "done",
  "tokens_in": 1234,
  "tokens_out": 567,
  "elapsed": "3.42s",
  "provider": "cs",
  "model": "gpt-4o",
  "user_message_id": 4321,   // SQLite row id of the user msg that started this turn
  "last_message_id": 4322,   // highest row id in this session (typically the assistant reply)
  "seq": 99
}
```

`user_message_id` and `last_message_id` are populated only here, so
the frontend should read them from this event, not from any prior
chunk. The stream always ends with exactly one `done`.

### 3.7 `content_rewrite` / `thinking_rewrite` — post-stream redact

```json
{ "type": "content_rewrite", "content": "the new full text", "seq": 50 }
{ "type": "thinking_rewrite", "thinking": "the new full thinking", "seq": 51 }
```

These carry the **full replacement text** for the trailing part of
the respective kind. The agent emits them when its post-stream
redactor (`internal/agent/util.go::redactPhantomErrors`) detects a
phantom error message the LLM appended to its own output. Frontend:
**replace**, not append.

### 3.8 `session_status` — chat turn lifecycle

```json
{ "type": "session_status", "session_status": "busy", "seq": 1 }
{ "type": "session_status", "session_status": "idle", "seq": 100 }
```

- `busy` is emitted at the start of the agent loop.
- `idle` is emitted on every exit (success, error, cancel,
  max-rounds, stuck, panic).

The frontend uses this to drive the TodoPanel state machine: a
`busy` toggles `state.sessionWorking[id] = true`; an `idle` clears
it. Without this signal, the UI has no way to distinguish "the LLM
is mid-turn, don't clear stale todos" from "the LLM stopped and
forgot to clear them".

### 3.9 `question` — LLM asks the user a question

```json
{
  "type": "question",
  "question_json": "[{\"header\":\"Q1\",\"question\":\"…\",\"options\":[…],\"multiSelect\":false}]",
  "seq": 20
}
```

`question_json` is a string-encoded JSON array of `Question` objects
(matches the Anthropic `question` tool spec). Frontend: parse,
render a modal, post the answer back to
`POST /api/v1/sessions/:id/question`.

### 3.10 `tool_confirm` — sandbox wants permission

```json
{
  "type": "tool_confirm",
  "tool_confirm_json": "{\"tool\":\"exec_command\",\"args\":{\"cmd\":\"rm -rf /\"},\"reason\":\"…\"}",
  "seq": 21
}
```

Frontend: render a confirm modal, post the answer to
`POST /api/v1/sessions/:id/confirm`. The agent loop blocks on this
reply.

---

## 4. `seq` and `trace_id`

- `seq` is a per-stream monotonic counter starting at 1 (or 0 for
  the very first event). It's surfaced in two places: the JSON
  `seq` field and the SSE `id:` line. Both must agree.
- `trace_id` is the P3-3 end-to-end correlation id
  (e.g. `T-9f3c4a2b`). It is minted server-side from the request
  context, appears on every chunk, and is also mirrored in the
  response's `X-Trace-Id` header so `curl` users can grep the
  server log with the same id.

Clients should treat `seq` strictly as a debug aid, not as a
substitute for content-based ordering — the agent's forwarder
(`internal/agent/agent.go::sendOrDrop`) drops chunks only when the
caller's request context is cancelled, so under normal operation
`seq` is dense.

---

## 5. Error kinds (`error_kind`)

| Value | When |
|---|---|
| `auth_error` | Upstream returned 401/403 |
| `rate_limit` | Upstream returned 429 |
| `context_overflow` | Input exceeded model's context window |
| `vision_unsupported` | Image attachment sent to a non-vision model |
| `timeout` | Upstream didn't respond within the per-call deadline |
| `upstream` | Generic 5xx |
| `parse` | LLM stream produced un-parseable chunks |
| `protocol` | Wire-level mismatch (e.g. Anthropic tool_use_id missing) |
| `panic` | Recovered from an internal panic; the message includes the stack |

---

## 6. Multi-line `data:` payloads

A single SSE `data:` line is permitted to span physical lines by
repeating the `data:` prefix; the parser must concatenate. The
server only emits single-line JSON (no newlines inside the JSON
value), but the parser must still tolerate the multi-line form per
the SSE spec — see `frontend/src/api/sse.ts::parseSSEFrame` for the
implementation we use.

---

## 7. Disconnection recovery (P0-1)

If the SSE stream dies mid-flight (server crash, network blip,
reverse-proxy timeout), the client receives a `reader.read()` error.
The frontend `onStreamDrop` callback receives the last `seq` it
saw and the error message, and can then call
`GET /api/v1/sessions/:id/snapshot?after_seq=<lastSeq>` to fetch
any persisted parts that didn't reach the wire. The endpoint
returns the same `MessageResponse` shape used by
`GET /api/v1/sessions/:id/messages`, with a `seq` field on each
message so the client can stitch the missing tail onto the
in-memory bubble.

User abort (the user clicked Stop) sets `signal.aborted` before the
request is torn down, so the `onStreamDrop` callback skips the
recovery path on intentional cancels.

---

## 8. Quick curl example

```bash
curl -N -X POST http://127.0.0.1:14712/api/v1/sessions/$SID/messages \
  -H 'Content-Type: application/json' \
  -d '{"content":"hello","trace_id":"T-curl-demo"}' \
  | head -50
```

The `-N` (no-buffer) flag is required to see the stream in real
time. Pipe through `head -50` to stop after a few events; the
`X-Trace-Id` response header is available even if you cut the
stream short.
