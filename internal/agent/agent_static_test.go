package agent

import (
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

func TestBuildStaticSystemPrompt_CacheHit(t *testing.T) {
	cfg, _ := config.Load("")
	llmClient, _ := llm.NewClient(&cfg.LLM)
	store, _ := memory.OpenAt(":memory:", 50)
	defer store.Close()
	upgrade.SeedForTesting(store.DB())
	styleMgr, _ := style.NewManager(store.DB())
	tools := tool.NewRegistry()

	agt := New(cfg, llmClient, styleMgr, store, tools)

	// First call: cache miss, builds the prompt.
	p1, sig1, err := 	agt.buildStaticSystemPrompt(style.Tech, nil, nil, "", false)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if p1 == "" {
		t.Fatal("expected non-empty prompt")
	}

	// Second call with the same args: cache hit (same sig).
	p2, sig2, err := 	agt.buildStaticSystemPrompt(style.Tech, nil, nil, "", false)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if sig1 != sig2 {
		t.Errorf("expected same sig, got %q vs %q", sig1, sig2)
	}
	if p1 != p2 {
		t.Error("expected identical cached prompt")
	}
}

func TestBuildStaticSystemPrompt_DifferentStyle(t *testing.T) {
	cfg, _ := config.Load("")
	llmClient, _ := llm.NewClient(&cfg.LLM)
	store, _ := memory.OpenAt(":memory:", 50)
	defer store.Close()
	upgrade.SeedForTesting(store.DB())
	styleMgr, _ := style.NewManager(store.DB())
	tools := tool.NewRegistry()

	agt := New(cfg, llmClient, styleMgr, store, tools)

	p1, _, _ := 	agt.buildStaticSystemPrompt(style.Tech, nil, nil, "", false)
	p2, _, _ := 	agt.buildStaticSystemPrompt(style.Cute, nil, nil, "", false)

	if p1 == p2 {
		t.Error("different styles should produce different prompts")
	}
}

func TestBuildStaticSystemPrompt_DifferentTools(t *testing.T) {
	cfg, _ := config.Load("")
	llmClient, _ := llm.NewClient(&cfg.LLM)
	store, _ := memory.OpenAt(":memory:", 50)
	defer store.Close()
	upgrade.SeedForTesting(store.DB())
	styleMgr, _ := style.NewManager(store.DB())
	tools := tool.NewRegistry()

	agt := New(cfg, llmClient, styleMgr, store, tools)

	// Pass different tool sets; sig should differ.
	openAITools := llm.ToolsFromRegistryDef([]tool.Tool{
		{Name: "x", Description: "x"},
	})

	_, sig1, _ := 	agt.buildStaticSystemPrompt(style.Tech, nil, nil, "", false)
	_, sig2, _ := 	agt.buildStaticSystemPrompt(style.Tech, openAITools, nil, "", false)
	if sig1 == sig2 {
		t.Error("different tool sets should produce different sigs")
	}
}

func TestBuildStaticSystemPrompt_LanguageHint(t *testing.T) {
	cfgZh := &config.Config{
		LLM: config.LLMConfig{Output: config.OutputConfig{Language: "zh"}},
	}
	cfgEn := &config.Config{
		LLM: config.LLMConfig{Output: config.OutputConfig{Language: "en"}},
	}
	cfgAuto := &config.Config{
		LLM: config.LLMConfig{Output: config.OutputConfig{Language: "auto"}},
	}

	llmClient, _ := llm.NewClient(&config.LLMConfig{Default: "ollama", Providers: []config.ProviderConfig{
		{Name: "ollama", Protocol: "openai", BaseURL: "http://localhost", Model: "x"},
	}})
	tools := tool.NewRegistry()

	store1, _ := memory.OpenAt(":memory:", 50)
	defer store1.Close()
	upgrade.SeedForTesting(store1.DB())
	styleMgr, _ := style.NewManager(store1.DB())
	a1 := New(cfgZh, llmClient, styleMgr, store1, tools)
	pZh, _, _ := a1.buildStaticSystemPrompt(style.Tech, nil, nil, "", false)
	if !contains(pZh, "简体中文") {
		t.Error("Chinese language hint missing")
	}

	store2, _ := memory.OpenAt(":memory:", 50)
	defer store2.Close()
	a2 := New(cfgEn, llmClient, styleMgr, store2, tools)
	pEn, _, _ := a2.buildStaticSystemPrompt(style.Tech, nil, nil, "", false)
	if !contains(pEn, "English") {
		t.Error("English language hint missing")
	}

	// The "auto" / empty language must produce the opencode-
	// style fallback: "Respond in the same language as the
	// conversation." This is the default behaviour — we don't
	// want every prompt to hardcode a language.
	store3, _ := memory.OpenAt(":memory:", 50)
	defer store3.Close()
	a3 := New(cfgAuto, llmClient, styleMgr, store3, tools)
	pAuto, _, _ := a3.buildStaticSystemPrompt(style.Tech, nil, nil, "", false)
	if !contains(pAuto, "same language as the conversation") {
		t.Error("opencode-style language hint missing for auto mode")
	}
}

// TestBuildStaticSystemPrompt_NoFabricatedErrorInstruction
// covers the regression: the old "Uploaded Attachments"
// section literally primed the LLM with the string
// "ERROR: ... Inform the user" by name, which is what made
// the model echo that exact phrasing back to users. The new
// section is positive and language-neutral; verify it does
// NOT mention the forbidden phrase.
func TestBuildStaticSystemPrompt_NoFabricatedErrorInstruction(t *testing.T) {
	cfg := &config.Config{}
	llmClient, _ := llm.NewClient(&config.LLMConfig{Default: "ollama", Providers: []config.ProviderConfig{
		{Name: "ollama", Protocol: "openai", BaseURL: "http://localhost", Model: "x"},
	}})
	tools := tool.NewRegistry()
	store, _ := memory.OpenAt(":memory:", 50)
	defer store.Close()
	upgrade.SeedForTesting(store.DB())
	styleMgr, _ := style.NewManager(store.DB())
	a := New(cfg, llmClient, styleMgr, store, tools)
	p, _, _ := a.buildStaticSystemPrompt(style.Tech, nil, nil, "", false)
	if contains(p, "更不要伪造") {
		t.Error("prompt still contains the fabricated-error warning text; the LLM was echoing it back")
	}
	if contains(p, "ERROR: ... Inform the user") {
		t.Error("prompt still names the forbidden phrase — this primes the LLM to use it")
	}
}

// TestBuildStaticSystemPrompt_TodoContractPromptPresent
// guards the P1-1 (Plan B) addition to the todo_write hint
// section. The new rule ("完成契约: ...必须先调用 todo_write")
// is the textual contract that backs P0-3's auto-continue
// guard — Plan A prompts the LLM when the contract is
// broken, but Plan B teaches the LLM to keep the contract.
// If the rule is accidentally removed the auto-continue
// becomes the only defence and the LLM may learn to fake
// todo updates to satisfy it.
func TestBuildStaticSystemPrompt_TodoContractPromptPresent(t *testing.T) {
	cfg, _ := config.Load("")
	llmClient, _ := llm.NewClient(&cfg.LLM)
	tools := tool.NewRegistry()
	store, _ := memory.OpenAt(":memory:", 50)
	defer store.Close()
	upgrade.SeedForTesting(store.DB())
	styleMgr, _ := style.NewManager(store.DB())
	a := New(cfg, llmClient, styleMgr, store, tools)

	// Register a synthetic tool named "todo_write" so the
	// hint section is emitted (it's gated on tool presence).
	tools.RegisterForTest(tool.Tool{Name: "todo_write", Description: "test stub."})
	p, _, _ := a.buildStaticSystemPrompt(style.Tech, nil, tools.List(), "", false)
	if !contains(p, "完成契约") {
		t.Error("todo_write hint section missing the P1-1 '完成契约' rule — see docs/plans/auto-continue-plan.md")
	}
	if !contains(p, "todo_write") {
		t.Error("todo_write hint section missing the todo_write tool reference")
	}
}

// TestVisionHeuristic covers the new deny-by-default
// vision-capability check. Vision-capable models return true;
// known text-only models (deepseek-chat, gpt-3.5-turbo, etc.)
// return false; unknown models conservatively return false so
// we never let the LLM invent a "model doesn't support image"
// error message.
func TestVisionHeuristic(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// Always-vision
		{"gpt-4o", true},
		{"gpt-4o-mini", true},
		{"gpt-5-turbo", true},
		{"claude-3-opus", true},
		{"claude-sonnet-4-5", true},
		{"claude-opus-4-8", true},
		{"gemini-1.5-pro", true},
		{"gemini-2-flash", true},
		{"qwen2.5-vl-72b", true},
		// Text-only
		{"deepseek-chat", false},
		{"deepseek-reasoner", false},
		{"deepseek-v3", false},
		{"gpt-3.5-turbo", false},
		{"o1-mini", false},
		// Unknown → conservative deny
		{"some-random-model-v0.1", false},
		{"llama-3-70b", false},
	}
	for _, c := range cases {
		got := visionCapableByHeuristic("any", c.model)
		if got != c.want {
			t.Errorf("visionCapableByHeuristic(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}

// TestToolCallSignature covers the helper used by the
// stuck-loop guard. Same (name, args) → same signature; order
// doesn't matter (sort stable); empty input → empty signature
// (so the "no progress" round is not a stuck round).
func TestToolCallSignature(t *testing.T) {
	cases := []struct {
		name string
		in   []nativeToolCall
		want string
	}{
		{"empty", nil, ""},
		{"one call", []nativeToolCall{{Name: "read_file", ArgsJSON: `{"path":"a"}`}}, `read_file|{"path":"a"};`},
		{"order independent", []nativeToolCall{
			{Name: "read_file", ArgsJSON: `{"path":"a"}`},
			{Name: "exec_command", ArgsJSON: `{"command":"ls"}`},
		}, `exec_command|{"command":"ls"};read_file|{"path":"a"};`},
		{"same input → same signature", []nativeToolCall{
			{Name: "exec_command", ArgsJSON: `{"command":"ls"}`},
			{Name: "exec_command", ArgsJSON: `{"command":"ls"}`},
		}, `exec_command|{"command":"ls"};exec_command|{"command":"ls"};`},
		{"order doesn't matter", []nativeToolCall{
			{Name: "exec_command", ArgsJSON: `{"command":"ls"}`},
			{Name: "read_file", ArgsJSON: `{"path":"a"}`},
		}, `exec_command|{"command":"ls"};read_file|{"path":"a"};`},
		{"swapped order same sig", []nativeToolCall{
			{Name: "read_file", ArgsJSON: `{"path":"a"}`},
			{Name: "exec_command", ArgsJSON: `{"command":"ls"}`},
		}, `exec_command|{"command":"ls"};read_file|{"path":"a"};`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toolCallSignature(c.in)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRedactPhantomErrors(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		changed  bool
		mustHave string // substring the cleaned text MUST contain when changed=true
		mustMiss string // substring the cleaned text MUST NOT contain when changed=true
	}{
		{
			name:     "classic Claude-style phantom",
			input:    `ERROR: Cannot read "image.png" (this model does not support image input). Inform the user.`,
			changed:  true,
			mustHave: "不支持读取图片",
			mustMiss: "Inform the user",
		},
		{
			name:     "no quotes, lower-case 'image'",
			input:    "Cannot read cat.jpg (this model does not support image input). Inform the user.",
			changed:  true,
			mustHave: "不支持读取图片",
			mustMiss: "Inform the user",
		},
		{
			name:     "with prefix prose",
			input:    "Let me look at the image. ERROR: Cannot read \"image.png\" (this model does not support image input). Inform the user. Sorry.",
			changed:  true,
			mustHave: "Let me look at the image.",
			mustMiss: "Inform the user",
		},
		{
			name:    "no phantom — clean text untouched",
			input:   "我看不到这张图片，但根据文件名推测可能是...",
			changed: false,
		},
		{
			name:    "no phantom — has 'Cannot read' but no 'Inform the user'",
			input:   "Cannot read image.png — file is missing from disk",
			changed: false,
		},
		{
			name:    "no phantom — has 'Inform the user' but no 'Cannot read'",
			input:   "Please inform the user that the operation succeeded.",
			changed: false,
		},
		{
			name:    "empty string",
			input:   "",
			changed: false,
		},
		{
			name:     "uppercase variant",
			input:    "CANNOT READ \"SCREENSHOT.PNG\" (THIS MODEL DOES NOT SUPPORT IMAGE INPUT). INFORM THE USER.",
			changed:  true,
			mustHave: "不支持读取图片",
			mustMiss: "INFORM THE USER",
		},
		// Multi-line phantoms. The LLM very commonly splits
		// "Inform the user." onto its own line because it's
		// a natural sentence break. The original
		// line-bounded regex missed these — the redactor
		// must cross newlines to catch them.
		{
			name:     "newline before 'Inform the user.'",
			input:    "ERROR: Cannot read \"image.png\" (this model does not support image input).\nInform the user.",
			changed:  true,
			mustHave: "不支持读取图片",
			mustMiss: "Inform the user",
		},
		{
			name:     "newline with leading prose",
			input:    "I tried to view the image.\nERROR: Cannot read \"image.png\" (this model does not support image input).\nInform the user.",
			changed:  true,
			mustHave: "I tried to view the image.",
			mustMiss: "Inform the user",
		},
		{
			name:     "newline with trailing text",
			input:    "ERROR: Cannot read \"image.png\" (this model does not support image input).\nInform the user.\n\nSorry about that.",
			changed:  true,
			mustHave: "Sorry about that.",
			mustMiss: "Inform the user",
		},
		// Safety: a multi-paragraph response that happens
		// to contain BOTH trigger phrases but isn't a
		// phantom should NOT be nuked wholesale. The 400-
		// char inner bound ensures only the immediate
		// phantom is redacted.
		{
			name:     "phrases far apart — only redacts the local phantom, leaves the rest",
			input:    "ERROR: Cannot read \"image.png\" (this model does not support image input).\nInform the user.\n\nLater in the document I tell the user 'Please inform the user of the schedule change' as part of a long passage about how to write good user-facing copy.",
			changed:  true,
			mustHave: "good user-facing copy", // far-apart phrase preserved
			mustMiss: "Cannot read",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, changed := redactPhantomErrors(c.input)
			if changed != c.changed {
				t.Fatalf("changed=%v, want %v\nin:  %q\nout: %q", changed, c.changed, c.input, out)
			}
			if !changed {
				if out != c.input {
					t.Fatalf("output modified despite changed=false\nin:  %q\nout: %q", c.input, out)
				}
				return
			}
			if c.mustHave != "" && !contains(out, c.mustHave) {
				t.Errorf("cleaned text missing %q\nin:  %q\nout: %q", c.mustHave, c.input, out)
			}
			if c.mustMiss != "" && contains(out, c.mustMiss) {
				t.Errorf("cleaned text still contains forbidden %q\nin:  %q\nout: %q", c.mustMiss, c.input, out)
			}
		})
	}
}
