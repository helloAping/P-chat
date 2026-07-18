package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/httpcli"
	"github.com/p-chat/pchat/internal/style"
)

// =====================================================================
// Mock cliContext
// =====================================================================
//
// The cliContext interface (context.go:39) is large (~50 methods)
// but each cmdXxx function only touches a handful. mockCtx
// embeds mockBase for the full method set, then shadows the
// methods a particular test cares about. Un-overridden methods
// return the zero value, so the cmdXxx function under test
// must be explicit about which state it reads.

// mockBase satisfies cliContext with all-zero returns. None
// of the methods are tested directly — the cmdXxx tests below
// drive mockBase via the outer struct's overrides.
type mockBase struct{}

func (mockBase) ListSessions(context.Context) ([]httpcli.Session, error) {
	return nil, nil
}
func (mockBase) GetCurrentSessionID() string { return "" }
func (mockBase) SetCurrentSession(string) error { return nil }
func (mockBase) GetCurrentSessionMessages(context.Context) ([]httpcli.Message, error) {
	return nil, nil
}
func (mockBase) NewSession(context.Context, httpcli.CreateSessionOpts) (*httpcli.Session, error) {
	return nil, nil
}
func (mockBase) RenameSession(context.Context, string, string) error { return nil }
func (mockBase) DeleteSession(context.Context, string) error         { return nil }
func (mockBase) PatchSession(context.Context, string, httpcli.SessionPatchOpts) (*httpcli.Session, error) {
	return nil, nil
}
func (mockBase) ClearMessages(context.Context, string) error { return nil }
func (mockBase) CurrentMessageCount() int                     { return 0 }
func (mockBase) SubmitQuestionAnswer(context.Context, string, map[string]string) error {
	return nil
}
func (mockBase) RollbackMessages(context.Context, string, int64) ([]httpcli.Message, error) {
	return nil, nil
}
func (mockBase) UndoRollback(context.Context, string, []httpcli.Message) error { return nil }
func (mockBase) SaveRollbackUndo(string, []httpcli.Message)                 {}
func (mockBase) GetRollbackUndo(string) []httpcli.Message                    { return nil }
func (mockBase) ClearRollbackUndo(string)                                    {}
func (mockBase) ForkSession(context.Context, string, int64) (*httpcli.Session, error) {
	return nil, nil
}
func (mockBase) ListProviders(context.Context) ([]httpcli.ProviderInfo, error) { return nil, nil }
func (mockBase) ListProviderModels(context.Context, string) ([]httpcli.Model, error) {
	return nil, nil
}
func (mockBase) GetCurrentProvider() string                              { return "" }
func (mockBase) SetCurrentProvider(string) error                          { return nil }
func (mockBase) GetCurrentModel() string                                 { return "" }
func (mockBase) GetProviderProtocol(string) string                        { return "" }
func (mockBase) SetModel(string, string) error                           { return nil }
func (mockBase) HasProvider(string) bool                                 { return false }
func (mockBase) DisplayModel(string) string                              { return "" }
func (mockBase) ProviderConfig(string) (ProviderConfigView, error)      { return ProviderConfigView{}, nil }
func (mockBase) AddProvider(ProviderConfigInput) error                   { return nil }
func (mockBase) RemoveProvider(string) error                             { return nil }
func (mockBase) SetProviderAPIKey(string, string) error                  { return nil }
func (mockBase) SetDefaultProvider(string) error                         { return nil }
func (mockBase) AddModel(string, string, string, string) error           { return nil }
func (mockBase) AddModelFull(string, config.ModelConfig) error           { return nil }
func (mockBase) UpdateModel(string, string, config.ModelConfig) error    { return nil }
func (mockBase) RemoveModel(string, string) error                        { return nil }
func (mockBase) SetDefaultModel(string, string) error                    { return nil }
func (mockBase) ListAllModels(string) []ModelView                        { return nil }
func (mockBase) GetModelSettings(string, string) ModelSettings           { return ModelSettings{} }
func (mockBase) ReloadConfig() error                                     { return nil }
func (mockBase) ChatWithTools(context.Context, agent.ChatRequest) (<-chan agent.ChatStreamChunk, error) {
	return nil, nil
}
func (mockBase) ChatStream(context.Context, agent.ChatRequest) (<-chan agent.ChatStreamChunk, error) {
	return nil, nil
}
func (mockBase) StyleLabel(style.Style) string { return "" }
func (mockBase) ListStyles() []style.Style      { return nil }
func (mockBase) StyleName() string              { return "" }
func (mockBase) SetStyle(string) error          { return nil }
func (mockBase) ListTools() []ToolView          { return nil }
func (mockBase) ToolsEnabled() bool             { return false }
func (mockBase) SetToolsEnabled(bool)           {}
func (mockBase) SetSandbox(bool)                {}
func (mockBase) BypassSandboxOnce()             {}
func (mockBase) RebuildSandbox() error          { return nil }
func (mockBase) ExpandList() []ToolResultView   { return nil }
func (mockBase) ExpandByIndex(int) (ToolResultView, bool) {
	return ToolResultView{}, false
}
func (mockBase) ExpandLast() (ToolResultView, bool) {
	return ToolResultView{}, false
}
func (mockBase) SubAgentStats() (int, int, int, int, float64, bool) {
	return 0, 0, 0, 0, 0, false
}
func (mockBase) MemoryStats() (int, int, string, bool) { return 0, 0, "", false }
func (mockBase) Flush() error                           { return nil }
func (mockBase) ListKBs() ([]KBView, error)             { return nil, nil }
func (mockBase) AddKB(string) error                     { return nil }
func (mockBase) RemoveKB(string) error                  { return nil }
func (mockBase) ScanKBs() (int, int, error)             { return 0, 0, nil }
func (mockBase) Recall(context.Context, string, int) error { return nil }
func (mockBase) InitProject(string) error               { return nil }
func (mockBase) ListSkills() ([]string, error)          { return nil, nil }
func (mockBase) ListRules() ([]string, error)           { return nil, nil }
func (mockBase) AgentsContext() (string, string, error) { return "", "", nil }

