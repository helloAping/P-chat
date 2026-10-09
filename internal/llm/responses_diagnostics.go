package llm

import (
	"encoding/json"
	"fmt"
)

// responsesRequestShapeSummary 只汇总请求结构，不包含正文、工具参数／结果、ID 或模型名。
// responsesRequestShapeSummary reports only structural metadata, never content, tool payloads, IDs, or model names.
func responsesRequestShapeSummary(body []byte) (string, bool) {
	var request struct {
		Input []struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			CallID string `json:"call_id"`
		} `json:"input"`
		Tools           []json.RawMessage `json:"tools"`
		Stream          *bool             `json:"stream"`
		MaxOutputTokens *int              `json:"max_output_tokens"`
		Reasoning       json.RawMessage   `json:"reasoning"`
		Temperature     json.RawMessage   `json:"temperature"`
		TopP            json.RawMessage   `json:"top_p"`
		PreviousID      json.RawMessage   `json:"previous_response_id"`
		Store           json.RawMessage   `json:"store"`
	}
	if err := json.Unmarshal(body, &request); err != nil || request.Input == nil {
		return "", false
	}
	var messages, reasoning, calls, nativeCalls, syntheticCalls, outputs, pairedOutputs, orphanOutputs int
	var missingCallIDs, missingOutputs, other int
	seenCalls := make(map[string]struct{})
	seenOutputs := make(map[string]struct{})
	for _, item := range request.Input {
		switch item.Type {
		case "message":
			messages++
		case "reasoning":
			reasoning++
		case "function_call":
			calls++
			if item.CallID == "" {
				missingCallIDs++
			} else {
				seenCalls[item.CallID] = struct{}{}
			}
			if item.ID != "" && item.ID != item.CallID {
				nativeCalls++
			} else if item.ID != "" {
				syntheticCalls++
			}
		case "function_call_output":
			outputs++
			if item.CallID == "" {
				missingCallIDs++
				orphanOutputs++
				continue
			}
			seenOutputs[item.CallID] = struct{}{}
			if _, ok := seenCalls[item.CallID]; ok {
				pairedOutputs++
			} else {
				orphanOutputs++
			}
		default:
			other++
		}
	}
	for callID := range seenCalls {
		if _, ok := seenOutputs[callID]; !ok {
			missingOutputs++
		}
	}
	stream := request.Stream != nil && *request.Stream
	maxOutputTokens := 0
	if request.MaxOutputTokens != nil {
		maxOutputTokens = *request.MaxOutputTokens
	}
	return fmt.Sprintf("items=%d messages=%d reasoning=%d calls=%d native_calls=%d synthetic_calls=%d outputs=%d paired_outputs=%d orphan_outputs=%d missing_outputs=%d missing_call_ids=%d other=%d tools=%d stream=%t max_output_tokens=%d has_reasoning=%t has_temperature=%t has_top_p=%t has_previous_response_id=%t has_store=%t body_sha256=%s",
		len(request.Input), messages, reasoning, calls, nativeCalls, syntheticCalls, outputs, pairedOutputs, orphanOutputs, missingOutputs, missingCallIDs, other, len(request.Tools), stream, maxOutputTokens,
		len(request.Reasoning) > 0, len(request.Temperature) > 0, len(request.TopP) > 0, len(request.PreviousID) > 0, len(request.Store) > 0, payloadFingerprint(body)), true
}
