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
	runes := utf8.RuneCountInString(s)
	cjk := 0
	ascii := 0
	for _, r := range s {
		if r <= 0x007F {
			ascii++
		} else {
			cjk++
		}
	}
	// ASCII: ~4 chars / token → 0.25 token/char
	// CJK: ~1.5 char / token → 0.67 token/char
	// Round up for safety.
	_ = runes
	return (ascii+3)/4 + (cjk*2+2)/3
}

// EstimateTokensMessages returns a rough token estimate for a slice of
// ChatMessage. It sums the token count of every message's Content plus a
// small per-message overhead.
func EstimateTokensMessages(msgs []ChatMessage) int {
	total := 0
	for _, m := range msgs {
		total += EstimateTokens(m.Content)
		total += 4 // per-message metadata overhead
	}
	return total
}

// DefaultContextWindow is used when the model's context length is unknown.
const DefaultContextWindow = 128_000

// maxOutputTokensDefault is the default max output tokens for estimation.
const maxOutputTokensDefault = 32_000

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
