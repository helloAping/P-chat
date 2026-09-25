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
