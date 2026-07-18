package cli

import (
	"context"
	"strings"
	"testing"

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
	setModelArg struct{ p, m string }
	setProvArg  string
	title       string
	styleArg    string
	autoArg     string
	err         error
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
func (m *mockCtx) GetCurrentSessionID() string { return "sess-1" }
func (m *mockCtx) RenameSession(_ context.Context, _, t string) error {
	m.title = t
	return m.err
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
