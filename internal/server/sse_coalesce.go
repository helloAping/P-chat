package server

import (
	"time"

	"github.com/p-chat/pchat/internal/agent"
)

// PERF-01: coalesce pure content/thinking deltas before SSE marshal+flush.
// Critical events (tool / phase / done / error / question / …) flush immediately.
const (
	sseCoalesceWindow   = 20 * time.Millisecond
	sseCoalesceMaxBytes = 8 << 10
)

// isCoalescableChunk reports whether a chunk is a pure text or thinking
// delta that may be merged with the next same-kind delta.
func isCoalescableChunk(c agent.ChatStreamChunk) bool {
	if c.Done || c.Error != "" || c.QuestionJSON != "" || c.ToolConfirmJSON != "" {
		return false
	}
	if c.ToolName != "" || c.SkillName != "" || c.StreamReset {
		return false
	}
	if c.ContentRewrite != "" || c.ThinkingRewrite != "" || c.SessionStatus != "" {
		return false
	}
	// Sub-agent lifecycle close frames travel as Phase events with empty
	// content — never coalesce those. Pure deltas have empty Phase.
	if c.Phase != "" && c.Content == "" && c.Thinking == "" {
		return false
	}
	hasContent := c.Content != ""
	hasThinking := c.Thinking != ""
	// Exactly one of content/thinking — agent emits them separately.
	return hasContent != hasThinking
}

func sameCoalesceKind(a, b agent.ChatStreamChunk) bool {
	if (a.Content != "") != (b.Content != "") {
		return false
	}
	if a.SubAgent != b.SubAgent {
		return false
	}
	if a.SubAgentTask != b.SubAgentTask || a.SubAgentTaskID != b.SubAgentTaskID {
		return false
	}
	return true
}

func mergeCoalescableChunks(a, b agent.ChatStreamChunk) agent.ChatStreamChunk {
	out := a
	out.Content += b.Content
	out.Thinking += b.Thinking
	if b.TokensIn > out.TokensIn {
		out.TokensIn = b.TokensIn
	}
	if b.TokensOut > out.TokensOut {
		out.TokensOut = b.TokensOut
	}
	// Advance resume cursor to the last merged chunk.
	if b.Seq > out.Seq {
		out.Seq = b.Seq
		out.TraceID = b.TraceID
	}
	return out
}

func coalescedBytes(c agent.ChatStreamChunk) int {
	return len(c.Content) + len(c.Thinking)
}

// readCoalescedChunk pulls one logical chunk from stream, merging consecutive
// pure content or thinking deltas that arrive within sseCoalesceWindow.
// hold stores a non-mergeable lookahead chunk for the next call.
func readCoalescedChunk(stream <-chan agent.ChatStreamChunk, hold **agent.ChatStreamChunk) (agent.ChatStreamChunk, bool) {
	var chunk agent.ChatStreamChunk
	if *hold != nil {
		chunk = **hold
		*hold = nil
	} else {
		var ok bool
		chunk, ok = <-stream
		if !ok {
			return agent.ChatStreamChunk{}, false
		}
	}
	if !isCoalescableChunk(chunk) {
		return chunk, true
	}

	timer := time.NewTimer(sseCoalesceWindow)
	defer timer.Stop()
	for {
		select {
		case next, ok := <-stream:
			if !ok {
				return chunk, true
			}
			if !isCoalescableChunk(next) || !sameCoalesceKind(chunk, next) {
				c := next
				*hold = &c
				return chunk, true
			}
			merged := mergeCoalescableChunks(chunk, next)
			if coalescedBytes(merged) > sseCoalesceMaxBytes {
				// Cap frame size: keep next for the following read.
				c := next
				*hold = &c
				return chunk, true
			}
			chunk = merged
		case <-timer.C:
			return chunk, true
		}
	}
}
