package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/p-chat/pchat/internal/tool"
)

func MakeMCPHandler(mgr *Manager, fullToolName string) tool.ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		var m map[string]any
		if err := json.Unmarshal(args, &m); err != nil {
			return &tool.CallResult{
				Content: fmt.Sprintf("invalid arguments: %v", err),
				IsError: true,
			}, nil
		}

		result, err := mgr.CallTool(ctx, fullToolName, m)
		if err != nil {
			return &tool.CallResult{
				Content: fmt.Sprintf("MCP tool error: %v", err),
				IsError: true,
			}, nil
		}

		var text string
		for _, item := range result.Content {
			if item.Type == "text" {
				text += item.Text
			}
		}
		if text == "" {
			data, _ := json.Marshal(result.Content)
			text = string(data)
		}

		return &tool.CallResult{
			Content: text,
			IsError: result.IsError,
		}, nil
	}
}
