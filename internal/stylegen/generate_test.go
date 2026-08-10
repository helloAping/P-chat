package stylegen

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
)

// ───────────────────────── test scaffolding ─────────────────────────

// fakeLLM is a scripted LLMClient: it streams a fixed chunk list and then
// closes. Set err to make the stream fail.
type fakeLLM struct {
	chunks []llm.StreamChunk
	err    error
}

func (f *fakeLLM) ChatStream(ctx context.Context, provider, model string, msgs []llm.Message) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk)
	go func() {
		defer close(ch)
		for _, c := range f.chunks {
			select {
			case ch <- c:
			case <-ctx.Done():
				return
			}
		}
		if f.err != nil {
			select {
			case ch <- llm.StreamChunk{Err: f.err}:
			case <-ctx.Done():
			}
			return
		}
		ch <- llm.StreamChunk{Done: true}
	}()
	return ch
}

func llmText(s string) *fakeLLM {
	return &fakeLLM{chunks: []llm.StreamChunk{{Content: s}}}
}

// seedStyles creates the styles table and seeds the three built-in rows,
// mirroring what the upgrade package does on first run.
func seedStyles(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS styles (
		id          TEXT PRIMARY KEY,
		label       TEXT NOT NULL DEFAULT '',
		prompt      TEXT NOT NULL DEFAULT '',
		memory      TEXT NOT NULL DEFAULT '',
		is_builtin  INTEGER NOT NULL DEFAULT 0,
		created_at  TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at  TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("create styles: %v", err)
	}
	builtins := map[string][2]string{
		"cute":    {"小P (PiPi)", "# 小P\n可爱风。"},
		"guofeng": {"墨言 (MoYan)", "# 墨言\n古风。"},
		"tech":    {"NEXUS (零号)", "# NEXUS\n科技风。"},
	}
	for id, pair := range builtins {
		if _, err := db.Exec(`INSERT OR IGNORE INTO styles
			(id, label, prompt, memory, is_builtin, created_at, updated_at)
			VALUES (?, ?, ?, '', 1, datetime('now'), datetime('now'))`, id, pair[0], pair[1]); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

// testDeps builds an isolated Store + StyleMgr on a temp DB with the
// given fake LLM. It also creates one conversation and returns its id.
func testDeps(t *testing.T, llmClient LLMClient) (Deps, string) {
	t.Helper()
	store, err := memory.OpenAt(filepath.Join(t.TempDir(), "test.db"), 100)
	if err != nil {
		t.Fatalf("memory.OpenAt: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatalf("style.NewManager: %v", err)
	}
	seedStyles(t, store.DB())
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	return Deps{
		Store:    store,
		StyleMgr: styleMgr,
		LLM:      llmClient,
		Provider: "test-provider",
		MaxChars: 100000,
	}, convID
}

func addChat(t *testing.T, store *memory.Store, convID string, msgs ...llm.ChatMessage) {
	t.Helper()
	for _, m := range msgs {
		store.AddChatMessageTo(convID, m)
	}
	_ = store.Flush()
}

func textMsg(role, content string) llm.ChatMessage {
	return llm.ChatMessage{Role: role, Type: llm.TypeText, Content: content, MsgType: llm.MsgTypeText, SubmitToLLM: 1}
}

// ───────────────────────── buildTranscript ─────────────────────────

func TestBuildTranscript_FiltersNonTextAndOrdersOldestFirst(t *testing.T) {
	deps, convID := testDeps(t, llmText("{}"))
	addChat(t, deps.Store, convID,
		textMsg(llm.RoleUser, "第一条用户消息"),
		llm.ChatMessage{Role: llm.RoleAssistant, Type: llm.TypeThinking, Content: "hidden thinking", MsgType: llm.MsgTypeText, SubmitToLLM: 0},
		llm.ChatMessage{Role: llm.RoleTool, Type: llm.TypeToolCall, Content: `{"name":"read"}`, MsgType: llm.MsgTypeTool, SubmitToLLM: 1},
		llm.ChatMessage{Role: llm.RoleTool, Type: llm.TypeToolResult, Content: "file contents", MsgType: llm.MsgTypeTool, SubmitToLLM: 1},
		llm.ChatMessage{Role: llm.RoleAssistant, Type: "command", Content: "$ ls -la", MsgType: llm.MsgTypeCommand, SubmitToLLM: 0},
		textMsg(llm.RoleAssistant, "AI 的回复"),
		textMsg(llm.RoleUser, ""), // empty content → dropped
	)

	tr := buildTranscript(deps, convID)
	if strings.Contains(tr, "hidden thinking") || strings.Contains(tr, "file contents") ||
		strings.Contains(tr, "ls -la") || strings.Contains(tr, `"name":"read"`) {
		t.Fatalf("transcript leaked filtered content:\n%s", tr)
	}
	if !strings.Contains(tr, "第一条用户消息") || !strings.Contains(tr, "AI 的回复") {
		t.Fatalf("transcript missing text messages:\n%s", tr)
	}
	// Oldest-first ordering: user message must precede the AI reply.
	if strings.Index(tr, "第一条用户消息") > strings.Index(tr, "AI 的回复") {
		t.Fatalf("transcript not oldest-first:\n%s", tr)
	}
}

func TestBuildTranscript_TruncatesOldestFirst(t *testing.T) {
	deps, convID := testDeps(t, llmText("{}"))
	deps.MaxChars = 40
	// Two long messages; the cap (40) fits only the newest one.
	addChat(t, deps.Store, convID,
		textMsg(llm.RoleUser, strings.Repeat("旧", 100)),
		textMsg(llm.RoleAssistant, "新回复NEW"),
	)
	tr := buildTranscript(deps, convID)
	if !strings.Contains(tr, "新回复NEW") {
		t.Fatalf("newest message dropped:\n%s", tr)
	}
	if strings.Contains(tr, "旧") {
		t.Fatalf("oldest message should be truncated away:\n%s", tr)
	}
}

// ───────────────────────── parseGenOutput ─────────────────────────

func TestParseGenOutput_FencedJSON(t *testing.T) {
	raw := "```json\n{\"id\": \"gentle-teacher\", \"prompt\": \"# 温柔老师\", \"memory\": \"喜欢温和语气\"}\n```"
	out := parseGenOutput(raw)
	if out.ID != "gentle-teacher" || out.Prompt != "# 温柔老师" || out.Memory != "喜欢温和语气" {
		t.Fatalf("unexpected parse: %+v", out)
	}
}

func TestParseGenOutput_BareJSON(t *testing.T) {
	out := parseGenOutput(`{"id":"code-sensei","prompt":"# X","memory":"m"}`)
	if out.ID != "code-sensei" || out.Prompt != "# X" || out.Memory != "m" {
		t.Fatalf("unexpected parse: %+v", out)
	}
}

func TestParseGenOutput_EmbeddedInProse(t *testing.T) {
	out := parseGenOutput(`好的，这是生成的风格：{"id":"x","prompt":"# P","memory":"M"} 希望对你有帮助。`)
	if out.ID != "x" || out.Prompt != "# P" || out.Memory != "M" {
		t.Fatalf("unexpected parse: %+v", out)
	}
}

func TestParseGenOutput_FallbackWholeText(t *testing.T) {
	raw := "这不是 JSON，就是一段人设文字"
	out := parseGenOutput(raw)
	if out.Prompt != raw {
		t.Fatalf("fallback should treat whole text as prompt: %+v", out)
	}
	if out.Memory != "" || out.ID != "" {
		t.Fatalf("fallback should leave memory/id empty: %+v", out)
	}
}

// ───────────────────────── Generate: create ─────────────────────────

func TestGenerate_Create(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"id":"gentle-teacher","prompt":"# 温柔老师\n## 人设\n...","memory":"喜欢温和语气"}`))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "帮我写代码"))

	var stages []string
	res, err := Generate(context.Background(), deps, Params{
		Mode: "create", Label: "温柔老师", Requirement: "语气要温和", ConversationID: convID,
	}, func(ev ProgressEvent) {
		if ev.Content == "" && ev.Thinking == "" && ev.Stage != "" {
			stages = append(stages, ev.Stage)
		}
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.ID != "gentle-teacher" {
		t.Errorf("id = %q, want gentle-teacher", res.ID)
	}
	if res.Updated {
		t.Error("create should not report updated")
	}
	if got := deps.StyleMgr.DisplayLabel(style.Style("gentle-teacher")); got != "温柔老师" {
		t.Errorf("label = %q", got)
	}
	prompt, err := deps.StyleMgr.GetSystemPrompt(style.Style("gentle-teacher"))
	if err != nil {
		t.Fatalf("GetSystemPrompt: %v", err)
	}
	if !strings.Contains(prompt, "## 人设") {
		t.Errorf("prompt not stored: %q", prompt)
	}
	mem, _ := deps.StyleMgr.GetMemory(style.Style("gentle-teacher"))
	if mem != "喜欢温和语气" {
		t.Errorf("memory = %q", mem)
	}
	// Full stage sequence ran.
	for _, want := range []string{"reading", "analyzing", "generating", "saving"} {
		found := false
		for _, s := range stages {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("stage %q missing, got %v", want, stages)
		}
	}
}

func TestGenerate_CreateFallsBackToDerivedID(t *testing.T) {
	// LLM omits the id field → derive from the ASCII label.
	deps, convID := testDeps(t, llmText(`{"prompt":"# x","memory":""}`))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "hi"))
	res, err := Generate(context.Background(), deps, Params{
		Mode: "create", Label: "code-sensei", ConversationID: convID,
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.ID != "code-sensei" {
		t.Errorf("id = %q, want derived code-sensei", res.ID)
	}
}

// TestGenerate_CreateChineseID_FallsBackToASCIILabel: the LLM returned a
// Chinese id, which ASCII-normalises to "" — the id must fall back to the
// ASCII label instead of failing with E_ARGS.
func TestGenerate_CreateChineseID_FallsBackToASCIILabel(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"id":"温柔老师","prompt":"# x","memory":""}`))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "hi"))
	res, err := Generate(context.Background(), deps, Params{
		Mode: "create", Label: "gentle-teacher", ConversationID: convID,
	}, nil)
	if err != nil {
		t.Fatalf("create with Chinese LLM id must not fail, got: %v", err)
	}
	if res.ID != "gentle-teacher" {
		t.Errorf("id = %q, want gentle-teacher (derived from ASCII label)", res.ID)
	}
	if res.ID != deriveID(res.ID) {
		t.Errorf("id %q must be ASCII-normalisable", res.ID)
	}
}

