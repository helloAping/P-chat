package llm

import (
	"encoding/json"
	"strings"
)

// SafeToolInputJSON returns a JSON object string safe for OpenAI
// function.arguments and Anthropic tool_use.input. It is a protocol boundary
// guard for old or malformed history rows; current agent turns normalize tool
// calls before execution.
func SafeToolInputJSON(input string) string {
	s := strings.TrimSpace(input)
	if s == "" || s == "null" {
		return "{}"
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "{}"
	}
	if _, ok := v.(map[string]any); !ok {
		return "{}"
	}
	return s
}
