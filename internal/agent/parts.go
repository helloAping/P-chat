// Package agent — message-part types + streaming accumulator.
//
// The assistant message is rendered to the user as a flat list of
// "parts" (text + thinking + tool calls + sub-agents) in stream
// order. The client (frontend/src/) mirrors this in
// src/api/client.ts:MessagePart, and the server's wire type
// (internal/server/handler.go:StreamEvent) flattens the per-part
// events back out for streaming.
//
// When the assistant message is persisted to the conversation
// history we encode the same parts list as a JSON blob in
// messages.metadata, under the key "parts". The
// GET /sessions/:id/messages handler decodes it back and includes
// it in the response, so when the user reopens a session the
// thinking blocks, tool calls, and sub-agent cards all come back
// — they don't just disappear into a plain `content` text.
package agent

// partsAccumulator 维护当前轮次助手消息的结构化 parts 列表。
//
// 支持四种 part 类型：
//   - text      — 文本增量（由 appendTextPart 追加）
//   - thinking  — 思考增量（由 appendThinkingPart 追加，含 streaming flag）
//   - tool      — 工具调用卡片（start/ok/err 状态，由 update 驱动）
//   - sub_agent — 嵌套子代理卡片（start/ok/err 状态，含内嵌 Parts）
//
// 并发安全：自有 mutex，主 LLM 流循环和 per-tool forwarder 同时写入。
//
// 持久化：snapshotStructural() 完整序列化 parts 数组（包含 text + thinking +
//   tool + sub_agent）→ meta["parts"] JSON。消息重载时 decodePartsFromMeta
//   自动识别新旧格式：新格式直接反序列化还原完整顺序，旧格式从 content /
//   meta["thinking"] 单独字段重组（向后兼容）。
//
// 修改指南 → docs/modules/agent.md

import (
	"encoding/json"
	"strings"
	"sync"
)

// MessagePart mirrors the client-side MessagePart union in
// src/api/client.ts. Kind is one of:
//
//   - "text"      — assistant prose (Text)
//   - "thinking"  — model reasoning / chain-of-thought (Text + Streaming)
//   - "tool"      — a single tool call (Name, Args, Status, Result, ...)
//   - "sub_agent" — a nested sub-agent run (Task, Status, Parts)
//
// The JSON tags are the wire format. Anything not appropriate for
// a given Kind is left at the zero value and is dropped by
// `omitempty` so e.g. a "text" part never carries a stray
// "elapsed" field.
type MessagePart struct {
	Kind     string        `json:"kind"`
	Text     string        `json:"text,omitempty"`
	Streaming bool         `json:"streaming,omitempty"`
	Name     string        `json:"name,omitempty"`
	Args     string        `json:"args,omitempty"`
	Status   string        `json:"status,omitempty"`
	Result   string        `json:"result,omitempty"`
	Error    string        `json:"error,omitempty"`
	Elapsed  string        `json:"elapsed,omitempty"`
	Task     string        `json:"task,omitempty"`
	Parts    []MessagePart `json:"parts,omitempty"`
	ToolID   string        `json:"tool_id,omitempty"`
	// AgentType is the sub-agent's registered name
	// ("explore", "plan", "general-purpose", or a custom
	// agent from .p-chat/agent/*.md). Set on sub_agent
	// parts so the card can render the agent label after a
	// session reload.
	AgentType string `json:"agent_type,omitempty"`
	// AgentColor is the sub-agent's accent color. Same
	// rationale as AgentType.
	AgentColor string `json:"agent_color,omitempty"`
	// AgentModel is the model the sub-agent used. Surfaced
	// in the card header.
	AgentModel string `json:"agent_model,omitempty"`
	// TaskID is the resume-by-id key (Args.task_id).
	// Surfaced in the card footer as a monospace badge.
	TaskID string `json:"task_id,omitempty"`
	// AgentDescription is the one-line "when to use" hint
	// from the agent's registry entry. Surfaced as a
	// hover tooltip on the agent-name badge in the card
	// header so the user can read the full hint without
	// expanding the body.
	AgentDescription string `json:"agent_description,omitempty"`
}

// Part kind numeric constants for dispatch.
const (
	PartKindText      = 0
	PartKindThinking  = 1
	PartKindTool      = 2
	PartKindSubAgent  = 3
	PartKindQuestion  = 4
)