// TestGenerate_CreateChineseIDAndChineseLabel: when BOTH the LLM id and
// the label are Chinese-only (normalise to ""), fall back to a generated
// ASCII id instead of failing with E_ARGS.
func TestGenerate_CreateChineseIDAndChineseLabel(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"id":"温柔老师","prompt":"# x","memory":""}`))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "hi"))
	res, err := Generate(context.Background(), deps, Params{
		Mode: "create", Label: "温柔老师", ConversationID: convID,
	}, nil)
	if err != nil {
		t.Fatalf("create with all-Chinese id/label must not fail, got: %v", err)
	}
	if res.ID == "" {
		t.Fatal("generated id is empty")
	}
	if res.ID != deriveID(res.ID) {
		t.Errorf("generated id %q must be ASCII-normalisable", res.ID)
	}
	if !strings.HasPrefix(res.ID, "style-") {
		t.Errorf("fallback id = %q, want a style-<hash> prefix", res.ID)
	}
}

// TestGenerate_OptimizeBuiltinChineseID verifies the builtin-copy id also
// survives an all-Chinese LLM id (falls back to label / hash, no E_ARGS).
func TestGenerate_OptimizeBuiltinChineseID(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"id":"科技风","prompt":"# 科技风优化版","memory":""}`))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "科技风再酷一点"))
	origPrompt, _ := deps.StyleMgr.GetSystemPrompt(style.Tech)

	res, err := Generate(context.Background(), deps, Params{
		Mode: "optimize", StyleID: "tech", Label: "科技风·优化版", ConversationID: convID,
	}, nil)
	if err != nil {
		t.Fatalf("builtin copy with Chinese LLM id must not fail, got: %v", err)
	}
	if res.ID == "" || res.ID != deriveID(res.ID) {
		t.Errorf("generated id %q must be non-empty ASCII", res.ID)
	}
	afterPrompt, _ := deps.StyleMgr.GetSystemPrompt(style.Tech)
	if afterPrompt != origPrompt {
		t.Errorf("builtin tech was modified: %q → %q", origPrompt, afterPrompt)
	}
}