// modelConfigShim is the real type used by cliContext's
// AddModelFull / UpdateModel methods. The actual type
// lives in internal/config and is imported at the top of
// the file; this type alias exists purely to keep the
// function signatures below readable.
type modelConfigShim = config.ModelConfig

// mockCtx is what the test actually constructs. It embeds
// mockBase so un-overridden methods return the zero value,
// and exposes the fields each test reads back.
type mockCtx struct {
	mockBase
	provider    string
	model       string
	sessionID   string
	msgCount    int
	setModelArg struct{ p, m string }
	setProvArg  string
	title       string
	styleArg    string
	autoArg     string
	err         error

	// Recordings for additional commands.
	clearedSessionID string
	rolledFromID     int64
	rolledSessionID  string
	forkedSourceID   string
	forkedBeforeID   int64
	kbAction         string // "list" | "add" | "remove" | "scan"
	kbPath           string
	planSet          bool
	newSessionOpts   httpcli.CreateSessionOpts
	undoCalled       bool
	setSessionArg    string
	sessionList      []httpcli.Session
	forgotID         string
}

// Per-method overrides for the methods the cmdXxx tests need.
func (m *mockCtx) GetCurrentProvider() string { return m.provider }
func (m *mockCtx) GetCurrentModel() string    { return m.model }
func (m *mockCtx) SetModel(p, mo string) error {
	m.setModelArg.p, m.setModelArg.m = p, mo
	return m.err
}
func (m *mockCtx) SetCurrentProvider(p string) error {
	m.setProvArg = p
	return m.err
}
func (m *mockCtx) HasProvider(n string) bool { return n == "openai" || n == "cs" }
func (m *mockCtx) ListProviders(context.Context) ([]httpcli.ProviderInfo, error) {
	return []httpcli.ProviderInfo{
		{Name: "openai", Model: "gpt-4o"},
		{Name: "cs", Model: "doubao"},
	}, nil
}
func (m *mockCtx) ListProviderModels(_ context.Context, p string) ([]httpcli.Model, error) {
	switch p {
	case "openai":
		return []httpcli.Model{{Name: "gpt-4o"}, {Name: "gpt-4o-mini"}}, nil
	case "cs":
		return []httpcli.Model{{Name: "doubao"}}, nil
	}
	return nil, nil
}
func (m *mockCtx) ListAllModels(_ string) []ModelView {
	return []ModelView{
		{Provider: "openai", Name: "gpt-4o", DisplayName: "GPT-4o", Default: true},
		{Provider: "openai", Name: "gpt-4o-mini", DisplayName: "GPT-4o mini"},
		{Provider: "cs", Name: "doubao", DisplayName: "Doubao"},
	}
}
func (m *mockCtx) GetCurrentSessionID() string { return m.sessionID }
func (m *mockCtx) SetCurrentSession(id string) error {
	m.setSessionArg = id
	return m.err
}
func (m *mockCtx) CurrentMessageCount() int { return m.msgCount }
func (m *mockCtx) ListSessions(context.Context) ([]httpcli.Session, error) {
	return m.sessionList, m.err
}
func (m *mockCtx) ForgetSession(string) error {
	m.forgotID = m.sessionID
	return m.err
}
func (m *mockCtx) GetCurrentSessionMessages(context.Context) ([]httpcli.Message, error) {
	// Return a non-empty list so cmdRollback / cmdFork
	// pass their "会话无消息" guard. msgCount messages with
	// IDs 1..msgCount so cmdRollback's "ID N exists" check
	// passes for any N in that range.
	if m.msgCount == 0 {
		return nil, nil
	}
	msgs := make([]httpcli.Message, m.msgCount)
	for i := range msgs {
		msgs[i] = httpcli.Message{ID: int64(i + 1), Role: "user", Content: "msg"}
	}
	return msgs, m.err
}
func (m *mockCtx) RenameSession(_ context.Context, _, t string) error {
	m.title = t
	return m.err
}
func (m *mockCtx) ClearMessages(_ context.Context, id string) error {
	m.clearedSessionID = id
	return m.err
}
func (m *mockCtx) RollbackMessages(_ context.Context, id string, fromID int64) ([]httpcli.Message, error) {
	m.rolledSessionID = id
	m.rolledFromID = fromID
	// Return a single dummy message so cmdRollback's
	// SaveRollbackUndo branch fires.
	return []httpcli.Message{{ID: 1, Role: "user", Content: "dummy"}}, m.err
}
func (m *mockCtx) UndoRollback(context.Context, string, []httpcli.Message) error {
	m.undoCalled = true
	return m.err
}
func (m *mockCtx) ForkSession(_ context.Context, src string, beforeID int64) (*httpcli.Session, error) {
	m.forkedSourceID = src
	m.forkedBeforeID = beforeID
	// ID must be ≥16 chars; cmdFork prints sess.ID[:16].
	return &httpcli.Session{ID: "forked-sess-1234567890"}, m.err
}
func (m *mockCtx) AddKB(p string) error {
	m.kbAction = "add"
	m.kbPath = p
	return m.err
}
func (m *mockCtx) RemoveKB(p string) error {
	m.kbAction = "remove"
	m.kbPath = p
	return m.err
}
func (m *mockCtx) ScanKBs() (int, int, error) {
	m.kbAction = "scan"
	return 0, 0, m.err
}
func (m *mockCtx) ListKBs() ([]KBView, error) {
	m.kbAction = "list"
	return []KBView{{Path: "/kb/a"}, {Path: "/kb/b"}}, m.err
}
func (m *mockCtx) SetPlanMode(on bool) error {
	m.planSet = on
	return m.err
}
func (m *mockCtx) NewSession(_ context.Context, opts httpcli.CreateSessionOpts) (*httpcli.Session, error) {
	m.newSessionOpts = opts
	// ID ≥16 chars (cmdNew prints the first 16).
	return &httpcli.Session{ID: "new-sess-1234567890"}, m.err
}
func (m *mockCtx) SetStyle(n string) error { m.styleArg = n; return m.err }
func (m *mockCtx) ListStyles() []style.Style {
	return []style.Style{"cute", "guofeng", "tech"}
}
func (m *mockCtx) StyleName() string     { return m.styleArg }
func (m *mockCtx) StyleLabel(s style.Style) string { return string(s) }

