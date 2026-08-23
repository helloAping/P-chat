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

const (
	// visionImageTokenEstimate is a bounded placeholder for one image
	// part in a multimodal request. Image rows store base64 locally, but
	// providers charge/count the visual input as image tokens, not as the
	// literal base64 text. Counting base64 here turns a normal uploaded
	// screenshot/photo into ~100K+ fake text tokens and triggers history
	// compaction before the first real model call.
	visionImageTokenEstimate = 1024
	mediaMarkerTokenEstimate = 64
)

// EstimateMessageTokens returns a rough token estimate for one
// ChatMessage, including protocol-relevant metadata and wrapper overhead.
// Binary media payloads are intentionally bounded: their Content field is
// local base64 transport data, not prompt text.
func EstimateMessageTokens(m ChatMessage) int {
	contentTokens := EstimateTokens(m.Content)
	switch m.Type {
	case TypeImage:
		contentTokens = visionImageTokenEstimate
	case TypeAudio, TypeVideo:
		contentTokens = mediaMarkerTokenEstimate
	case TypeFile:
		if m.MimeType != "" {
			contentTokens = mediaMarkerTokenEstimate
		}
	}
	return contentTokens +
		EstimateTokens(m.ToolInput) +
		EstimateTokens(m.ToolName) +
		EstimateTokens(m.ToolID) +
		EstimateTokens(m.Name) +
		EstimateTokens(m.MimeType) +
		12 // role/type/protocol wrapper overhead
}

// EstimateTokensMessages returns a rough token estimate for a slice of
// ChatMessage. It counts the primary content plus protocol-relevant
// metadata such as tool arguments, attachment names, and MIME types.
func EstimateTokensMessages(msgs []ChatMessage) int {
	total := 0
	for _, m := range msgs {
		total += EstimateMessageTokens(m)
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
