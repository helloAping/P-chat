package server

import (
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

func TestApplyUsageMetaRestoresElapsedWithoutCache(t *testing.T) {
	resp := &MessageResponse{}
	applyUsageMeta(resp, `{"tokens_in":"12","tokens_out":"3","elapsed":"3.6s"}`)
	if resp.Elapsed != "3.6s" {
		t.Fatalf("Elapsed = %q, want 3.6s", resp.Elapsed)
	}
	if resp.TokensIn != 12 || resp.TokensOut != 3 {
		t.Fatalf("tokens = %d/%d, want 12/3", resp.TokensIn, resp.TokensOut)
	}
	if resp.CacheHitTokens != nil {
		t.Fatalf("cache hit = %v, want nil when cache was not reported", *resp.CacheHitTokens)
	}
}

func TestBuildMessageResponseHidesInternalResumeRows(t *testing.T) {
	resp := buildMessageResponse(
		llm.ChatMessage{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     "continue",
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		},
		[]string{`{"origin":"auto_resume","ui_hidden":true}`},
		[]int64{123},
		0,
		42,
		7,
		"",
		false,
	)
	if resp != nil {
		t.Fatalf("internal resume row should not render, got %+v", resp)
	}
}

func TestBuildMessageResponseHidesLegacyInternalResumeRows(t *testing.T) {
	cases := []string{
		"⏱ 上一回合因任务尚未完成被中断，请继续完成“下载文档”…",
		"⚠ 系统检测：你刚才的回复没有调用任何工具，但 todo 列表还有未完成项。",
	}
	for _, content := range cases {
		resp := buildMessageResponse(
			llm.ChatMessage{
				Role:        llm.RoleUser,
				Type:        llm.TypeText,
				Content:     content,
				MsgType:     llm.MsgTypeText,
				SubmitToLLM: 1,
			},
			nil,
			[]int64{123},
			0,
			42,
			7,
			"",
			false,
		)
		if resp != nil {
			t.Fatalf("legacy internal resume row should not render, got %+v", resp)
		}
	}
}
