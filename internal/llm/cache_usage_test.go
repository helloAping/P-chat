package llm

import (
	"strings"
	"testing"
)

func TestParseStreamCacheUsage(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		known       bool
		hit, miss   int
	}{
		{"deepseek", `{"prompt_tokens":1000,"completion_tokens":20,"prompt_cache_hit_tokens":850,"prompt_cache_miss_tokens":150}`, true, 850, 150},
		{"details", `{"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":900}}`, true, 900, 100},
		{"cold", `{"prompt_tokens":1000,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":1000}`, true, 0, 1000},
		{"absent", `{"prompt_tokens":1000}`, false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := "data: {\"choices\":[],\"usage\":" + tc.usage + "}\n\ndata: [DONE]\n\n"
			var usage *CacheUsage
			for chunk := range NewOpenAIAdapter("", "", "test").ParseStream(strings.NewReader(stream)) {
				if chunk.TokensIn > 0 {
					usage = chunk.CacheUsage
				}
			}
			if (usage != nil) != tc.known {
				t.Fatalf("cache presence = %v", usage)
			}
			if usage != nil && (usage.HitTokens != tc.hit || usage.MissTokens != tc.miss) {
				t.Fatalf("usage = %+v", usage)
			}
		})
	}
}