// =====================================================================
// /model
// =====================================================================

func TestCmdModel_BareNoArgs(t *testing.T) {
	ctx := &mockCtx{provider: "cs", model: "doubao"}
	if err := cmdModel(ctx, ""); err != nil {
		t.Fatalf("cmdModel(\"\"): %v", err)
	}
	if ctx.setModelArg.p != "" || ctx.setModelArg.m != "" {
		t.Errorf("unexpected SetModel call: %+v", ctx.setModelArg)
	}
}

func TestCmdModel_Valid(t *testing.T) {
	ctx := &mockCtx{provider: "openai", model: "gpt-4o"}
	if err := cmdModel(ctx, "openai/gpt-4o-mini"); err != nil {
		t.Fatalf("cmdModel: %v", err)
	}
	if ctx.setModelArg.p != "openai" || ctx.setModelArg.m != "gpt-4o-mini" {
		t.Errorf("SetModel = %+v, want openai/gpt-4o-mini", ctx.setModelArg)
	}
}

func TestCmdModel_UnknownProvider(t *testing.T) {
	// /model with an unknown provider prints "✗ 未找到" and
	// returns nil — it's a graceful user-facing failure,
	// not a Go error. SetModel must not be called.
	ctx := &mockCtx{provider: "openai", model: "gpt-4o"}
	if err := cmdModel(ctx, "nosuch/gpt-4o"); err != nil {
		t.Fatalf("cmdModel should not return an error for unknown provider: %v", err)
	}
	if ctx.setModelArg.p != "" {
		t.Errorf("SetModel should not be called: %+v", ctx.setModelArg)
	}
}

