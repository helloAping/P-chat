package llm

import (
	"encoding/json"
	"net/url"
	"strings"
)

func deepSeekReasoningReplay(model, endpoint string) bool {
	if strings.HasPrefix(strings.ToLower(model), "deepseek") {
		return true
	}
	u, err := url.Parse(endpoint)
	return err == nil && strings.EqualFold(u.Hostname(), "api.deepseek.com")
}

func messageReasoning(msg ChatMessage) string {
	if msg.Role != RoleAssistant {
		return ""
	}
	thinking, _ := msg.Meta["thinking"].(string)
	return thinking
}

// addReasoningContent 保留 SDK 尚未支持的 DeepSeek 请求字段。
// addReasoningContent preserves the DeepSeek request field missing from the SDK.
func addReasoningContent(body []byte, reasoning map[int]string) ([]byte, error) {
	if len(reasoning) == 0 {
		return body, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(root["messages"], &messages); err != nil {
		return nil, err
	}
	for i, thinking := range reasoning {
		value, err := json.Marshal(thinking)
		if err != nil {
			return nil, err
		}
		messages[i]["reasoning_content"] = value
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	root["messages"] = encoded
	return json.Marshal(root)
}
