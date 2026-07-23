// hybrid.go implements KB-02 hybrid retrieval.
//
// Besides FTS5 (semantic/prefix token match), we also run lexical
// strategies that excel at code-repo and exact-term queries:
//
//   - path / filename substring match on index_nodes.source
//   - title substring match on index_nodes.title
//
// Each strategy produces its own ranked list. Lists are fused with
// Reciprocal Rank Fusion (RRF) so a strong exact path hit can outrank
// a weak FTS hit, while pure semantic FTS hits still surface when no
// path/title match exists.
//
// MatchType on each result records the highest-priority strategy that
// contributed the hit (path > filename > title > keywords > overview >
// l2 > content).
package knowledge

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
)

// Match-type constants exposed on IndexSearchItem.MatchType.
const (
	MatchPath     = "path"
	MatchFilename = "filename"
	MatchTitle    = "title"
	MatchKeywords = "keywords"
	MatchOverview = "overview"
	MatchL2       = "l2"
	MatchContent  = "content"
)

// rrfK is the standard RRF constant (Cormack et al.). Larger k softens
// the top-rank advantage; 60 is the literature default.
const rrfK = 60.0

// matchPriority ranks match types so the "primary" reason on a fused
// hit prefers more precise signals.
var matchPriority = map[string]int{
	MatchPath:     70,
	MatchFilename: 60,
	MatchTitle:    50,
	MatchKeywords: 40,
	MatchOverview: 30,
	MatchL2:       20,
	MatchContent:  10,
}

