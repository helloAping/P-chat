package agent

import "github.com/p-chat/pchat/internal/llm"

// requestUsage 只对同一请求内的累计 usage 去重，跨请求按增量求和。
// requestUsage deduplicates cumulative usage within a request and returns deltas for totals.
type requestUsage struct {
	input  int
	output int
}

func (u *requestUsage) add(chunk llm.StreamChunk) (int, int) {
	input := max(u.input, chunk.TokensIn)
	output := max(u.output, chunk.TokensOut)
	inDelta, outDelta := input-u.input, output-u.output
	u.input, u.output = input, output
	return inDelta, outDelta
}