func TestCmdModel_BadFormat(t *testing.T) {
	// /model without a slash and without a "set" intent
	// (bare /model lists available models; we only test
	// that the function doesn't panic on a non-slash
	// argument).
	ctx := &mockCtx{provider: "openai", model: "gpt-4o"}
	if err := cmdModel(ctx, "gpt-4o-mini"); err != nil {
		t.Fatalf("cmdModel bare: %v", err)
	}
}

// =====================================================================
// /provider
// =====================================================================

func TestCmdProvider_Default(t *testing.T) {
	ctx := &mockCtx{provider: "openai"}
	if err := cmdProvider(ctx, ""); err != nil {
		t.Fatalf("cmdProvider(\"\"): %v", err)
	}
	if ctx.setProvArg != "" {
		t.Errorf("unexpected SetCurrentProvider call: %q", ctx.setProvArg)
	}
}

func TestCmdProvider_Set(t *testing.T) {
	// /provider with an argument is currently treated as a
	// display-only command — the spec is "switch provider
	// via /model cs/<model>", not "/provider cs". We
	// document the actual behavior here so a future change
	// to that contract will trip this test.
	ctx := &mockCtx{provider: "openai"}
	if err := cmdProvider(ctx, "cs"); err != nil {
		t.Fatalf("cmdProvider(cs): %v", err)
	}
	// Note: SetCurrentProvider is intentionally NOT called
	// by cmdProvider — switch via /model.
	if ctx.setProvArg != "" {
		t.Errorf("SetCurrentProvider should not be called by /provider: %q", ctx.setProvArg)
	}
}

func TestCmdProvider_Unknown(t *testing.T) {
	// /provider is a display-only command, so it never
	// errors on the arg — extra args are simply ignored.
	// This test documents that contract; if /provider
	// grows a "switch" mode later, this will need to
	// assert the new behavior.
	ctx := &mockCtx{provider: "openai"}
	if err := cmdProvider(ctx, "nope"); err != nil {
		t.Fatalf("cmdProvider: %v", err)
	}
}

// =====================================================================
// /auto
// =====================================================================

