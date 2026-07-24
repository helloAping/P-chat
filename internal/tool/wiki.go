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
	Query  string `json:"query,omitempty"`
	Base   string `json:"base,omitempty"`
	Expand bool   `json:"expand,omitempty"`
	Level  int    `json:"level,omitempty"`
	Page   int    `json:"page,omitempty"`
	Size   int    `json:"size,omitempty"`
}

type wikiListArgs struct {
	ParentID int `json:"parent_id"`
	Page     int `json:"page,omitempty"`
	Size     int `json:"size,omitempty"`
}

// RegisterWiki registers wiki_lookup and wiki_list tools.
func RegisterWiki(r *Registry, cfg *config.Config) {
	r.Register(Tool{
		Name: "wiki_lookup",
		Description: "检索知识库，支持按关键词、标题或概览搜索，支持分页。query=空 浏览文件目录；query=关键词 搜索匹配条目；expand=true 同时返回正文。" +
			"默认每页 20 条，按关联度降序排列。",
		Parameters: ObjectSchema(map[string]any{
			"query": StringProp("搜索词（可选，留空=浏览所有文件目录；输入关键词=搜索匹配的标题、关键词或概览）"),
			"base":  StringProp("知识库名称（可选，留空或 __all__=全部）"),
			"expand": map[string]any{
				"type":        "boolean",
				"description": "是否同时返回正文内容（默认 false）",
			},
			"level": map[string]any{
				"type":        "integer",
				"description": "限定层级: 0=自动, 2=仅文件级, 3=仅章节级（默认 0）",
				"minimum":     0,
				"maximum":     3,
			},
			"page": map[string]any{
				"type":        "integer",
				"description": "页码，从 1 开始（默认 1）",
				"minimum":     1,
			},
			"size": map[string]any{
				"type":        "integer",
				"description": "每页条数（默认 20，上限 50）",
				"minimum":     1,
				"maximum":     50,
			},
		}, nil),
	}, makeWikiLookupHandler(cfg))

	r.Register(Tool{
		Name:        "wiki_list",
		Description: "列出指定节点下的子节点列表。用于浏览某个文件内的所有章节，或展开查看某个章节的内容片段。",
		Parameters: ObjectSchema(map[string]any{
			"parent_id": map[string]any{
				"type":        "integer",
				"description": "父节点 id（L1=1 列出所有文件，L2 节点的 id 列出该文件所有章节）",
			},
			"page": map[string]any{
				"type":        "integer",
				"description": "页码（默认 1）",
				"minimum":     1,
			},
			"size": map[string]any{
				"type":        "integer",
				"description": "每页条数（默认 50，上限 100）",
				"minimum":     1,
				"maximum":     100,
			},
		}, []string{"parent_id"}),
	}, makeWikiListHandler(cfg))
}

func makeWikiLookupHandler(cfg *config.Config) ToolHandler {
	return func(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
		var a wikiLookupArgs
		if err := json.Unmarshal(argsRaw, &a); err != nil {
			return &CallResult{Content: "参数错误: " + err.Error(), IsError: true}, nil
		}
		if a.Page <= 0 {
			a.Page = 1
		}
		if a.Size <= 0 || a.Size > 50 {
			a.Size = 20
		}

		kc := cfg.Knowledge
		if !kc.Enabled {
			return &CallResult{Content: "知识库未启用", IsError: true}, nil
		}

		basesToSearch := resolveBases(kc, a.Base)
		if len(basesToSearch) == 0 {
			return &CallResult{Content: "知识库未配置或不可用", IsError: true}, nil
		}

		// Collect results across all selected bases (KB-01/KB-03).
		// Each base is queried for the original query plus derived
		// sub-queries. MergeAndRerank normalises scores, dedupes,
		// and produces a global ranking before pagination.
		plan := knowledge.PlanQueries(a.Query)
		queries := plan.Queries
		if a.Query == "" {
			queries = []string{""}
		}
		var allItems []knowledge.IndexSearchItem
		rawTotal := 0
		for _, base := range basesToSearch {
			if !base.Enabled {
				continue
			}
			store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
			if err != nil {
				continue
			}
			for _, q := range queries {
				res, err := store.LookupSearch(ctx, q, base.Name, a.Expand, a.Level, 1, 200)
				if err != nil {
					continue
				}
				items := knowledge.TagBase(res.Items, base.Name)
				for i := range items {
					if items[i].Query == "" {
						items[i].Query = q
					}
				}
				allItems = append(allItems, items...)
				rawTotal += res.Total
			}
		}
		if rawTotal == 0 && len(allItems) == 0 {
			return &CallResult{Content: "(知识库为空，尚未扫描)"}, nil
		}

		// Global merge: keep a generous pool then paginate.
		// TopK = page*size so deeper pages still see globally-ranked items.
		pool := a.Page * a.Size
		if pool < a.Size {
			pool = a.Size
		}
		if pool < 50 {
			pool = 50
		}
		mergedAll := knowledge.MergeAndRerank(allItems, knowledge.MergeOptions{TopK: pool})
		total := len(mergedAll)
		if total < rawTotal {
			// Merge truncates the pool; report at least the pool size
			// so the LLM knows more may exist beyond this window.
			total = len(mergedAll)
		}

		// Paginate the merged+re-ranked results.
		start := (a.Page - 1) * a.Size
		end := start + a.Size
		if start >= len(mergedAll) {
			start = len(mergedAll)
		}
		if end > len(mergedAll) {
			end = len(mergedAll)
		}
		mergedItems := mergedAll[start:end]
		hasMore := end < len(mergedAll) || rawTotal > len(mergedAll)

		var b strings.Builder
		if a.Query == "" {
			fmt.Fprintf(&b, "## Wiki Directory (%d files, page %d)\n\n", total, a.Page)
		} else {
			fmt.Fprintf(&b, "## wiki_lookup: \"%s\" (%d results, page %d)\n", a.Query, total, a.Page)
			if len(plan.Queries) > 1 {
				fmt.Fprintf(&b, "*派生查询: %s*\n", strings.Join(plan.Queries[1:], ", "))
			}
			b.WriteString("\n")
		}
		for _, it := range mergedItems {
			if it.Parent != nil {
				fmt.Fprintf(&b, "### %s / %s\n", it.Parent.Title, it.Title)
			} else {
				fmt.Fprintf(&b, "### %s\n", it.Title)
			}
			if it.Base != "" {
				fmt.Fprintf(&b, "*来源库: %s*\n", it.Base)
			}
			citation := knowledge.BuildCitation(it)
			if it.MatchType != "" {
				fmt.Fprintf(&b, "*命中: %s*\n", it.MatchType)
			}
			if it.Query != "" && it.Query != a.Query {
				fmt.Fprintf(&b, "*匹配查询: %s*\n", it.Query)
			}
			if citation.Explanation != "" {
				fmt.Fprintf(&b, "*解释: %s*\n", citation.Explanation)
			}
			if it.Keywords != "" {
				fmt.Fprintf(&b, "*关键词: %s*\n", it.Keywords)
			}
			if it.Overview != "" {
				overview := it.Overview
				if len(overview) > 500 {
					overview = overview[:500] + "..."
				}
				fmt.Fprintf(&b, "%s\n", overview)
			}
			if it.Rank > 0 {
				fmt.Fprintf(&b, "*(relevance: %.2f)*\n", it.Rank)
			}
			if len(it.Children) > 0 {
				b.WriteString("\n")
				for _, c := range it.Children {
					content := c.Content
					if len(content) > 800 {
						content = content[:800] + "\n...(truncated)"
					}
					fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(content, "\n", "\n> "))
				}
			}
			b.WriteString("\n")
		}
		if hasMore {
			fmt.Fprintf(&b, "*(共 %d 条，继续翻页请用 page=%d)*\n", total, a.Page+1)
		}
		return &CallResult{Content: b.String()}, nil
	}
}

