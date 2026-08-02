package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

func TestChatWithTools_BoundsRunawayLLMStream(t *testing.T) {
	const (
		maxExpectedBytes = 1 << 20
		deltaSize        = 8 << 10
	)
	delta := strings.Repeat("x", deltaSize)
	chunkCount := maxExpectedBytes/deltaSize + 4

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < chunkCount; i++ {
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", delta)
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "test",
			Providers: []config.ProviderConfig{{
				Name:     "test",
				Protocol: "openai",
				BaseURL:  srv.URL,
				APIKey:   "test-key",
				Model:    "test-model",
			}},
		},
		Limits: config.LimitsConfig{MaxRounds: 1},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := upgrade.SeedForTesting(store.DB()); err != nil {
		t.Fatal(err)
	}
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatal(err)
	}

	agt := New(cfg, llmClient, styleMgr, store, tool.NewRegistry())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var contentBytes int
	var sawLimitError bool
	for chunk := range agt.ChatWithTools(ctx, ChatRequest{
		Style:     style.Tech,
		Provider:  "test",
		Model:     "test-model",
		SessionID: "stream-limit-test",
		Messages: []llm.ChatMessage{{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     "start",
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		}},
	}) {
		contentBytes += len(chunk.Content)
		if strings.Contains(chunk.Error, "stream output limit") {
			sawLimitError = true
		}
	}

	if contentBytes > maxExpectedBytes {
		t.Fatalf("streamed content = %d bytes, want at most %d", contentBytes, maxExpectedBytes)
	}
	if !sawLimitError {
		t.Fatal("runaway stream should terminate with a stream output limit error")
	}
}
