package agent

import (
	"github.com/p-chat/pchat/internal/llm"
	"testing"
)

func TestRequestUsageDeduplicatesWithinRequestAndSumsAcrossRequests(t *testing.T) {
	var totalIn, totalOut int
	for _, snapshots := range [][]llm.StreamChunk{
		{{TokensIn: 1000, TokensOut: 10}, {TokensIn: 1000, TokensOut: 20}, {TokensIn: 1000, TokensOut: 20}},
		{{TokensIn: 1200, TokensOut: 30}},
	} {
		var request requestUsage
		for _, chunk := range snapshots {
			in, out := request.add(chunk)
			totalIn += in
			totalOut += out
		}
	}
	if totalIn != 2200 || totalOut != 50 {
		t.Fatalf("totals=%d/%d", totalIn, totalOut)
	}
}

func TestRequestUsageAddCacheDeltas(t *testing.T) {
	var u requestUsage
	hit, miss, known := u.addCache(nil)
	if hit != 0 || miss != 0 || known {
		t.Fatalf("nil cache = %d/%d known=%v", hit, miss, known)
	}
	hit, miss, known = u.addCache(&llm.CacheUsage{HitTokens: 800, MissTokens: 200})
	if hit != 800 || miss != 200 || !known {
		t.Fatalf("first = %d/%d known=%v", hit, miss, known)
	}
	hit, miss, known = u.addCache(&llm.CacheUsage{HitTokens: 850, MissTokens: 150})
	if hit != 50 || miss != -50 || !known {
		t.Fatalf("delta = %d/%d known=%v", hit, miss, known)
	}
}
