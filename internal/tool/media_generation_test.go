package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/generation"
)

type recordingGenerationExecutor struct {
	calls int
	req   generation.Request
}

func (e *recordingGenerationExecutor) Generate(_ context.Context, req generation.Request) (generation.Result, error) {
	e.calls++
	e.req = req
	return generation.Result{JobID: "job-1", Status: generation.StatusQueued}, nil
}

func TestMediaGenerationToolHardGateBlocksDisabledOperation(t *testing.T) {
	executor := &recordingGenerationExecutor{}
	ctx := generation.WithAccess(context.Background(), generation.Access{
		Enabled:  map[config.GenerationOperation]bool{},
		Targets:  map[config.GenerationOperation]config.GenerationModelTarget{},
		Executor: executor,
	})

	result, err := handleGenerateVideo(ctx, json.RawMessage(`{"operation":"text_to_video","prompt":"a sunrise"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.Status != CallStatusBlocked || !strings.Contains(result.Content, "已在当前会话关闭") {
		t.Fatalf("disabled result = %#v", result)
	}
	if executor.calls != 0 {
		t.Fatalf("disabled operation called executor %d time(s)", executor.calls)
	}
}

func TestMediaGenerationToolEnabledWithoutTargetDoesNotExecute(t *testing.T) {
	executor := &recordingGenerationExecutor{}
	ctx := generation.WithAccess(context.Background(), generation.Access{
		Enabled:  map[config.GenerationOperation]bool{config.GenerationTextToVideo: true},
		Targets:  map[config.GenerationOperation]config.GenerationModelTarget{},
		Executor: executor,
	})

	result, err := handleGenerateVideo(ctx, json.RawMessage(`{"operation":"text_to_video","prompt":"a sunrise"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content, "没有可用模型") {
		t.Fatalf("missing-target result = %#v", result)
	}
	if executor.calls != 0 {
		t.Fatalf("missing target called executor %d time(s)", executor.calls)
	}
}

func TestTextGenerationOperationRejectsInjectedMediaReference(t *testing.T) {
	access := generation.Access{
		Enabled: map[config.GenerationOperation]bool{config.GenerationTextToVideo: true},
		Targets: map[config.GenerationOperation]config.GenerationModelTarget{
			config.GenerationTextToVideo: {Provider: "provider", Model: "model"},
		},
		Dispatches: map[config.GenerationOperation]generation.Dispatch{
			config.GenerationTextToVideo: {Target: config.GenerationModelTarget{Provider: "provider", Model: "model"}},
		},
		Executor: &recordingGenerationExecutor{},
	}
	ctx := generation.WithAccess(context.Background(), access)
	result, err := handleGenerateVideo(ctx, json.RawMessage(`{"operation":"text_to_video","prompt":"animate","input_refs":["upload-id"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CallStatusBlocked || !strings.Contains(result.Content, "不接受媒体") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestMediaGenerationToolPassesOnlyReferencesToExecutor(t *testing.T) {
	executor := &recordingGenerationExecutor{}
	target := config.GenerationModelTarget{Provider: "minimax", Model: "MiniMax-H3"}
	ctx := WithSessionID(context.Background(), "session-1")
	ctx = generation.WithAccess(ctx, generation.Access{
		Enabled: map[config.GenerationOperation]bool{config.GenerationImageToVideo: true},
		Targets: map[config.GenerationOperation]config.GenerationModelTarget{config.GenerationImageToVideo: target},
		Dispatches: map[config.GenerationOperation]generation.Dispatch{
			config.GenerationImageToVideo: {Target: target},
		},
		Executor: executor,
	})

	result, err := handleGenerateVideo(ctx, json.RawMessage(`{"operation":"image_to_video","prompt":"make it move","prompt_mode":"assist","input_refs":["upl-image-1"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || executor.calls != 1 {
		t.Fatalf("result/calls = %#v / %d", result, executor.calls)
	}
	if executor.req.SessionID != "session-1" || executor.req.Target != target {
		t.Fatalf("request routing = %#v", executor.req)
	}
	if len(executor.req.InputRefs) != 1 || executor.req.InputRefs[0] != "upl-image-1" {
		t.Fatalf("input refs = %#v", executor.req.InputRefs)
	}
}

func TestMediaGenerationToolRejectsOperationFromAnotherTool(t *testing.T) {
	executor := &recordingGenerationExecutor{}
	ctx := generation.WithAccess(context.Background(), generation.Access{
		Enabled: map[config.GenerationOperation]bool{config.GenerationTextToSpeech: true},
		Targets: map[config.GenerationOperation]config.GenerationModelTarget{
			config.GenerationTextToSpeech: {Provider: "minimax", Model: "speech-2.8-hd"},
		},
		Executor: executor,
	})

	result, err := handleGenerateImage(ctx, json.RawMessage(`{"operation":"text_to_speech","prompt":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content, "不属于 generate_image") {
		t.Fatalf("wrong-tool result = %#v", result)
	}
	if executor.calls != 0 {
		t.Fatalf("wrong tool called executor %d time(s)", executor.calls)
	}
}

func TestMediaGenerationToolRejectsUntrustedVendorOptionsBeforeExecution(t *testing.T) {
	executor := &recordingGenerationExecutor{}
	target := config.GenerationModelTarget{Provider: "provider", Model: "video-model"}
	ctx := generation.WithAccess(context.Background(), generation.Access{
		Enabled: map[config.GenerationOperation]bool{config.GenerationTextToVideo: true},
		Targets: map[config.GenerationOperation]config.GenerationModelTarget{config.GenerationTextToVideo: target},
		Dispatches: map[config.GenerationOperation]generation.Dispatch{
			config.GenerationTextToVideo: {Target: target},
		},
		Executor: executor,
	})

	result, err := handleGenerateVideo(ctx, json.RawMessage(`{"operation":"text_to_video","prompt":"sunrise","options":{"callback_url":"https://example.invalid"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CallStatusBlocked || !strings.Contains(result.Content, "已拒绝") {
		t.Fatalf("unexpected result: %#v", result)
	}
	if executor.calls != 0 {
		t.Fatalf("rejected options called executor %d time(s)", executor.calls)
	}
}

func TestMediaGenerationToolBoundsInputReferenceCountBeforeExecution(t *testing.T) {
	executor := &recordingGenerationExecutor{}
	target := config.GenerationModelTarget{Provider: "provider", Model: "video-model"}
	ctx := generation.WithAccess(context.Background(), generation.Access{
		Enabled: map[config.GenerationOperation]bool{config.GenerationImageToVideo: true},
		Targets: map[config.GenerationOperation]config.GenerationModelTarget{config.GenerationImageToVideo: target},
		Dispatches: map[config.GenerationOperation]generation.Dispatch{
			config.GenerationImageToVideo: {Target: target},
		},
		Executor: executor,
	})
	result, err := handleGenerateVideo(ctx, json.RawMessage(`{"operation":"image_to_video","prompt":"move","input_refs":["one","two"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CallStatusBlocked || !strings.Contains(result.Content, "最多接受 1 个") {
		t.Fatalf("unexpected result: %#v", result)
	}
	if executor.calls != 0 {
		t.Fatalf("excess references called executor %d time(s)", executor.calls)
	}
}

func TestRegisteredMediaGenerationToolsUseOperationTimeout(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltin(registry)
	for _, candidate := range registry.List() {
		switch candidate.Name {
		case "generate_image", "generate_video", "generate_audio":
			if timeout := candidate.EffectivePolicy().Timeout(); timeout != 0 {
				t.Fatalf("%s has outer timeout %s; executor operation timeout must own the deadline", candidate.Name, timeout)
			}
		}
	}
}
