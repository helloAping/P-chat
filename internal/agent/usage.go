package agent

import "github.com/p-chat/pchat/internal/llm"

// requestUsage 只对同一请求内的累计 usage 去重，跨请求按增量求和。
// requestUsage deduplicates cumulative usage within a request and returns deltas for totals.
type requestUsage struct {
	input  int
	output int
	// cacheHit/cacheMiss track the latest absolute cache snapshot
	// for this request. Providers usually emit usage once at the
	// end; if they re-emit, we treat the numbers as absolute and
	// return the delta against the previous snapshot.
	cacheHit    int
	cacheMiss   int
	cacheKnown  bool
}

func (u *requestUsage) add(chunk llm.StreamChunk) (int, int) {
	input := max(u.input, chunk.TokensIn)
	output := max(u.output, chunk.TokensOut)
	inDelta, outDelta := input-u.input, output-u.output
	u.input, u.output = input, output
	return inDelta, outDelta
}

// addCache folds a cache snapshot into this request and returns
// the hit/miss deltas to add to turn totals. Nil / missing cache
// leaves totals unchanged.
func (u *requestUsage) addCache(c *llm.CacheUsage) (hitDelta, missDelta int, known bool) {
	if c == nil {
		return 0, 0, u.cacheKnown
	}
	hit, miss := max(0, c.HitTokens), max(0, c.MissTokens)
	if !u.cacheKnown {
		u.cacheKnown = true
		u.cacheHit, u.cacheMiss = hit, miss
		return hit, miss, true
	}
	hitDelta = hit - u.cacheHit
	missDelta = miss - u.cacheMiss
	u.cacheHit, u.cacheMiss = hit, miss
	return hitDelta, missDelta, true
}
