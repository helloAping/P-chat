package agent

// normalize.go — message/tool-call normalization. Three concerns:
//
//   1. Guard-reset bookkeeping (resetGuardCounters, resetSameToolErr)
//      so the "same tool errored twice in a row" detector doesn't
//      leak state across rounds.
//   2. tool_call_id assignment + result-pairing for the Anthropic
//      protocol which, unlike OpenAI, requires every tool result
//      to carry the matching tool_use_id (normalizeToolCallIDs,
//      needsNormalizedToolResults, normalizeToolResults).
//
// Split from agent.go in T05. Behaviour unchanged.

import (
	"github.com/google/uuid"

	"github.com/p-chat/pchat/internal/llm"
)

func resetGuardCounters(streak *int, prevSig *string, prevErrored *bool) {
	*streak = 0
	*prevSig = ""
	*prevErrored = false
}

// resetSameToolErr clears the same-tool-name error counter.
// Same rationale as resetGuardCounters: the LLM was just told
// to switch tools, give it a clean slate on this counter too.
func resetSameToolErr(name *string, count *int) {
	*name = ""
	*count = 0
}

// normalizeToolCallIDs walks a slice of tool calls in-place
// and reassigns any ID that is either empty or already used
// by an earlier call in the same slice. The replacement uses
// the "call_<uuid>" prefix that downstream parsers (tool
// handlers, the UI's tool card key, the SQLite UNIQUE column)
// already depend on, so this is a transparent fix for LLM
// streams where the upstream model either omits the ID field
// or emits duplicates. P2-3.
func normalizeToolCallIDs(toolCalls []nativeToolCall, seen map[string]bool) {
	for i := range toolCalls {
		tc := &toolCalls[i]
		if tc.ID == "" || seen[tc.ID] {
			tc.ID = "call_" + uuid.NewString()
		}
		seen[tc.ID] = true
	}
}

// needsNormalizedToolResults reports whether the named provider
// needs the legacy `normalizeToolResults` transformation before
// messages are sent to the LLM.
//
// History: an earlier version of this code applied the normalize
// transformation globally to every provider, on the theory that a
// handful of OpenAI-compatible proxies validate the
// tool_call/tool_result pairing and reject mixed rounds. The cost
// was that standard openai / anthropic LLMs lost the
// tool_call/tool_result pairing in their context, which broke the
// `question` tool flow: the LLM no longer saw its own tool_call,
// interpreted the tool result as a user message, and re-asked via
// the question tool — a loop. The bug surfaced in 2026-07-09
// against the `cs` provider (Doubao proxy → mimo-v2.5).
//
// The fix is to apply normalize only to the providers that
// actually need it. Currently no provider on the active list does,
// so this returns false for every known name; the legacy code path
// is preserved as a fall-back for the day a quirky proxy shows up.
// Add a provider name here (or a `Protocol` value via the config
// field below) to opt in.
//
// Note: this intentionally keys on the provider NAME, not the
// `Protocol` field. The protocol only tells us the wire format
// (openai / anthropic) — both support tool_call/tool_result pairs
// correctly. The "needing normalize" attribute is a per-provider
// quirk, not a protocol attribute.
func needsNormalizedToolResults(providerName string) bool {
	switch providerName {
	// Add provider names here that have been verified to need
	// the legacy flatten-tool-results treatment. Examples
	// (none currently active):
	//
	//   case "some-quirky-proxy":
	//       return true
	default:
		return false
	}
}

// normalizeToolResults removes TypeToolCall metadata rows and
// converts TypeToolResult messages to User role. This way providers
// that validate tool_call/tool_result pairing (some OpenAI-
// compatible proxies) see normal user-assistant-user conversation,
// and the LLM still sees tool results as part of the ongoing
// dialogue.
//
// WARNING: this transformation BREAKS the question tool flow on
// standard OpenAI / Anthropic models. The LLM needs the
// tool_call/tool_result pairing to recognise that the user has
// answered; flattening the result into a user message makes the
// LLM interpret the JSON as a user statement and re-ask the
// question (infinite loop). Apply only via
// needsNormalizedToolResults — never globally.
func normalizeToolResults(msgs []llm.ChatMessage) []llm.ChatMessage {
	out := make([]llm.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Type == llm.TypeToolCall {
			continue
		}
		if m.Type == llm.TypeToolResult {
			m.Role = llm.RoleUser
		}
		out = append(out, m)
	}
	return out
}

