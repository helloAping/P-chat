package server

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/p-chat/pchat/internal/agent"
)

// toolStatusFromChunkStep was the server-side mirror of
// internal/agent.toolStatusFromStep. Removed in T06 — both
// the parts accumulator and the wire mapper now call
// agent.ToolStatusFromStep so the two stay in lockstep.

func chunkToEvent(chunk agent.ChatStreamChunk, provider, model string) StreamEvent {
	ev := StreamEvent{
		Phase:                 chunk.Phase,
		Step:                  chunk.Step,
		Message:               chunk.Message,
		Provider:              provider,
		Model:                 model,
		SubAgent:              chunk.SubAgent,
		SubAgentTask:          chunk.SubAgentTask,
		SubAgentStatus:        chunk.SubAgentStatus,
		SubAgentType:          chunk.SubAgentType,
		SubAgentColor:         chunk.SubAgentColor,
		SubAgentModel:         chunk.SubAgentModel,
		SubAgentTaskID:        chunk.SubAgentTaskID,
		SubAgentDescription:   chunk.SubAgentDescription,
		SubAgentFailureReason: chunk.SubAgentFailureReason,
		ThinkingRewrite:       chunk.ThinkingRewrite,
		SessionStatus:         chunk.SessionStatus,
		// Seq is the per-stream monotonic counter stamped by
		// agent.sendOrDrop. Surfaced on the wire so the
		// frontend (and curl) can debug reorder / drop
		// issues — and so the P0-1 recovery flow can use
		// it as a resume cursor. See P3-1 in
		// docs/plans/round2-stream-and-render-plan.md.
		Seq: chunk.Seq,
		// TraceID is the P3-3 end-to-end correlation id
		// (e.g. "T-9f3c4a2b") stamped on every chunk by
		// agent.sendOrDrop from the request context.
		// Surfaced on the wire so the frontend can render
		// it on error bubbles (the "复制 trace id" button
		// on the error chip) and so curl users can copy it
		// straight from the JSON payload.
		TraceID: chunk.TraceID,
		// Elapsed carries the duration the server stamped on the
		// chunk. The agent sets it on the final "done" chunk AND
		// on every sub_agent_* lifecycle close event (so the
		// SubAgentCard can show the elapsed time once the run
		// finishes). Surfacing it here unconditionally lets the
		// frontend read ev.elapsed on phase events without
		// waiting for a separate "done" tick.
		Elapsed: chunk.Duration,
	}

	// Question events are emitted by the question tool handler
	// before it blocks waiting for user input.
	if chunk.QuestionJSON != "" {
		ev.Type = "question"
		ev.QuestionJSON = chunk.QuestionJSON
		return ev
	}
	if chunk.ToolConfirmJSON != "" {
		ev.Type = "tool_confirm"
		ev.ToolConfirmJSON = chunk.ToolConfirmJSON
		return ev
	}
	if chunk.Error != "" {
		ev.Type = "error"
		ev.Error = chunk.Error
		ev.Suggestion = chunk.Suggestion
		ev.ErrorKind = chunk.ErrorKind
		return ev
	}
	if chunk.Done {
		ev.Type = "done"
		ev.TokensIn = chunk.TokensIn
		ev.TokensOut = chunk.TokensOut
		// ev.Elapsed already populated above (chunk.Duration).
		return ev
	}
	if chunk.ToolName != "" {
		ev.Type = "tool"
		ev.ToolID = chunk.ToolID
		ev.ToolName = chunk.ToolName
		ev.ToolArgs = chunk.ToolArgs
		ev.ToolResult = chunk.ToolResult
		ev.ToolResultFull = chunk.ToolResultFull
		ev.ToolError = chunk.ToolError
		ev.ToolElapsed = chunk.ToolElapsed
		// Status: parse the trailing segment of "call-N-status"
		// rather than substring-matching "ok" / "err" so a future
		// status name can't accidentally match. See
		// internal/agent/parts.go::ToolStatusFromStep for the
		// single source of truth.
		ev.ToolStatus = agent.ToolStatusFromStep(chunk.Step, chunk.ToolError)
		return ev
	}
	if chunk.Thinking != "" {
		ev.Type = "thinking"
		ev.Thinking = chunk.Thinking
		return ev
	}
	if chunk.Content != "" {
		ev.Type = "content"
		ev.Content = chunk.Content
		return ev
	}
	// ContentRewrite: the agent's post-stream redactor rewrote
	// the assistant's trailing text (e.g. stripped a phantom
	// vision error). The UI should REPLACE the trailing text
	// part with this value, not append it. We treat this as a
	// distinct event type so the chat store can route it
	// differently from regular content deltas.
	if chunk.ContentRewrite != "" {
		ev.Type = "content_rewrite"
		ev.Content = chunk.ContentRewrite
		return ev
	}
	// ThinkingRewrite: same pattern but for the LLM's
	// chain-of-thought block. Some phantoms appear in
	// thinking rather than the text response; the UI
	// replaces the trailing thinking part's text.
	if chunk.ThinkingRewrite != "" {
		ev.Type = "thinking_rewrite"
		ev.Thinking = chunk.ThinkingRewrite
		return ev
	}
	// SessionStatus events carry lifecycle signals ("busy" /
	// "idle") so the frontend can drive the TodoPanel state
	// machine. Must be checked BEFORE Phase because the chunk
	// may also carry a Phase field.
	if chunk.SessionStatus != "" {
		ev.Type = "session_status"
		return ev
	}
	// Other phase events (system, memory, plan, sub-agent
	// start/ok/err) — surface as "phase" with the original
	// Phase/Step/Message fields. Sub-agent lifecycle events
	// (sub_agent_start / sub_agent_ok / sub_agent_err) come
	// through here.
	if chunk.Phase != "" {
		ev.Type = "phase"
		return ev
	}
	// Unknown / empty event — emit as a heartbeat so the client
	// doesn't appear to hang.
	ev.Type = "phase"
	ev.Message = ""
	return ev
}

type streamDoneIDs struct {
	userMessageID int64
	lastMessageID int64
}

// streamEventFromChunk converts an agent chunk into the wire event
// and adds final message ids on done events.
func streamEventFromChunk(chunk agent.ChatStreamChunk, provider, model string, ids streamDoneIDs) StreamEvent {
	ev := chunkToEvent(chunk, provider, model)
	if chunk.Done {
		if ids.userMessageID > 0 {
			ev.UserMessageID = ids.userMessageID
		}
		if ids.lastMessageID > 0 {
			ev.LastMessageID = ids.lastMessageID
		}
	}
	return ev
}

// writeSSEFrame writes one Server-Sent Event frame.
func writeSSEFrame(w io.Writer, ev StreamEvent) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\nid: %d\n\n", data, ev.Seq)
	return err
}