// fuseRRF merges multiple ordered hit lists via Reciprocal Rank Fusion.
// Each list should already be sorted best-first. Returns a single list
// sorted by RRF score descending, with MatchType set to the highest-
// priority contributing strategy and Rank set to the RRF score.
func fuseRRF(lists [][]searchHit) []searchHit {
	type acc struct {
		item   IndexSearchItem
		score  float64
		match  string
		weight float64
	}
	byID := map[int]*acc{}

	for _, list := range lists {
		for rank, h := range list {
			// RRF: 1 / (k + rank), rank is 1-based.
			score := 1.0 / (rrfK + float64(rank+1))
			// Mild per-strategy weight so path lists still beat content.
			if h.weight > 0 {
				score *= h.weight
			}
			a, ok := byID[h.item.ID]
			if !ok {
				cp := h.item
				a = &acc{item: cp, score: score, match: h.item.MatchType, weight: h.weight}
				byID[h.item.ID] = a
				continue
			}
			a.score += score
			// Keep the more precise match type.
			if matchPriority[h.item.MatchType] > matchPriority[a.match] {
				a.match = h.item.MatchType
			}
			// Prefer richer parent/title payload if the first list was sparse.
			if a.item.Parent == nil && h.item.Parent != nil {
				a.item.Parent = h.item.Parent
			}
			if a.item.Overview == "" && h.item.Overview != "" {
				a.item.Overview = h.item.Overview
			}
			if a.item.Keywords == "" && h.item.Keywords != "" {
				a.item.Keywords = h.item.Keywords
			}
		}
	}

	out := make([]searchHit, 0, len(byID))
	for _, a := range byID {
		a.item.MatchType = a.match
		a.item.Rank = a.score
		out = append(out, searchHit{item: a.item, dedupKey: a.item.ID, weight: 1, ftsRank: a.score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ftsRank > out[j].ftsRank
	})
	return out
}

// lexicalPathHits finds nodes whose source path or filename contains
// the query (case-insensitive). Exact filename equality ranks first.
func (ws *WikiStore) lexicalPathHits(ctx context.Context, query, baseWhere, baseArg string) []searchHit {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	like := "%" + strings.ToLower(q) + "%"

	sqlStmt := `SELECT n.id, n.level, n.title, n.keywords, n.overview, n.source, n.kind,
	                   n.parent_id, p.title
	            FROM index_nodes n
	            LEFT JOIN index_nodes p ON n.parent_id = p.id
	            WHERE n.level IN (2, 3)
	              AND (LOWER(n.source) LIKE ? OR LOWER(n.title) LIKE ?)
	              ` + baseWhere + `
	            ORDER BY n.level DESC, n.id
	            LIMIT 50`
	// baseWhere already starts with AND when set; empty when all bases.
	// But baseWhere uses `n.base` — good.

	var args []any
	args = append(args, like, like)
	if baseWhere != "" {
		// baseWhere is `AND n.base = ?`
		args = append(args, baseArg)
	}

	rows, err := ws.db.QueryContext(ctx, sqlStmt, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	qlower := strings.ToLower(q)
	var out []searchHit
	for rows.Next() {
		var it IndexSearchItem
		var parentID, parentTitle string
		if err := rows.Scan(&it.ID, &it.Level, &it.Title, &it.Keywords, &it.Overview, &it.Source, &it.Kind,
			&parentID, &parentTitle); err != nil {
			continue
		}
		it.Parent = &NodeRef{}
		if pid, ok := parseStrInt(parentID); ok && pid > 0 {
			it.Parent.ID = pid
			it.Parent.Title = parentTitle
		}

		srcLower := strings.ToLower(filepath.ToSlash(it.Source))
		baseName := strings.ToLower(filepath.Base(it.Source))
		titleLower := strings.ToLower(it.Title)

		match := MatchPath
		weight := 1.0
		switch {
		case baseName == qlower || strings.TrimSuffix(baseName, filepath.Ext(baseName)) == qlower:
			match = MatchFilename
			weight = 1.3
		case strings.HasSuffix(srcLower, "/"+qlower) || strings.HasSuffix(srcLower, qlower):
			match = MatchPath
			weight = 1.2
		case strings.Contains(baseName, qlower):
			match = MatchFilename
			weight = 1.15
		case strings.Contains(srcLower, qlower):
			match = MatchPath
			weight = 1.1
		case strings.EqualFold(it.Title, q):
			match = MatchTitle
			weight = 1.25
		case strings.Contains(titleLower, qlower):
			// Title matched via the OR branch — still useful.
			match = MatchTitle
			weight = 1.05
		default:
			// Shouldn't happen given the SQL filter.
			match = MatchPath
			weight = 1.0
		}
		it.MatchType = match
		out = append(out, searchHit{item: it, dedupKey: it.ID, weight: weight, ftsRank: weight})
	}

	// Sort by weight desc so RRF rank 1 is the strongest path hit.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].weight > out[j].weight
	})
	return out
}

// collectFTSList runs one FTS strategy and returns its hit list with
// MatchType stamped. Unlike runFTSQuery it does not mutate a shared
// seen map — RRF handles cross-list dedupe.
func (ws *WikiStore) collectFTSList(ctx context.Context, query, baseWhere, baseArg string, weight float64, matchType, sqlTmpl string, ftsExpr func(string) string) []searchHit {
	seen := map[int]bool{}
	// Reuse runFTSQuery machinery with a fresh seen map, then stamp MatchType.
	hits := ws.runFTSQuery(ctx, nil, &seen, query, baseWhere, baseArg, weight, sqlTmpl, ftsExpr)
	for i := range hits {
		hits[i].item.MatchType = matchType
	}
	// FTS bm25: lower (more negative) is better. Sort ascending by ftsRank.
	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].ftsRank < hits[j].ftsRank
	})
	return hits
}

// collectContentLikeList always runs content LIKE (not only as empty
// fallback) so exact body terms still contribute to hybrid ranking.
func (ws *WikiStore) collectContentLikeList(ctx context.Context, query, baseWhere, baseArg string) []searchHit {
	seen := map[int]bool{}
	hits := ws.addContentLikeHits(ctx, nil, &seen, query, baseWhere, baseArg, weightContent)
	for i := range hits {
		hits[i].item.MatchType = MatchContent
	}
	return hits
}
