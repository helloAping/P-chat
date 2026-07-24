// merge.go implements KB-01 cross-base result merge + re-rank.
//
// Each knowledge base runs its own FTS/ranked search and produces
// scores that are only comparable *within* that base (different
// corpora, different score scales). MergeAndRerank:
//
//  1. Tags each item with its base (if missing).
//  2. Normalises ranks per-base to [0, 1] (max-normalise).
//  3. Dedupes identical fragments across bases (same source+title).
//  4. Sorts by normalised score descending and truncates to topK.
package knowledge

import (
	"path/filepath"
	"sort"
	"strings"
)

// MergeOptions controls MergeAndRerank behaviour.
type MergeOptions struct {
	// TopK is the maximum number of items to return. <=0 → 20.
	TopK int
	// BaseWeights optionally boosts/penalises a base after
	// normalisation (1.0 = neutral). Missing keys default to 1.0.
	BaseWeights map[string]float64
}

// MergeAndRerank combines per-base search hits into a single ranked
// list. Items should already carry Rank from LookupSearch; Base
// should be set by the caller (or will be left empty).
//
// The function does not mutate the input slice elements in place
// beyond copying them into a new result; callers may reuse inputs.
func MergeAndRerank(items []IndexSearchItem, opt MergeOptions) []IndexSearchItem {
	if len(items) == 0 {
		return nil
	}
	topK := opt.TopK
	if topK <= 0 {
		topK = 20
	}

	// 1) Per-base max rank for normalisation.
	maxByBase := map[string]float64{}
	for _, it := range items {
		b := it.Base
		if it.Rank > maxByBase[b] {
			maxByBase[b] = it.Rank
		}
	}

	// 2) Build scored copies.
	type scored struct {
		item  IndexSearchItem
		score float64
	}
	scoredItems := make([]scored, 0, len(items))
	for _, it := range items {
		norm := it.Rank
		if max := maxByBase[it.Base]; max > 0 {
			norm = it.Rank / max
		} else if it.Rank <= 0 {
			// Unscored / browse hits: keep a tiny floor so they
			// still appear after real matches.
			norm = 0
		}
		w := 1.0
		if opt.BaseWeights != nil {
			if bw, ok := opt.BaseWeights[it.Base]; ok && bw > 0 {
				w = bw
			}
		}
		score := norm * w
		cp := it
		cp.Rank = score // replace with normalised global score
		scoredItems = append(scoredItems, scored{item: cp, score: score})
	}

	// 3) Sort by score desc (stable for equal scores → preserve base order).
	sort.SliceStable(scoredItems, func(i, j int) bool {
		return scoredItems[i].score > scoredItems[j].score
	})

	// 4) Dedupe by (normalised source + title). Prefer higher score
	// (already sorted). Across bases the same file path may appear
	// with different path separators / case on Windows.
	seen := make(map[string]bool, len(scoredItems))
	out := make([]IndexSearchItem, 0, topK)
	for _, s := range scoredItems {
		key := dedupeKey(s.item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s.item)
		if len(out) >= topK {
			break
		}
	}
	return out
}

// dedupeKey collapses path separators / case so the same fragment
// from two bases (or the same base twice) is recognised.
func dedupeKey(it IndexSearchItem) string {
	src := strings.ToLower(filepath.ToSlash(strings.TrimSpace(it.Source)))
	title := strings.ToLower(strings.TrimSpace(it.Title))
	// Prefer source+title; fall back to title only when source empty.
	if src == "" && title == "" {
		// Last resort: id scoped by base so we don't collapse
		// unrelated empty rows from different bases.
		return it.Base + "#" + itoa(it.ID)
	}
	if src == "" {
		return "t:" + title
	}
	return "s:" + src + "|t:" + title
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// TagBase sets Base on every item that lacks one. Convenience for
// callers that searched a single store and forgot to stamp the name.
func TagBase(items []IndexSearchItem, base string) []IndexSearchItem {
	if base == "" || len(items) == 0 {
		return items
	}
	out := make([]IndexSearchItem, len(items))
	copy(out, items)
	for i := range out {
		if out[i].Base == "" {
			out[i].Base = base
		}
	}
	return out
}
