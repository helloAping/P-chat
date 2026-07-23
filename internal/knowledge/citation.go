package knowledge

import "fmt"

// Citation explains why a knowledge result was returned.
type Citation struct {
	Base        string  `json:"base,omitempty"`
	Source      string  `json:"source,omitempty"`
	Title       string  `json:"title,omitempty"`
	ParentTitle string  `json:"parent_title,omitempty"`
	Level       int     `json:"level,omitempty"`
	Kind        string  `json:"kind,omitempty"`
	Query       string  `json:"query,omitempty"`
	MatchType   string  `json:"match_type,omitempty"`
	Score       float64 `json:"score,omitempty"`
	Explanation string  `json:"explanation,omitempty"`
}

// BuildCitation creates human-readable provenance metadata for a hit.
func BuildCitation(it IndexSearchItem) Citation {
	parent := ""
	if it.Parent != nil {
		parent = it.Parent.Title
	}
	c := Citation{
		Base:        it.Base,
		Source:      it.Source,
		Title:       it.Title,
		ParentTitle: parent,
		Level:       it.Level,
		Kind:        it.Kind,
		Query:       it.Query,
		MatchType:   it.MatchType,
		Score:       it.Rank,
	}
	c.Explanation = explainCitation(c)
	return c
}

func explainCitation(c Citation) string {
	where := c.Title
	if c.ParentTitle != "" && c.Title != "" {
		where = c.ParentTitle + " / " + c.Title
	}
	if where == "" {
		where = c.Source
	}
	if where == "" {
		where = "知识库片段"
	}
	match := matchTypeLabel(c.MatchType)
	if c.Query != "" && match != "" {
		return fmt.Sprintf("%s 命中派生查询 %q，命中类型：%s。", where, c.Query, match)
	}
	if c.Query != "" {
		return fmt.Sprintf("%s 命中查询 %q。", where, c.Query)
	}
	if match != "" {
		return fmt.Sprintf("%s 命中类型：%s。", where, match)
	}
	return fmt.Sprintf("%s 来自知识库检索结果。", where)
}

func matchTypeLabel(mt string) string {
	switch mt {
	case MatchPath:
		return "路径"
	case MatchFilename:
		return "文件名"
	case MatchTitle:
		return "标题"
	case MatchKeywords:
		return "关键词"
	case MatchOverview:
		return "概览"
	case MatchL2:
		return "文件节点"
	case MatchContent:
		return "正文"
	default:
		return mt
	}
}