func TestGenerate_CreateEmptyConversationNoRequirement(t *testing.T) {
	deps, convID := testDeps(t, llmText("{}"))
	res, err := Generate(context.Background(), deps, Params{
		Mode: "create", Label: "x", ConversationID: convID,
	}, nil)
	if err == nil {
		t.Fatal("expected error for empty conversation + no requirement")
	}
	if res != nil {
		t.Errorf("result should be nil, got %+v", res)
	}
	if KindOf(err) != KindEmpty {
		t.Errorf("error kind = %q, want E_EMPTY", KindOf(err))
	}
}

func TestGenerate_CreateEmptyConversationWithRequirement(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"id":"r-only","prompt":"# 仅凭要求","memory":""}`))
	res, err := Generate(context.Background(), deps, Params{
		Mode: "create", Label: "x", Requirement: "只要要求也能生成", ConversationID: convID,
	}, nil)
	if err != nil {
		t.Fatalf("Generate with requirement should succeed: %v", err)
	}
	if res.ID != "r-only" {
		t.Errorf("id = %q", res.ID)
	}
}

func TestGenerate_CreateDuplicateID(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"id":"gentle-teacher","prompt":"# x","memory":""}`))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "hi"))
	if _, err := deps.StyleMgr.Create("gentle-teacher", "已存在", "# 已存在", ""); err != nil {
		t.Fatalf("seed existing style: %v", err)
	}
	_, err := Generate(context.Background(), deps, Params{
		Mode: "create", Label: "温柔老师", ConversationID: convID,
	}, nil)
	if err == nil {
		t.Fatal("expected E_DUP error")
	}
	if KindOf(err) != KindDup {
		t.Errorf("error kind = %q, want E_DUP", KindOf(err))
	}
}

