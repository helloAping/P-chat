package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/recall"
	"github.com/p-chat/pchat/internal/tool"
)

// registerRecallTool adds a `recall` tool to the registry so the LLM
// can call the recall engine itself. Lives in the main package to
// avoid an import cycle (tool → recall → memory → llm → tool).
func registerRecallTool(reg *tool.Registry, engine *recall.Engine) {
	t := tool.Tool{
		Name: "recall",
		Description: "Search the attached knowledge bases and conversation history " +
			"for chunks relevant to a query. Use this when you're not sure about a " +
			"fact, want to recall past work, or need specific information from " +
			"the user's documents. Returns the top-5 most similar chunks with " +
			"source paths and similarity scores.",
		Parameters: tool.ObjectSchema(map[string]any{
			"query": tool.StringProp("Natural-language query describing what you're looking for"),
			"top_k": tool.StringEnumProp("How many results to return. Default 5.", "1", "3", "5", "10"),
		}, []string{"query"}),
	}
	h := func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		if engine == nil {
			return &tool.CallResult{Content: "recall: engine not configured", IsError: true}, nil
		}
		var a struct {
			Query string `json:"query"`
			TopK  int    `json:"top_k"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return &tool.CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
		}
		if strings.TrimSpace(a.Query) == "" {
			return &tool.CallResult{Content: "query is required", IsError: true}, nil
		}
		if a.TopK <= 0 {
			a.TopK = 5
		}

		hits, err := engine.Search(ctx, a.Query, a.TopK)
		if err != nil {
			return &tool.CallResult{Content: fmt.Sprintf("recall error: %v", err), IsError: true}, nil
		}
		if len(hits) == 0 {
			return &tool.CallResult{Content: "(no relevant chunks found)"}, nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Found %d relevant chunks:\n", len(hits))
		for i, h := range hits {
			content := h.Content
			if len(content) > 400 {
				content = content[:400] + "..."
			}
			content = strings.Map(func(r rune) rune {
				if r == '\n' || r == '\r' {
					return ' '
				}
				return r
			}, content)
			fmt.Fprintf(&b, "[%d] %s  (sim=%.0f%%)\n    %s\n", i+1, h.Source, h.Similarity*100, content)
		}
		return &tool.CallResult{Content: b.String()}, nil
	}
	reg.Register(t, h)
}