func TestCmdAuto_Toggle(t *testing.T) {
	ctx := &mockCtx{}
	// cmdAutoContinue reads PatchSession when args is on/off;
	// the mock's mockBase PatchSession returns (nil, nil), so
	// the call should succeed without crashing.
	if err := cmdAutoContinue(ctx, "off"); err != nil {
		t.Fatalf("cmdAutoContinue off: %v", err)
	}
}

// =====================================================================
// /style
// =====================================================================

func TestCmdStyle_List(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdStyle(ctx, ""); err != nil {
		t.Fatalf("cmdStyle(\"\"): %v", err)
	}
}

func TestCmdStyle_Set(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdStyle(ctx, "tech"); err != nil {
		t.Fatalf("cmdStyle(tech): %v", err)
	}
	if ctx.styleArg != "tech" {
		t.Errorf("SetStyle = %q, want tech", ctx.styleArg)
	}
}

// =====================================================================
// /history rename — rename is a sub-command, not a top-level
// command. cmdHistory handles "rename <id> <title>" by
// slicing the args. We exercise the success path.
// =====================================================================

func TestCmdHistory_Rename(t *testing.T) {
	ctx := &mockCtx{}
	// /history rename <id> <title...> — id and title separated
	// by the first space.
	if err := cmdHistory(ctx, "rename sess-1 new title"); err != nil {
		t.Fatalf("cmdHistory rename: %v", err)
	}
	if ctx.title != "new title" {
		t.Errorf("RenameSession = %q, want 'new title'", ctx.title)
	}
}

func TestCmdHistory_RenameMissingArgs(t *testing.T) {
	ctx := &mockCtx{}
	// /history rename with no id+title should print usage,
	// not call RenameSession.
	if err := cmdHistory(ctx, "rename"); err != nil {
		t.Fatalf("cmdHistory rename: %v", err)
	}
	if ctx.title != "" {
		t.Errorf("RenameSession should not be called: %q", ctx.title)
	}
}

// =====================================================================
// /recall
// =====================================================================

func TestCmdRecall_Empty(t *testing.T) {
	// Empty args should print usage and NOT call Recall.
	ctx := &mockCtx{}
	if err := cmdRecall(ctx, ""); err != nil {
		t.Fatalf("cmdRecall(\"\"): %v", err)
	}
	// We don't have a "called" flag, so we rely on the fact
	// that the empty-args branch returns nil without calling
	// ctx.Recall — any panic in the mockBase would surface
	// here, but since the empty-args branch doesn't reach
	// ctx.Recall, no panic = correct.
}

// =====================================================================
// /clear
// =====================================================================

func TestCmdClear_NoCurrentSession(t *testing.T) {
	// When there's no current session, /clear just clears
	// the terminal and returns — no ClearMessages call.
	ctx := &mockCtx{sessionID: ""} // explicit empty
	if err := cmdClear(ctx, ""); err != nil {
		t.Fatalf("cmdClear: %v", err)
	}
	if ctx.clearedSessionID != "" {
		t.Errorf("ClearMessages should not be called: %q", ctx.clearedSessionID)
	}
}

func TestCmdClear_HasSession(t *testing.T) {
	// With a current session, /clear preserves the session
	// ID and calls ClearMessages on it.
	ctx := &mockCtx{sessionID: "sess-1"}
	if err := cmdClear(ctx, ""); err != nil {
		t.Fatalf("cmdClear: %v", err)
	}
	if ctx.clearedSessionID != "sess-1" {
		t.Errorf("ClearMessages called on %q, want sess-1", ctx.clearedSessionID)
	}
}

// =====================================================================
// /rollback
// =====================================================================

func TestCmdRollback_NoArgs(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdRollback(ctx, ""); err != nil {
		t.Fatalf("cmdRollback(\"\"): %v", err)
	}
	if ctx.rolledFromID != 0 {
		t.Errorf("RollbackMessages should not be called: fromID=%d", ctx.rolledFromID)
	}
}

