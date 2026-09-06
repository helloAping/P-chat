package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/skill"
	"github.com/p-chat/pchat/internal/tool"
)

type recordingSkillManager struct {
	load func(skill.LoadRequest) (skill.LoadedSkill, error)
}

func (m *recordingSkillManager) Catalog(context.Context, skill.CatalogQuery) (skill.Catalog, error) {
	return skill.Catalog{}, nil
}

func (m *recordingSkillManager) Load(_ context.Context, request skill.LoadRequest) (skill.LoadedSkill, error) {
	return m.load(request)
}

func (m *recordingSkillManager) Apply(context.Context, skill.ChangeRequest) (skill.ChangeResult, error) {
	return skill.ChangeResult{}, errors.New("not implemented")
}

func TestLoadActiveSkillsEmitsStartBeforeInstructionsBecomeAvailable(t *testing.T) {
	var events []ChatStreamChunk
	manager := &recordingSkillManager{load: func(request skill.LoadRequest) (skill.LoadedSkill, error) {
		if len(events) != 1 || events[0].SkillStatus != "start" || events[0].SkillName != request.Name {
			t.Fatalf("load called before visible start event: %+v", events)
		}
		return skill.LoadedSkill{
			Name:    request.Name,
			Context: "DOMAIN INSTRUCTIONS",
			Skills: []skill.LoadedEntry{{SkillInfo: skill.SkillInfo{
				Name: request.Name, Scope: skill.ScopeGlobalManaged, Path: "x/SKILL.md",
			}}},
		}, nil
	}}
	a := &Agent{skillManager: manager}

	contextText, err := a.loadActiveSkills(context.Background(), ChatRequest{
		ActiveSkills: []string{"lark-doc", "lark-doc"},
	}, func(event ChatStreamChunk) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if contextText != "DOMAIN INSTRUCTIONS" {
		t.Fatalf("context = %q", contextText)
	}
	if len(events) != 2 || events[0].SkillStatus != "start" || events[1].SkillStatus != "ready" {
		t.Fatalf("events = %+v, want one start/ready pair", events)
	}
	if events[0].Message != "当前调用 Skill：lark-doc" {
		t.Fatalf("announcement = %q", events[0].Message)
	}
}

func TestLoadActiveSkillsEmitsStructuredError(t *testing.T) {
	var events []ChatStreamChunk
	a := &Agent{skillManager: &recordingSkillManager{load: func(skill.LoadRequest) (skill.LoadedSkill, error) {
		return skill.LoadedSkill{}, errors.New("dependency missing")
	}}}

	_, err := a.loadActiveSkills(context.Background(), ChatRequest{ActiveSkills: []string{"broken"}}, func(event ChatStreamChunk) {
		events = append(events, event)
	})
	if err == nil || !strings.Contains(err.Error(), "dependency missing") {
		t.Fatalf("err = %v", err)
	}
	if len(events) != 2 || events[1].SkillStatus != "error" || events[1].SkillError == "" {
		t.Fatalf("events = %+v", events)
	}
}

func TestPartsAccumulatorSkillLifecycle(t *testing.T) {
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{SkillName: "lark-doc", SkillStatus: "start", Message: "当前调用 Skill：lark-doc"})
	acc.update(ChatStreamChunk{
		SkillName: "lark-doc", SkillStatus: "ready", SkillScope: "global_managed",
		SkillSource: "skills/lark-doc/SKILL.md", SkillDependencies: []string{"lark-shared"},
	})

	parts := acc.snapshot()
	if len(parts) != 1 || parts[0].Kind != "skill" || parts[0].Name != "lark-doc" || parts[0].Status != "ready" {
		t.Fatalf("parts = %+v", parts)
	}
	if parts[0].Scope != "global_managed" || len(parts[0].Dependencies) != 1 || parts[0].Dependencies[0] != "lark-shared" {
		t.Fatalf("skill metadata = %+v", parts[0])
	}
}

func TestParseSkillLoadCall(t *testing.T) {
	name, ok := parseSkillLoadCall("skill", `{"action":"load","name":"lark-doc"}`)
	if !ok || name != "lark-doc" {
		t.Fatalf("parseSkillLoadCall = %q, %v", name, ok)
	}
	if _, ok := parseSkillLoadCall("skill", `{"action":"inspect","name":"lark-doc"}`); ok {
		t.Fatal("inspect must not be treated as a Skill invocation")
	}
}

func TestSkillLoadCacheRunsOnceAndReturnsCompactDuplicate(t *testing.T) {
	cache := newSkillLoadCache()
	var calls atomic.Int32
	start := make(chan struct{})
	release := make(chan struct{})
	load := func() (*tool.CallResult, error) {
		calls.Add(1)
		close(start)
		<-release
		return &tool.CallResult{
			Content:         "FULL SKILL BODY",
			SkillInvocation: &tool.SkillInvocation{Name: "lark-doc", Status: "ready"},
		}, nil
	}

	var first, second *tool.CallResult
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		first, _ = cache.do("lark-doc", load)
	}()
	<-start
	go func() {
		defer wait.Done()
		second, _ = cache.do("lark-doc", load)
	}()
	close(release)
	wait.Wait()

	if calls.Load() != 1 {
		t.Fatalf("load calls = %d, want 1", calls.Load())
	}
	if first.Content != "FULL SKILL BODY" {
		t.Fatalf("first result = %q", first.Content)
	}
	if strings.Contains(second.Content, "FULL SKILL BODY") || !strings.Contains(second.Content, "already loaded") {
		t.Fatalf("duplicate result = %q", second.Content)
	}
}

func TestSkillLoadCacheReusesExplicitlyActivatedSkill(t *testing.T) {
	cache := newSkillLoadCache()
	cache.markLoaded("lark-doc")
	called := false
	result, err := cache.do("lark-doc", func() (*tool.CallResult, error) {
		called = true
		return &tool.CallResult{Content: "FULL SKILL BODY"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("explicitly activated Skill must not be loaded again by the tool")
	}
	if strings.Contains(result.Content, "FULL SKILL BODY") || !strings.Contains(result.Content, "already loaded") {
		t.Fatalf("reuse result = %q", result.Content)
	}
}

func TestSkillLoadFailurePartsArePersisted(t *testing.T) {
	store, err := memory.OpenAt(filepath.Join(t.TempDir(), "memory.db"), 20)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	acc := newPartsAccumulator()
	acc.update(ChatStreamChunk{Phase: "skill", SkillName: "broken", SkillStatus: "start", Message: "当前调用 Skill：broken"})
	acc.update(ChatStreamChunk{Phase: "skill", SkillName: "broken", SkillStatus: "error", SkillError: "dependency missing"})

	persistAssistant(sessionID, store, llm.ChatMessage{Role: llm.RoleAssistant, Type: llm.TypeText}, "", acc, 0, 0, "")
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	_, metadata, _ := store.GetChatMessagesWithMetaFor(sessionID, 20)
	if len(metadata) != 1 || !strings.Contains(metadata[0], `\"kind\":\"skill\"`) || !strings.Contains(metadata[0], `\"status\":\"error\"`) {
		t.Fatalf("persisted metadata = %+v", metadata)
	}
}

func TestHostRuntimeExplainsLarkCLISkillImport(t *testing.T) {
	prompt := buildHostRuntimeBlock(`D:\projects\demo`)
	for _, expected := range []string{"source_cli `lark-cli`", "omit name", "skill_manage"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("host runtime prompt missing %q: %s", expected, prompt)
		}
	}
}