// PartKindMap maps numeric part kind to its string representation.
var PartKindMap = map[int]string{
	PartKindText:     "text",
	PartKindThinking: "thinking",
	PartKindTool:     "tool",
	PartKindSubAgent: "sub_agent",
	PartKindQuestion: "question",
}

// PartKindStr maps string part kind to its numeric representation.
var PartKindStr = map[string]int{
	"text":      PartKindText,
	"thinking":  PartKindThinking,
	"tool":      PartKindTool,
	"sub_agent": PartKindSubAgent,
	"question":  PartKindQuestion,
}

// nativeToolCall is the parsed form of a tool call, whether it
// arrived as a native LLM tool_call delta or was extracted from a
// markdown tool_call fence in the content. The agent uses this to
// dispatch tool execution and to build the assistant message's
// persistence metadata.
type nativeToolCall struct {
	ID       string
	Name     string
	ArgsJSON string
}

// partsAccumulator mutates a `[]MessagePart` in place as
// ChatStreamChunk events arrive. It's safe to call from multiple
// goroutines (the LLM-stream reader in the main loop, plus the
// per-tool-call forwarders that re-emit sub-agent events).
//
// The accumulator is deliberately lossy on metadata it can't
// reproduce: chunk-level phase / token counts / round numbers
// are discarded; only the parts that the user sees in the chat
// bubble are kept. A part that's never finished (status="start"
// for a tool, status="start" for a sub-agent) is kept as-is;
// the on-load UI just shows it without the "ok" / elapsed fields.
type partsAccumulator struct {
	mu sync.Mutex
	// parts is the top-level part list. Sub-agent parts carry
	// their own nested parts inside sub.Parts; we never recurse
	// at the accumulator level.
	parts []MessagePart
	// activeSub is the index in `parts` of the most-recently
	// opened sub-agent card (status == "start"). Cleared when
	// the matching sub_agent_ok / sub_agent_err arrives. Only
	// one level deep — sub-agents cannot spawn sub-agents in
	// practice.
	activeSub int
	activeSet bool
}

func newPartsAccumulator() *partsAccumulator {
	return &partsAccumulator{activeSub: -1}
}

// lastIndexOfKind returns the index of the trailing part of the
// given kind in `parts`, or -1 if none. Used to grow the
// streaming text / thinking parts in place rather than pushing
// a new part for every delta.
func lastIndexOfKind(parts []MessagePart, kind string) int {
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].Kind == kind {
			return i
		}
	}
	return -1
}

// activeSubParts returns the inner parts list of the active
// sub-agent, or the top-level parts if no sub-agent is active.
func (a *partsAccumulator) activeParts() []MessagePart {
	if a.activeSet {
		return a.parts[a.activeSub].Parts
	}
	return a.parts
}

// setActiveParts writes the (possibly new) inner parts list back
// to the active sub-agent, or to the top-level slice otherwise.
func (a *partsAccumulator) setActiveParts(p []MessagePart) {
	if a.activeSet {
		a.parts[a.activeSub].Parts = p
	} else {
		a.parts = p
	}
}