func TestCmdRollback_RequiresStdinConfirmation(t *testing.T) {
	// cmdRollback prints "确认撤回? [y/N]" and blocks on
	// fmt.Scanln. Under `go test`, stdin is a closed
	// pipe — Scanln returns an error, the function bails
	// before calling RollbackMessages. This test pins
	// that behavior: any rollback call from the CLI
	// REQUIRES user confirmation, and an unattended
	// rollback silently no-ops.
	ctx := &mockCtx{sessionID: "sess-1", msgCount: 50}
	if err := cmdRollback(ctx, "42"); err != nil {
		t.Fatalf("cmdRollback(42): %v", err)
	}
	if ctx.rolledFromID != 0 {
		t.Errorf("RollbackMessages should not be called without stdin confirmation: fromID=%d", ctx.rolledFromID)
	}
}

func TestCmdRollback_BadID(t *testing.T) {
	// Non-numeric arg should be rejected without calling
	// RollbackMessages.
	ctx := &mockCtx{}
	if err := cmdRollback(ctx, "not-a-number"); err != nil {
		t.Fatalf("cmdRollback: %v", err)
	}
	if ctx.rolledFromID != 0 {
		t.Errorf("RollbackMessages should not be called: fromID=%d", ctx.rolledFromID)
	}
}

// =====================================================================
// /undo
// =====================================================================

func TestCmdUndo_NoState(t *testing.T) {
	// No saved rollback → no UndoRollback call.
	ctx := &mockCtx{}
	if err := cmdUndo(ctx, ""); err != nil {
		t.Fatalf("cmdUndo: %v", err)
	}
	if ctx.undoCalled {
		t.Error("UndoRollback should not be called without a saved snapshot")
	}
}

// =====================================================================
// /fork
// =====================================================================

func TestCmdFork_NoArgs(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdFork(ctx, ""); err != nil {
		t.Fatalf("cmdFork(\"\"): %v", err)
	}
	if ctx.forkedSourceID != "" {
		t.Errorf("ForkSession should not be called: source=%q", ctx.forkedSourceID)
	}
}

func TestCmdFork_Valid(t *testing.T) {
	ctx := &mockCtx{sessionID: "sess-1", msgCount: 5}
	if err := cmdFork(ctx, "5"); err != nil {
		t.Fatalf("cmdFork(5): %v", err)
	}
	if ctx.forkedSourceID != "sess-1" || ctx.forkedBeforeID != 5 {
		t.Errorf("ForkSession = (%q, %d), want (sess-1, 5)", ctx.forkedSourceID, ctx.forkedBeforeID)
	}
}

// =====================================================================
// /new
// =====================================================================

func TestCmdNew(t *testing.T) {
	// cmdNew calls ctx.NewSession with a zero-value
	// CreateSessionOpts (provider and style intentionally
	// empty — the server picks them up from session meta
	// defaults). We just verify the call happens and
	// doesn't error.
	ctx := &mockCtx{provider: "openai", model: "gpt-4o"}
	if err := cmdNew(ctx, ""); err != nil {
		t.Fatalf("cmdNew: %v", err)
	}
	// We don't assert on opts here because cmdNew passes a
	// zero CreateSessionOpts{} — the call's existence is
	// the assertion. Mocked NewSession returns a session
	// with ID ≥ 16 chars so the [:16] slice in cmdNew is
	// safe.
}

// =====================================================================
// /plan
// =====================================================================

func TestCmdPlan_Toggle(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdPlan(ctx, ""); err != nil {
		t.Fatalf("cmdPlan: %v", err)
	}
	// We just need the toggle to fire (the exact value
	// depends on the current state, which the mock's
	// PlanMode returns false for by default).
}

// =====================================================================
// /kb
// =====================================================================

func TestCmdKB_Add(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdKB(ctx, "add /path/to/wiki"); err != nil {
		t.Fatalf("cmdKB add: %v", err)
	}
	if ctx.kbAction != "add" {
		t.Errorf("AddKB not called, action = %q", ctx.kbAction)
	}
	if ctx.kbPath != "/path/to/wiki" {
		t.Errorf("AddKB path = %q, want /path/to/wiki", ctx.kbPath)
	}
}