func makeWikiListHandler(cfg *config.Config) ToolHandler {
	return func(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
		var a wikiListArgs
		if err := json.Unmarshal(argsRaw, &a); err != nil {
			return &CallResult{Content: "参数错误: " + err.Error(), IsError: true}, nil
		}
		if a.ParentID <= 0 {
			return &CallResult{Content: "parent_id 必填", IsError: true}, nil
		}
		if a.Page <= 0 {
			a.Page = 1
		}
		if a.Size <= 0 || a.Size > 100 {
			a.Size = 50
		}

		kc := cfg.Knowledge
		if !kc.Enabled {
			return &CallResult{Content: "知识库未启用", IsError: true}, nil
		}

		// Search across all enabled bases.
		var merged *knowledge.IndexSearchResult
		for _, base := range kc.Bases {
			if !base.Enabled {
				continue
			}
			store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
			if err != nil {
				continue
			}
			res, err := store.ListChildren(ctx, a.ParentID, a.Page, a.Size)
			if err != nil {
				continue
			}
			if res.Total == 0 {
				continue
			}
			if merged == nil {
				merged = res
			} else {
				merged.Total += res.Total
				// HasMore across multiple bases: there are more
				// items to show if the union of all bases'
				// remaining items > 0. Using OR on each base's
				// HasMore was correct in the single-base case
				// but under-reports in the multi-base case
				// (e.g. base A has 30 items no more, base B has
				// 30 items with more → merged is 60 items with
				// more in B but A says no more; user wouldn't
				// paginate).
				merged.Items = append(merged.Items, res.Items...)
			}
		}
		if merged == nil || merged.Total == 0 {
			return &CallResult{Content: "(无子节点)"}, nil
		}
		// Recompute HasMore based on the merged view: there are
		// more items if the merged total exceeds what we showed.
		merged.HasMore = merged.Total > len(merged.Items)

		var b strings.Builder
		fmt.Fprintf(&b, "## Children of node %d (%d items)\n\n", a.ParentID, merged.Total)
		for _, it := range merged.Items {
			fmt.Fprintf(&b, "- **%s**", it.Title)
			if it.Keywords != "" {
				fmt.Fprintf(&b, " ← %s", it.Keywords)
			}
			if it.Overview != "" {
				overview := it.Overview
				if len(overview) > 200 {
					overview = overview[:200] + "..."
				}
				fmt.Fprintf(&b, " — %s", overview)
			}
			fmt.Fprintf(&b, " *(id=%d, source=%s)*\n", it.ID, it.Source)
		}
		if merged.HasMore {
			fmt.Fprintf(&b, "\n*(共 %d 条，继续翻页 page=%d)*\n", merged.Total, merged.Page+1)
		}
		return &CallResult{Content: b.String()}, nil
	}
}

// resolveBases resolves a base name to KnowledgeBase entries.
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