// update applies one chunk to the accumulator. The chunk's role
// is identified by the same fields chunkToEvent uses on the
// server: content, thinking, tool_*, sub_agent_*.
func (a *partsAccumulator) update(c ChatStreamChunk) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Sub-agent lifecycle: open / close the nested card BEFORE
	// any nested events get routed. (Sub-agent events arrive
	// with SubAgent=true; the SubAgentStatus is the gate.)
	if c.SubAgent && c.SubAgentStatus != "" {
		if c.SubAgentStatus == "start" {
			// Push a new sub_agent part and mark it as the
			// active sink for nested events. A sub-agent's
			// first chunk is the "start" lifecycle event, so
			// this also ensures the part exists for any
			// nested content / thinking that follows.
			sub := MessagePart{
				Kind:              "sub_agent",
				Task:              c.SubAgentTask,
				Status:            "start",
				AgentType:         c.SubAgentType,
				AgentColor:        c.SubAgentColor,
				AgentModel:        c.SubAgentModel,
				AgentDescription:  c.SubAgentDescription,
				TaskID:            c.SubAgentTaskID,
				Parts:             nil,
			}
			a.parts = append(a.parts, sub)
			a.activeSub = len(a.parts) - 1
			a.activeSet = true
			return
		}
		// ok / err — find the matching sub-agent part. We
		// match by Task + Status=="start" so concurrent
		// sub-agents (theoretically) don't trip each other.
		for i := len(a.parts) - 1; i >= 0; i-- {
			if a.parts[i].Kind != "sub_agent" || a.parts[i].Task != c.SubAgentTask || a.parts[i].Status != "start" {
				continue
			}
			a.parts[i].Status = c.SubAgentStatus
			if c.Duration != "" {
				a.parts[i].Elapsed = c.Duration
			}
			// Backfill any metadata that arrived on
			// the close event (e.g. the model name
			// may only be known after the first
			// chunk).
			if c.SubAgentModel != "" && a.parts[i].AgentModel == "" {
				a.parts[i].AgentModel = c.SubAgentModel
			}
			if c.SubAgentColor != "" && a.parts[i].AgentColor == "" {
				a.parts[i].AgentColor = c.SubAgentColor
			}
			// ★ 清除嵌套 thinking parts 的 streaming flag。
			// 子代理的 Done chunk 不再转发到 partsAcc（subagent.go
			// 拦截了它），所以 Done 分支无法为子代理清理 streaming。
			// 关闭事件是唯一可用的清理钩子——若此处不清，thinking
			// 块在持久化时会带 streaming=true，并在会话重载后显示
			// 永久闪烁效果。
			// Clear streaming flags on nested thinking
			// parts so they don't persist as "still
			// streaming" across session reloads. The
			// Done chunk (which normally handles this)
			// is NOT forwarded for sub-agents — the
			// close event is the only cleanup hook.
			for j := range a.parts[i].Parts {
				if a.parts[i].Parts[j].Kind == "thinking" && a.parts[i].Parts[j].Streaming {
					a.parts[i].Parts[j].Streaming = false
				}
			}
			a.activeSet = false
			a.activeSub = -1
			return
		}
		// Unknown task — drop.
		return
	}

	// Mid-stream metadata backfill: while a sub-agent is
	// running, the parent may emit a content / thinking /
	// tool chunk with SubAgent=true and an updated
	// SubAgentModel (e.g. the LLM resolved the model name
	// after the first round). Walk the trailing sub_agent
	// part with matching task and stamp the new field.
	// Note: we index a.parts[i] directly (not a local copy)
	// so the mutation lands on the persisted struct.
	if c.SubAgent && c.SubAgentTask != "" {
		for i := len(a.parts) - 1; i >= 0; i-- {
			if a.parts[i].Kind != "sub_agent" || a.parts[i].Task != c.SubAgentTask {
				continue
			}
			if c.SubAgentModel != "" && a.parts[i].AgentModel == "" {
				a.parts[i].AgentModel = c.SubAgentModel
			}
			if c.SubAgentColor != "" && a.parts[i].AgentColor == "" {
				a.parts[i].AgentColor = c.SubAgentColor
			}
			break
		}
	}

	// Tool call: start / ok / warn / error.
	if c.ToolName != "" {
		parts := a.activeParts()
		status := toolStatusFromStep(c.Step, c.ToolError)
		if status == "start" {
			// When a ToolID is present, avoid clobbering an
			// earlier same-name start part — the ID is the
			// unique key. For ID-less streams (old clients),
			// fall back to the legacy name-based check.
			if c.ToolID != "" {
				if i := len(parts) - 1; i >= 0 &&
					parts[i].Kind == "tool" &&
					parts[i].ToolID == c.ToolID &&
					parts[i].Status == "start" {
					if c.ToolArgs != "" {
						parts[i].Args = c.ToolArgs
					}
					a.setActiveParts(parts)
					return
				}
			} else if i := len(parts) - 1; i >= 0 &&
				parts[i].Kind == "tool" &&
				parts[i].Name == c.ToolName &&
				parts[i].Status == "start" {
				if c.ToolArgs != "" {
					parts[i].Args = c.ToolArgs
				}
				a.setActiveParts(parts)
				return
			}
			parts = append(parts, MessagePart{
				Kind:   "tool",
				Name:   c.ToolName,
				Args:   c.ToolArgs,
				Status: "start",
				ToolID: c.ToolID,
			})
			a.setActiveParts(parts)
			return
		}
		// ok / warn / error — exact match by ID, fallback to
		// name for legacy streams.
		for i := len(parts) - 1; i >= 0; i-- {
			p := parts[i]
			if p.Kind != "tool" || p.Status != "start" {
				continue
			}
			if c.ToolID != "" && p.ToolID == c.ToolID ||
				c.ToolID == "" && p.Name == c.ToolName {
				p.Status = status
				p.Result = c.ToolResult
				p.Error = c.ToolError
				p.Elapsed = c.ToolElapsed
				if c.ToolArgs != "" {
					p.Args = c.ToolArgs
				}
				parts[i] = p
				a.setActiveParts(parts)
				return
			}
		}
		// No matching start — just append a completed tool
		// part (defensive: a "ok" with no preceding "start"
		// can happen if the stream is reset between calls).
		parts = append(parts, MessagePart{
			Kind:    "tool",
			Name:    c.ToolName,
			Args:    c.ToolArgs,
			Status:  status,
			Result:  c.ToolResult,
			Error:   c.ToolError,
			Elapsed: c.ToolElapsed,
			ToolID:  c.ToolID,
		})
		a.setActiveParts(parts)
		return
	}

	// Thinking delta: append to the trailing thinking part of
	// the active (sub-agent or top-level) parts list.
	if c.Thinking != "" {
		parts := a.activeParts()
		if i := lastIndexOfKind(parts, "thinking"); i >= 0 {
			parts[i].Text += c.Thinking
			parts[i].Streaming = true
		} else {
			parts = append(parts, MessagePart{
				Kind:      "thinking",
				Text:      c.Thinking,
				Streaming: true,
			})
		}
		a.setActiveParts(parts)
		return
	}

	// Content delta: append to the trailing text part of the
	// active (sub-agent or top-level) parts list.
	if c.Content != "" {
		parts := a.activeParts()
		if i := lastIndexOfKind(parts, "text"); i >= 0 {
			parts[i].Text += c.Content
		} else {
			parts = append(parts, MessagePart{
				Kind: "text",
				Text: c.Content,
			})
		}
		a.setActiveParts(parts)
		return
	}

	// Final "done" event: clear the streaming flag on any open
	// thinking parts (the assistant is done reasoning).
	if c.Done {
		for i := range a.parts {
			if a.parts[i].Kind == "thinking" && a.parts[i].Streaming {
				a.parts[i].Streaming = false
			}
			for j := range a.parts[i].Parts {
				if a.parts[i].Parts[j].Kind == "thinking" && a.parts[i].Parts[j].Streaming {
					a.parts[i].Parts[j].Streaming = false
				}
			}
		}
	}
}