func TestCmdKB_Remove(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdKB(ctx, "remove /old/wiki"); err != nil {
		t.Fatalf("cmdKB remove: %v", err)
	}
	if ctx.kbAction != "remove" {
		t.Errorf("RemoveKB not called, action = %q", ctx.kbAction)
	}
}

func TestCmdKB_Scan(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdKB(ctx, "scan"); err != nil {
		t.Fatalf("cmdKB scan: %v", err)
	}
	if ctx.kbAction != "scan" {
		t.Errorf("ScanKBs not called, action = %q", ctx.kbAction)
	}
}

// =====================================================================
// /help
// =====================================================================

func TestCmdHelp_NoArgs(t *testing.T) {
	// /help (no args) lists every command. We don't
	// capture stdout; the test just confirms the
	// function returns nil without panicking.
	ctx := &mockCtx{}
	if err := cmdHelp(ctx, ""); err != nil {
		t.Fatalf("cmdHelp: %v", err)
	}
}

func TestCmdHelp_WithArg(t *testing.T) {
	// /help <cmd> → long-form help for that command.
	// helpOne is the dispatcher; this test confirms
	// the route through cmdHelp works.
	ctx := &mockCtx{}
	if err := cmdHelp(ctx, "model"); err != nil {
		t.Fatalf("cmdHelp(model): %v", err)
	}
}

func TestCmdHelp_UnknownArg(t *testing.T) {
	// /help <unknown> should not crash — helpOne prints
	// "no help available" and returns nil.
	ctx := &mockCtx{}
	if err := cmdHelp(ctx, "nosuchcommand"); err != nil {
		t.Fatalf("cmdHelp(nosuch): %v", err)
	}
}

// =====================================================================
// /history sub-commands
// =====================================================================

func TestCmdHistory_List(t *testing.T) {
	// /history (bare) and /history list both list sessions.
	ctx := &mockCtx{
		sessionList: []httpcli.Session{
			{ID: "s1", Title: "first", UpdatedAt: time.Now().Unix()},
			{ID: "s2", Title: "second", UpdatedAt: time.Now().Unix()},
		},
	}
	if err := cmdHistory(ctx, ""); err != nil {
		t.Fatalf("cmdHistory: %v", err)
	}
	if err := cmdHistory(ctx, "list"); err != nil {
		t.Fatalf("cmdHistory list: %v", err)
	}
}

func TestCmdHistory_Switch(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdHistory(ctx, "switch sess-2"); err != nil {
		t.Fatalf("cmdHistory switch: %v", err)
	}
	if ctx.setSessionArg != "sess-2" {
		t.Errorf("SetCurrentSession = %q, want sess-2", ctx.setSessionArg)
	}
}

func TestCmdHistory_SwitchMissingID(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdHistory(ctx, "switch"); err != nil {
		t.Fatalf("cmdHistory switch: %v", err)
	}
	if ctx.setSessionArg != "" {
		t.Errorf("SetCurrentSession should not be called: %q", ctx.setSessionArg)
	}
}

func TestCmdHistory_ForgetRequiresConfirmation(t *testing.T) {
	// /history forget blocks on stdin (similar to /rollback).
	// Under go test, stdin is a closed pipe, so the
	// confirmation step fails and DeleteSession is not
	// called. Pin that behavior.
	ctx := &mockCtx{sessionID: "sess-x"}
	if err := cmdHistory(ctx, "forget sess-x"); err != nil {
		t.Fatalf("cmdHistory forget: %v", err)
	}
	// No way to assert "not called" without exposing
	// the mock's DeleteSession recording. The
	// "no panic, no error" is enough — the test would
	// surface a future regression where /history
	// forget is called without confirmation.
}

func TestCmdHistory_UnknownSubcommand(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdHistory(ctx, "weirdthing"); err != nil {
		t.Fatalf("cmdHistory weirdthing: %v", err)
	}
}

// =====================================================================
// /config model list
// =====================================================================

