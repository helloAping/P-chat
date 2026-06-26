// Package recall exposes a single command and a system-prompt helper
// for semantic search across indexed knowledge bases and past
// conversation history.
package recall

import (
	"context"
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/knowledge"
	"github.com/p-chat/pchat/internal/memory"
)

// Engine combines a memory store, a retriever, and an embedder to
// perform semantic search across the user's knowledge.
type Engine struct {
	Store     *memory.Store
	Retriever *knowledge.Retriever
	Embedder  knowledge.Embedder
}

func New(store *memory.Store, r *knowledge.Retriever, e knowledge.Embedder) *Engine {
	return &Engine{Store: store, Retriever: r, Embedder: e}
}

// Search runs a query and returns formatted hits.
func (e *Engine) Search(ctx context.Context, query string, topK int) ([]knowledge.SearchResult, error) {
	if e == nil || e.Retriever == nil {
		return nil, nil
	}
	return e.Retriever.Search(ctx, query, topK, "")
}

// FormatResult renders one hit as a 3-line block. Truncates long content.
func FormatResult(r knowledge.SearchResult, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 200
	}
	content := r.Content
	content = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, content)
	if len(content) > maxChars {
		content = content[:maxChars] + "..."
	}
	pct := r.Similarity * 100
	return fmt.Sprintf("[#%d] %s  (sim=%.1f%%)\n     %s", r.Rank, r.Source, pct, content)
}

// FormatForPrompt joins top hits as a single text block ready to be
// prepended to a system or user message.
func FormatForPrompt(query string, hits []knowledge.SearchResult, maxChars int) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## 召回：%s\n", query)
	fmt.Fprintf(&b, "（以下是从你的知识库/历史对话中找到的相关内容，仅供参考）\n\n")
	for _, h := range hits {
		b.WriteString(FormatResult(h, maxChars))
		b.WriteString("\n")
	}
	return b.String()
}

// PrintSearch prints hits to the terminal for a /recall command.
func (e *Engine) PrintSearch(ctx context.Context, query string, topK int) error {
	hits, err := e.Search(ctx, query, topK)
	if err != nil {
		color.Red("  ✗ 检索失败: %v", err)
		return err
	}
	if len(hits) == 0 {
		color.HiBlack("  (未找到相关记忆)")
		return nil
	}
	color.Cyan("  找到 %d 条相关记忆:", len(hits))
	for _, h := range hits {
		fmt.Println("  " + strings.ReplaceAll(FormatResult(h, 250), "\n", "\n  "))
	}
	return nil
}
