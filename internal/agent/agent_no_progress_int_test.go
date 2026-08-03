package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

// roundStartRe matches the per-round start event step ("round-1", "round-2").
var roundStartRe = regexp.MustCompile(`^round-\d+$`)

// TestChatWithTools_NoProgressLoop_ForcesStop is the end-to-end regression test
// for the 2026-08 recipes.js runaway loop. An SSE server that ALWAYS answers
// with the same successful read_file tool call reproduces the pathological
// shape: every tool succeeds, nothing mutates, the same file is re-read every
// round. The loop must be stopped by the no-progress guard (a force-stop
// event), not run until the round cap.
func TestChatWithTools_NoProgressLoop_ForcesStop(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "stuck.txt")
	if err := os.WriteFile(tmp, []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readArgs, err := json.Marshal(map[string]string{"path": filepath.ToSlash(tmp)})
	if err != nil {
		t.Fatal(err)
	}
	// Unique tool id per request so repeated inserts don't trip a unique index.
	var callSeq atomic.Uint64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		id := fmt.Sprintf("call_stuck_%d", callSeq.Add(1))
		// arguments must be a JSON-encoded string, not a bare object.
		args := strconv.Quote(string(readArgs))
		line := fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":"read_file","arguments":%s}}]}}]}`,
			id, args)
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, line+"\n\n")
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
		// The round cap is high enough that only the no-progress guard (not the
		// round cap) can terminate the loop.
		Limits: config.LimitsConfig{MaxRounds: 50},
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
	reg := tool.NewRegistry()
	tool.RegisterBuiltin(reg)
	agt := New(cfg, llmClient, styleMgr, store, reg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var sawDirective, sawForceStop bool
	var rounds int
	for chunk := range agt.ChatWithTools(ctx, ChatRequest{
		Style:     style.Tech,
		Provider:  "test",
		Model:     "test-model",
		SessionID: "no-progress-int-test",
		Messages: []llm.ChatMessage{{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     "start",
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		}},
	}) {
		if chunk.Phase == "stuck" && chunk.Step == "no-progress-directive" {
			sawDirective = true
		}
		if chunk.Phase == "stuck" && chunk.Step == "no-progress-force-stop" {
			sawForceStop = true
		}
		// Count round-START events only (the -done/-tools suffixes would
		// double count each round).
		if chunk.Phase == "llm" && roundStartRe.MatchString(chunk.Step) {
			rounds++
		}
	}
	if !sawForceStop {
		t.Fatalf("no-progress loop was not force-stopped (directive=%v forceStop=%v rounds=%d)", sawDirective, sawForceStop, rounds)
	}
	// The force-stop must fire well before the 50-round cap, proving the
	// no-progress guard (not the round cap) ended the turn.
	if rounds > noProgressForceStopAfter+3 {
		t.Errorf("turn ran %d rounds before stopping, want ~%d (force-stop threshold)", rounds, noProgressForceStopAfter+1)
	}
}

// TestChatWithTools_CumulativeToolErrors_BreaksWhackAMole is the end-to-end
// regression test for the "whack-a-mole" sub-agent spin: each round the model
// calls exec_command with a DIFFERENT failing command (the 2026-08 explore
// `find` on Windows case), so the stuck-loop guard (identical signature) never
// fires and the same-tool guard (identical name) never fires either. The
// cumulative failure breaker must inject the "stop and summarise" instruction
// after CumToolErrMax total failures instead of spinning until the round cap.
func TestChatWithTools_CumulativeToolErrors_BreaksWhackAMole(t *testing.T) {
	// Always emit a failing exec_command, but with a UNIQUE command string
	// every round so the stuck-loop signature never repeats. `exit 1`
	// reliably fails on both cmd and sh, so roundAnyToolErrored flips
	// and the cumulative breaker can see it.
	var callSeq atomic.Uint64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		id := fmt.Sprintf("call_fail_%d", callSeq.Add(1))
		cmd := fmt.Sprintf("exit %d", callSeq.Load())
		args, _ := json.Marshal(map[string]string{"command": cmd})
		line := fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":"exec_command","arguments":%s}}]}}]}`,
			id, strconv.Quote(string(args)))
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, line+"\n\n")
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
		// Low round cap so a broken cumulative breaker (the
		// regression this test guards against) fails fast instead
		// of running 300 rounds. The fake LLM ignores the "stop"
		// system message, so the loop keeps failing until the cap
		// — the breaker is a *nudge*, not a hard stop; a real LLM
		// obeys the instruction and summarises. What we assert is
		// that the breaker FIRES, i.e. the cum-tool-err-limit
		// event exists at all.
		Limits: config.LimitsConfig{MaxRounds: 20},
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
	reg := tool.NewRegistry()
	tool.RegisterBuiltin(reg)
	agt := New(cfg, llmClient, styleMgr, store, reg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var sawCumBreaker bool
	var rounds int
	for chunk := range agt.ChatWithTools(ctx, ChatRequest{
		Style:     style.Tech,
		Provider:  "test",
		Model:     "test-model",
		SessionID: "cum-err-int-test",
		Messages: []llm.ChatMessage{{
			Role:        llm.RoleUser,
			Type:        llm.TypeText,
			Content:     "search the codebase",
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		}},
	}) {
		if chunk.Phase == "limit" && chunk.Step == "cum-tool-err-limit" {
			sawCumBreaker = true
		}
		if chunk.Phase == "llm" && roundStartRe.MatchString(chunk.Step) {
			rounds++
		}
	}
	if !sawCumBreaker {
		t.Fatalf("cumulative failure breaker never fired (rounds=%d) — the whack-a-mole loop ran to the round cap without the cum-tool-err-limit event", rounds)
	}
	// The breaker must fire BEFORE the round cap — i.e. the
	// cum-tool-err-limit event exists at all. The fake LLM ignores
	// the "stop" instruction, so rounds may reach MaxRounds (20);
	// that's expected for a non-compliant model. The regression we
	// guard against is the breaker NEVER firing, which would leave
	// the sub-agent spinning until its wall-clock timeout.
	if rounds >= 20 {
		t.Logf("breaker fired but the fake LLM kept failing to the round cap (rounds=%d) — expected, the real LLM obeys the stop instruction", rounds)
	}
}