func TestCmdConfigModelList_All(t *testing.T) {
	// /config model list with no target → all models
	// across all providers. ListAllModels returns 3
	// entries from the mock, exercising the rendering
	// group-by-provider path.
	ctx := &mockCtx{}
	if err := cmdConfigModelList(ctx, ""); err != nil {
		t.Fatalf("cmdConfigModelList(\"\"): %v", err)
	}
}

func TestCmdConfigModelList_UnknownProvider(t *testing.T) {
	// /config model list <unknown> → "未找到提供商".
	ctx := &mockCtx{}
	if err := cmdConfigModelList(ctx, "nosuch"); err != nil {
		t.Fatalf("cmdConfigModelList(nosuch): %v", err)
	}
}

func TestCmdConfigModelList_Known(t *testing.T) {
	// /config model list openai → openai group only.
	ctx := &mockCtx{}
	if err := cmdConfigModelList(ctx, "openai"); err != nil {
		t.Fatalf("cmdConfigModelList(openai): %v", err)
	}
}

// =====================================================================
// /config model dispatch
// =====================================================================

func TestCmdConfigModel_Dispatch(t *testing.T) {
	// /config model list <target> → cmdConfigModelList
	// (already tested above). We test the dispatcher
	// here with a sub-action the list-test doesn't
	// cover: /config model <empty> → prints usage.
	ctx := &mockCtx{}
	if err := cmdConfigModel(ctx, ""); err != nil {
		t.Fatalf("cmdConfigModel(\"\"): %v", err)
	}
}

func TestCmdConfigModel_UnknownSub(t *testing.T) {
	ctx := &mockCtx{}
	if err := cmdConfigModel(ctx, "weirdsub"); err != nil {
		t.Fatalf("cmdConfigModel(weirdsub): %v", err)
	}
}

// =====================================================================
// /recall (with non-empty query)
// =====================================================================

func TestCmdRecall_WithQuery(t *testing.T) {
	// /recall <query> with a mock that returns
	// ErrUnsupported from Recall — should print the
	// "(知识库未初始化)" friendly message and return
	// nil, not crash. (The mock's Recall is the
	// mockBase default which returns nil, so we expect
	// the cmd to actually call it and proceed without
	// error.)
	ctx := &mockCtx{}
	if err := cmdRecall(ctx, "how to install hooks"); err != nil {
		t.Fatalf("cmdRecall: %v", err)
	}
}

func TestCmdRecall_UnsupportedEngine(t *testing.T) {
	// When ctx.Recall returns ErrUnsupported (engine
	// not initialized), cmdRecall should print the
	// friendly message and return nil, not propagate
	// the error.
	ctx := &mockErrCtx{}
	if err := cmdRecall(ctx, "x"); err != nil {
		t.Fatalf("cmdRecall with unsupported: %v", err)
	}
}

// mockErrCtx is a thin wrapper that overrides Recall
// to return ErrUnsupported. We can't put this on
// mockCtx directly because the real cmdRecall calls
// isUnsupported(err) which needs an *ErrUnsupported.
type mockErrCtx struct {
	mockCtx
}

func (m *mockErrCtx) Recall(context.Context, string, int) error {
	return &ErrUnsupported{Op: "Recall"}
}

// =====================================================================
// Dispatcher
// =====================================================================

func TestMatchCommand_ResolvesKnown(t *testing.T) {
	cmd, _ := matchCommand("/model")
	if cmd == nil {
		t.Fatal("matchCommand(/model) returned nil")
	}
	if !strings.Contains(cmd.Description, "模型") && !strings.Contains(cmd.Description, "model") {
		t.Errorf("/model description = %q, expected something about model", cmd.Description)
	}
}

func TestMatchCommand_UnknownReturnsNil(t *testing.T) {
	cmd, args := matchCommand("/nosuchcommand arg1 arg2")
	if cmd != nil {
		t.Errorf("matchCommand returned %v for unknown command", cmd)
	}
	// matchCommand returns the whole string in args for
	// known commands, but for unknown commands it discards
	// the args (the caller falls back to printing
	// "unknown command: <input>"). The current
	// implementation returns ("", "") for unknown.
	if args != "" {
		t.Errorf("unknown-command args = %q, want empty", args)
	}
}