func TestGenerate_LLMError(t *testing.T) {
	deps, convID := testDeps(t, &fakeLLM{err: context.DeadlineExceeded})
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "hi"))
	_, err := Generate(context.Background(), deps, Params{
		Mode: "create", Label: "x", ConversationID: convID,
	}, nil)
	if err == nil {
		t.Fatal("expected LLM error")
	}
	if KindOf(err) != KindLLM {
		t.Errorf("error kind = %q, want E_LLM", KindOf(err))
	}
}

func TestGenerate_BadMode(t *testing.T) {
	deps, convID := testDeps(t, llmText("{}"))
	_, err := Generate(context.Background(), deps, Params{
		Mode: "wat", Label: "x", ConversationID: convID,
	}, nil)
	if KindOf(err) != KindArgs {
		t.Errorf("error kind = %q, want E_ARGS", KindOf(err))
	}
}

// ───────────────────────── Generate: optimize ─────────────────────────

func TestGenerate_OptimizeCustomUpdatesInPlace(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"prompt":"# 优化后 prompt","memory":"新记忆"}`))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "我们要更简洁一些"))
	if _, err := deps.StyleMgr.Create("custom1", "自定义风格", "# 原始 prompt", "原始记忆"); err != nil {
		t.Fatalf("seed custom style: %v", err)
	}

	res, err := Generate(context.Background(), deps, Params{
		Mode: "optimize", StyleID: "custom1", Label: "自定义风格·新版", ConversationID: convID,
	}, nil)
	if err != nil {
		t.Fatalf("Generate optimize: %v", err)
	}
	if !res.Updated {
		t.Error("custom optimize should report updated in place")
	}
	if res.ID != "custom1" {
		t.Errorf("id = %q, want custom1", res.ID)
	}
	prompt, _ := deps.StyleMgr.GetSystemPrompt(style.Style("custom1"))
	if prompt != "# 优化后 prompt" {
		t.Errorf("custom style not updated in place: %q", prompt)
	}
	mem, _ := deps.StyleMgr.GetMemory(style.Style("custom1"))
	if mem != "新记忆" {
		t.Errorf("custom memory = %q", mem)
	}
}

func TestGenerate_OptimizeBuiltinCopiesToNewStyle(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"id":"nexus-v2","prompt":"# 科技风优化版","memory":"新的记忆"}`))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "科技风再酷一点"))
	origPrompt, _ := deps.StyleMgr.GetSystemPrompt(style.Tech)

	res, err := Generate(context.Background(), deps, Params{
		Mode: "optimize", StyleID: "tech", Label: "科技风·优化版", ConversationID: convID,
	}, nil)
	if err != nil {
		t.Fatalf("Generate optimize builtin: %v", err)
	}
	if res.Updated {
		t.Error("builtin optimize should NOT report in-place update")
	}
	if res.ID != "nexus-v2" {
		t.Errorf("id = %q, want nexus-v2", res.ID)
	}
	// Original built-in is untouched.
	afterPrompt, _ := deps.StyleMgr.GetSystemPrompt(style.Tech)
	if afterPrompt != origPrompt {
		t.Errorf("builtin tech was modified!\nbefore=%q\nafter=%q", origPrompt, afterPrompt)
	}
	// New style exists with the optimized content.
	newPrompt, err := deps.StyleMgr.GetSystemPrompt(style.Style("nexus-v2"))
	if err != nil {
		t.Fatalf("new style missing: %v", err)
	}
	if newPrompt != "# 科技风优化版" {
		t.Errorf("new style prompt = %q", newPrompt)
	}
}

