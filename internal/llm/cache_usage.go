package llm

// CacheUsage 表示单次请求的缓存输入 token 统计。
// CacheUsage reports cached and uncached input tokens for one request.
type CacheUsage struct {
	HitTokens  int
	MissTokens int
}

func (u *openaiStreamUsage) chunk() StreamChunk {
	chunk := StreamChunk{TokensIn: u.PromptTokens, TokensOut: u.CompletionTokens}
	hit := u.CacheHitTokens
	if hit == nil && u.PromptDetails != nil {
		hit = u.PromptDetails.CachedTokens
	}
	if hit == nil && u.CacheMissTokens == nil {
		return chunk
	}
	usage := &CacheUsage{}
	if hit != nil {
		usage.HitTokens = max(0, *hit)
	} else {
		usage.HitTokens = max(0, u.PromptTokens-*u.CacheMissTokens)
	}
	if u.CacheMissTokens != nil {
		usage.MissTokens = max(0, *u.CacheMissTokens)
	} else {
		usage.MissTokens = max(0, u.PromptTokens-usage.HitTokens)
	}
	chunk.CacheUsage = usage
	return chunk
}
