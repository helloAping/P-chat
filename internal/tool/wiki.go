package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
)

type wikiLookupArgs struct {
	Title string `json:"title"`
	TopK  int    `json:"top_k,omitempty"`
	Base  string `json:"base,omitempty"` // base name, "__all__", or "" = current session base
}

type wikiIndexArgs struct {
	Source string `json:"source,omitempty"`
	Base   string `json:"base,omitempty"`
}

// RegisterWiki registers wiki_lookup and wiki_index tools.
func RegisterWiki(r *Registry, cfg *config.Config) {
	r.Register(Tool{
		Name: "wiki_lookup",
		Description: "从知识库中按标题检索结构化条目。base 参数指定知识库名称（留空=当前知识库，__all__=全部）。输入近似标题即可，支持模糊匹配。",
		Parameters: ObjectSchema(map[string]any{
			"title": StringProp("要检索的条目标题（可近似，支持模糊匹配）"),
			"top_k": map[string]any{
				"type":        "integer",
				"description": "返回结果数，默认 5",
				"minimum":     1,
				"maximum":     10,
			},
			"base": StringProp("知识库名称（可选，留空使用当前会话选定的知识库，__all__ 搜索全部）"),
		}, []string{"title"}),
	}, makeWikiLookupHandler(cfg))

	r.Register(Tool{
		Name:        "wiki_index",
		Description: "列出知识库的全部条目目录，或指定文件的章节列表。base 参数指定知识库名称。",
		Parameters: ObjectSchema(map[string]any{
			"source": StringProp("可选，指定文件名（如 AGENTS.md）只列出该文件的章节，留空列出全部"),
			"base":   StringProp("知识库名称（可选，留空使用当前会话选定的知识库）"),
		}, nil),
	}, makeWikiIndexHandler(cfg))
}

func makeWikiLookupHandler(cfg *config.Config) ToolHandler {
	return func(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
		var a wikiLookupArgs
		if err := json.Unmarshal(argsRaw, &a); err != nil {
			return &CallResult{Content: "参数错误: " + err.Error(), IsError: true}, nil
		}
		if a.Title == "" {
			return &CallResult{Content: "title 不能为空", IsError: true}, nil
		}
		if a.TopK <= 0 {
			a.TopK = 5
		}

		kc := cfg.Knowledge
		if !kc.Enabled {
			return &CallResult{Content: "知识库未启用", IsError: true}, nil
		}

		basesToSearch := resolveBases(kc, a.Base)
		if len(basesToSearch) == 0 {
			return &CallResult{Content: "知识库未配置或不可用", IsError: true}, nil
		}

		var all []knowledge.WikiSection
		for _, base := range basesToSearch {
			if !base.Enabled {
				continue
			}
			store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
			if err != nil {
				continue
			}
			results, err := store.SearchFTS(ctx, a.Title, a.TopK)
			if err != nil {
				continue
			}
			all = append(all, results...)
			if len(all) >= a.TopK {
				break
			}
		}
		if len(all) > a.TopK {
			all = all[:a.TopK]
		}

		if len(all) == 0 {
			return &CallResult{Content: fmt.Sprintf("(未找到与 \"%s\" 相关的条目)", a.Title)}, nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "## wiki_lookup: \"%s\" (%d results)\n\n", a.Title, len(all))
		for _, r := range all {
			content := r.Content
			if len(content) > 800 {
				content = content[:800] + "\n...(truncated)"
			}
			fmt.Fprintf(&b, "### %s\n*Source: %s*\n\n%s\n\n", r.Title, r.Source, content)
		}
		return &CallResult{Content: b.String()}, nil
	}
}

func makeWikiIndexHandler(cfg *config.Config) ToolHandler {
	return func(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
		var a wikiIndexArgs
		if argsRaw != nil {
			if err := json.Unmarshal(argsRaw, &a); err != nil {
				return &CallResult{Content: "参数错误: " + err.Error(), IsError: true}, nil
			}
		}
		kc := cfg.Knowledge
		if !kc.Enabled {
			return &CallResult{Content: "知识库未启用", IsError: true}, nil
		}

		basesToSearch := resolveBases(kc, a.Base)
		if len(basesToSearch) == 0 {
			return &CallResult{Content: "知识库未配置或不可用", IsError: true}, nil
		}

		var all []knowledge.WikiSection
		for _, base := range basesToSearch {
			if !base.Enabled {
				continue
			}
			store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
			if err != nil {
				continue
			}
			sections, err := store.ListBase(ctx, "")
			if err != nil {
				continue
			}
			all = append(all, sections...)
		}

		if a.Source != "" {
			var filtered []knowledge.WikiSection
			for _, s := range all {
				if strings.Contains(s.Source, a.Source) {
					filtered = append(filtered, s)
				}
			}
			all = filtered
		}

		if len(all) == 0 {
			return &CallResult{Content: "(知识库为空，尚未扫描)"}, nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "## Wiki Index (%d sections)\n\n", len(all))
		currentSource := ""
		for _, s := range all {
			if s.Source != currentSource {
				fmt.Fprintf(&b, "### %s\n", s.Source)
				currentSource = s.Source
			}
			indent := ""
			if s.Heading != "" {
				indent = "··" // indent sub-items
			}
			kw := extractKeywords(s.Content)
			line := fmt.Sprintf("%s- %s", indent, s.Title)
			if kw != "" {
				line += fmt.Sprintf(" ← %s", kw)
			}
			fmt.Fprintf(&b, "  %s\n", line)

			// Show sub-items under their parent if they share the same source.
			for _, sub := range all {
				if sub.Heading == s.Title && sub.Source == s.Source {
					subKw := extractKeywords(sub.Content)
					subLine := fmt.Sprintf("    - %s", sub.Title)
					if subKw != "" {
						subLine += fmt.Sprintf(" ← %s", subKw)
					}
					fmt.Fprintf(&b, "  %s\n", subLine)
				}
			}
		}
		return &CallResult{Content: b.String()}, nil
	}
}

// extractKeywords parses the "关键词：" line from a formatted index entry.
func extractKeywords(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "关键词：") || strings.HasPrefix(line, "关键词:") {
			return strings.TrimPrefix(strings.TrimPrefix(line, "关键词："), "关键词:")
		}
	}
	return ""
}

// resolveBases resolves a base name to KnowledgeBase entries.
// "" = all enabled bases, "__all__" = all enabled, name = single match.
func resolveBases(kc config.KnowledgeConfig, name string) []config.KnowledgeBase {
	if name == "" || name == "__all__" {
		var out []config.KnowledgeBase
		for _, b := range kc.Bases {
			if b.Enabled {
				out = append(out, b)
			}
		}
		return out
	}
	for _, b := range kc.Bases {
		if b.Name == name && b.Enabled {
			return []config.KnowledgeBase{b}
		}
	}
	return nil
}