func TestGenerate_OptimizeNotFound(t *testing.T) {
	deps, convID := testDeps(t, llmText("{}"))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "hi"))
	_, err := Generate(context.Background(), deps, Params{
		Mode: "optimize", StyleID: "does-not-exist", Label: "x", ConversationID: convID,
	}, nil)
	if err == nil {
		t.Fatal("expected E_NOT_FOUND")
	}
	if KindOf(err) != KindNotFound {
		t.Errorf("error kind = %q, want E_NOT_FOUND", KindOf(err))
	}
}

// TestGenerate_OptimizeEmptyLabel_KeepsOriginalName is the regression for
// the CLI /stylegen optimize bug: optimize with an empty label must NOT
// fail with E_ARGS — it keeps the target style's current name.
func TestGenerate_OptimizeEmptyLabel_KeepsOriginalName(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"prompt":"# 优化后 prompt","memory":"新记忆"}`))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "我们要更简洁一些"))
	if _, err := deps.StyleMgr.Create("custom1", "自定义风格", "# 原始 prompt", "原始记忆"); err != nil {
		t.Fatalf("seed custom style: %v", err)
	}

	res, err := Generate(context.Background(), deps, Params{
		Mode: "optimize", StyleID: "custom1", Label: "", Requirement: "更简洁", ConversationID: convID,
	}, nil)
	if err != nil {
		t.Fatalf("optimize with empty label must succeed, got: %v", err)
	}
	if KindOf(err) == KindArgs {
		t.Fatal("optimize with empty label must not return E_ARGS")
	}
	if !res.Updated {
		t.Error("custom optimize should report updated in place")
	}
	if res.Label != "自定义风格" {
		t.Errorf("label = %q, want original name 自定义风格", res.Label)
	}
	prompt, _ := deps.StyleMgr.GetSystemPrompt(style.Style("custom1"))
	if prompt != "# 优化后 prompt" {
		t.Errorf("custom style not updated: %q", prompt)
	}
}

// TestGenerate_OptimizeBuiltinEmptyLabel verifies the built-in read-only
// copy branch is reachable with an empty label and still marks the copy
// as an optimized variant ("·优化版").
func TestGenerate_OptimizeBuiltinEmptyLabel(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"id":"nexus-v2","prompt":"# 科技风优化版","memory":""}`))
	addChat(t, deps.Store, convID, textMsg(llm.RoleUser, "科技风再酷一点"))
	origPrompt, _ := deps.StyleMgr.GetSystemPrompt(style.Tech)

	res, err := Generate(context.Background(), deps, Params{
		Mode: "optimize", StyleID: "tech", Label: "", ConversationID: convID,
	}, nil)
	if err != nil {
		t.Fatalf("optimize builtin with empty label must succeed, got: %v", err)
	}
	if res.Updated {
		t.Error("builtin optimize should NOT report in-place update")
	}
	if !strings.Contains(res.Label, "·优化版") {
		t.Errorf("builtin copy label = %q, want a '·优化版' suffix", res.Label)
	}
	afterPrompt, _ := deps.StyleMgr.GetSystemPrompt(style.Tech)
	if afterPrompt != origPrompt {
		t.Errorf("builtin tech was modified!\nbefore=%q\nafter=%q", origPrompt, afterPrompt)
	}
}

// ───────────────────────── read-only contract ─────────────────────────

func TestGenerate_DoesNotTouchSourceConversation(t *testing.T) {
	deps, convID := testDeps(t, llmText(`{"id":"ro","prompt":"# 只读","memory":""}`))
	addChat(t, deps.Store, convID,
		textMsg(llm.RoleUser, "第一条"),
		textMsg(llm.RoleAssistant, "第二条"),
	)

	beforeMsgs, _, _ := deps.Store.GetChatMessagesWithMetaFor(convID, 0)
	beforeCount := len(beforeMsgs)
	titleBefore := "未变"

	_, err := Generate(context.Background(), deps, Params{
		Mode: "create", Label: "只读验证", ConversationID: convID,
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	afterMsgs, _, _ := deps.Store.GetChatMessagesWithMetaFor(convID, 0)
	if len(afterMsgs) != beforeCount {
		t.Fatalf("source conversation changed: %d → %d messages", beforeCount, len(afterMsgs))
	}
	// Neither role nor content of the snapshot changed.
	for i := range beforeMsgs {
		if beforeMsgs[i].Role != afterMsgs[i].Role || beforeMsgs[i].Content != afterMsgs[i].Content {
			t.Fatalf("message %d changed: %+v → %+v", i, beforeMsgs[i], afterMsgs[i])
		}
	}
	if titleBefore != "未变" {
		t.Fatal("title changed")
	}
}
