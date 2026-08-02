package llm

import "unicode/utf8"

// EstimateTokens returns a rough token-count estimate for a string.
// Chinese / CJK chars ≈ 1.5 tokens each, ASCII / Latin ≈ 0.25 tokens
// each (4 chars per token). Not exact but close enough for context-window
// budget decisions; over-estimating is safer than under-estimating.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	ascii, cjk := 0, 0
	for _, r := range s {
		if r <= 0x007F {
			ascii++
		} else {
			cjk++
		}
	}
	return (ascii+3)/4 + (cjk*2+2)/3
}

// EstimateTokensBytes is the []byte variant of EstimateTokens. It
// decodes runes in place via utf8.DecodeRune instead of converting to
// a string, so it performs zero allocations — important for tool
// parameter schemas, which are json.RawMessage bytes scanned on every
// EstimateTokensTools call.
func EstimateTokensBytes(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	ascii, cjk := 0, 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r <= 0x007F {
			ascii++
		} else {
			cjk++
		}
		b = b[size:]
	}
	return (ascii+3)/4 + (cjk*2+2)/3
}

// EstimateTokensMessages returns a rough token estimate for a slice of
// ChatMessage. It counts the primary content plus protocol-relevant
// metadata such as tool arguments, attachment names, and MIME types.
func EstimateTokensMessages(msgs []ChatMessage) int {
	total := 0
	for _, m := range msgs {
		total += EstimateTokens(m.Content)
		total += EstimateTokens(m.ToolInput)
		total += EstimateTokens(m.ToolName)
		total += EstimateTokens(m.ToolID)
		total += EstimateTokens(m.Name)
		total += EstimateTokens(m.MimeType)
		total += 12 // role/type/protocol wrapper overhead
	}
	return total
}

// EstimateTokensTools returns a rough token estimate for the tool schema
// block sent with a model request. Tool definitions are part of the prompt
// budget even though they are not ChatMessage rows.
func EstimateTokensTools(tools []ToolDef) int {
	total := 0
	for _, t := range tools {
		total += EstimateTokens(t.Name)
		total += EstimateTokens(t.Description)
		total += EstimateTokensBytes(t.Parameters)
		total += 24 // JSON schema/function wrapper overhead
	}
	return total
}

// EstimatePromptTokens returns a rough estimate of the complete prompt
// payload: messages plus the tool schema block.
func EstimatePromptTokens(msgs []ChatMessage, tools []ToolDef) int {
	return EstimateTokensMessages(msgs) + EstimateTokensTools(tools)
}

// DefaultContextWindow is used when the model's context length is unknown.
const DefaultContextWindow = 64_000

// maxOutputTokensDefault is the default max output tokens for estimation.
const maxOutputTokensDefault = 8_192

// AutoCompactBuffer is the token headroom reserved before triggering
// auto-compression. Mirrors opencode's 20k buffer.
const AutoCompactBuffer = 20_000

// UsableContext returns the usable context window for a model, minus
// reserved headroom. When contextWindow <= 0, DefaultContextWindow is used.
func UsableContext(contextWindow int) int {
	return UsableContextWithBuf(contextWindow, AutoCompactBuffer)
}

// UsableContextWithBuf is like UsableContext with a configurable buffer.
func UsableContextWithBuf(contextWindow, buffer int) int {
	if buffer <= 0 {
		buffer = AutoCompactBuffer
	}
	if contextWindow <= 0 {
		contextWindow = DefaultContextWindow
	}
	usable := contextWindow - maxOutputTokensDefault - buffer
	if usable < contextWindow/4 {
		usable = contextWindow / 4
	}
	return usable
}

// ShouldCompact returns true when the estimated total tokens exceed the
// usable context window and auto-compression should be triggered.
func ShouldCompact(totalEstimate, contextWindow int) bool {
	return ShouldCompactWithBuf(totalEstimate, contextWindow, AutoCompactBuffer)
}

// ShouldCompactWithBuf is like ShouldCompact with a configurable buffer.
func ShouldCompactWithBuf(totalEstimate, contextWindow, buffer int) bool {
	if contextWindow <= 0 {
		contextWindow = DefaultContextWindow
	}
	usable := UsableContextWithBuf(contextWindow, buffer)
	return totalEstimate > usable
}