// toolStatusFromStep mirrors the switch in server/handler.go:
// chunkToEvent so the accumulator and the wire format agree on
// the status string for a given step. Keep the two in lockstep
// if you ever touch this.
func toolStatusFromStep(step, errMsg string) string {
	switch {
	case errMsg != "":
		return "error"
	case strings.Contains(step, "ok"):
		return "ok"
	case strings.Contains(step, "warn"):
		return "warn"
	case strings.Contains(step, "err"):
		return "error"
	default:
		return "start"
	}
}

// snapshot returns a deep-enough copy of the parts for
// JSON-encoding. We do a full Marshal/Unmarshal round-trip so
// the caller gets a self-contained slice (no aliasing into the
// accumulator's internal buffers).
func (a *partsAccumulator) snapshot() []MessagePart {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.parts) == 0 {
		return nil
	}
	b, _ := json.Marshal(a.parts)
	var out []MessagePart
	_ = json.Unmarshal(b, &out)
	return out
}

// snapshotStructural returns a deep copy of only the
// structural parts (tool + sub_agent), dropping top-level
// text and thinking. Sub-agent nested parts (including their
// inner text and thinking) are preserved.
// snapshotStructural returns a deep copy of the accumulator's parts
// array — including text and thinking parts in their original stream
// order. The caller (persistAssistant) stores it as JSON in
// messages.metadata under the "parts" key.
//
// On reload, decodePartsFromMeta detects the format: when the JSON
// contains text or thinking parts it's used as-is (preserving the
// interleaved order the user saw during streaming); when it contains
// only tool / sub_agent parts, the legacy rebuild path kicks in
// (thinking from meta["thinking"], text from the content column).
func snapshotStructural(a *partsAccumulator) []MessagePart {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.parts) == 0 {
		return nil
	}
	b, _ := json.Marshal(a.parts)
	var out []MessagePart
	_ = json.Unmarshal(b, &out)
	return out
}
