// P2-3 ContextInspector endpoint tests. Verifies the
// per-message token breakdown + total / utilisation
// numbers line up with what the agent uses to decide
// when to compact.
package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

// TestContextInspector_EmptySession verifies the
// empty-state response: 0 messages, 0 estimated tokens,
// 0% utilisation. A fresh session should not 500.
func TestContextInspector_EmptySession(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/v1/sessions/"+convID+"/context",
		nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		SessionID       string `json:"session_id"`
		EstimatedTokens int    `json:"estimated_tokens"`
		Messages        []struct {
			Role   string `json:"role"`
			Tokens int    `json:"tokens"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.SessionID != convID {
		t.Errorf("session_id = %q, want %q", body.SessionID, convID)
	}
	if body.EstimatedTokens != 0 {
		t.Errorf("estimated_tokens = %d, want 0 for empty session", body.EstimatedTokens)
	}
	if len(body.Messages) != 0 {
		t.Errorf("messages = %d, want 0", len(body.Messages))
	}
}

// TestContextInspector_BasicCounting inserts 3 messages
// (user / assistant / user) and verifies the per-message
// token counts are non-zero and the total is at least
// the sum.
func TestContextInspector_BasicCounting(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(convID); err != nil {
		t.Fatal(err)
	}

	store.AddMessage(llm.Message{Role: "user", Content: "你好世界"})
	store.AddMessage(llm.Message{Role: "assistant", Content: "hello world"})
	store.AddMessage(llm.Message{Role: "user", Content: "再来一次"})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/v1/sessions/"+convID+"/context",
		nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		EstimatedTokens int    `json:"estimated_tokens"`
		UsableTokens    int    `json:"usable_tokens"`
		UtilizationPct  float64 `json:"utilization_pct"`
		Messages        []struct {
			Role   string `json:"role"`
			Tokens int    `json:"tokens"`
			Preview string `json:"preview"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(body.Messages))
	}
	// Per-message tokens should each be > 0 (we wrote
	// non-empty content for every row).
	for i, m := range body.Messages {
		if m.Tokens <= 0 {
			t.Errorf("messages[%d] (%q): tokens = %d, want > 0", i, m.Role, m.Tokens)
		}
		if m.Preview == "" {
			t.Errorf("messages[%d] (%q): preview empty", i, m.Role)
		}
	}
	// Total should be >= the sum of per-message tokens
	// (per-message overhead is added on top). The
	// EstimateTokensMessages function adds 4 tokens per
	// message, so for 3 messages we expect +12.
	sum := 0
	for _, m := range body.Messages {
		sum += m.Tokens
	}
	if body.EstimatedTokens < sum {
		t.Errorf("total = %d, want >= sum of per-message (%d)", body.EstimatedTokens, sum)
	}
	// Sanity: utilisation = estimated / usable * 100.
	// 0.0..999.9 in valid range. A 3-message session
	// is way under any reasonable threshold so the
	// value should be < 1%.
	if body.UsableTokens <= 0 {
		t.Errorf("usable_tokens = %d, want > 0 (default context window)", body.UsableTokens)
	}
	if body.UtilizationPct < 0 || body.UtilizationPct > 999.9 {
		t.Errorf("utilization_pct = %f, want 0..999.9", body.UtilizationPct)
	}
}

// TestContextInspector_ToolResultFlagged verifies that
// role="tool" rows carry is_tool_result=true so the
// frontend can colour them differently in the drawer.
func TestContextInspector_ToolResultFlagged(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(convID); err != nil {
		t.Fatal(err)
	}

	store.AddMessage(llm.Message{Role: "user", Content: "读 file"})
	store.AddMessage(llm.Message{Role: "tool", Content: "file contents..."})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/v1/sessions/"+convID+"/context",
		nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Messages []struct {
			Role         string `json:"role"`
			IsToolResult bool   `json:"is_tool_result"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(body.Messages))
	}
	if body.Messages[0].IsToolResult {
		t.Error("user message marked as tool_result (should be false)")
	}
	if !body.Messages[1].IsToolResult {
		t.Error("tool message NOT marked as tool_result (should be true)")
	}
}
