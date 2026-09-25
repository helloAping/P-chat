package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/generation"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/tool"
)

func TestBuildMediaToolContext_RecognitionPreservesResult(t *testing.T) {
	ctx, ok := buildMediaToolContext(
		"session-1",
		"99",
		"call_1",
		"media_recognize",
		`{"input_ref":"upl_1","input_refs":["upl_2","upl_1"],"question":"识别第一列","context_refs":["mctx_old"]}`,
		&tool.CallResult{Content: "col1\n1\n2", Summary: "Recognized 1 image"},
	)
	if !ok {
		t.Fatal("expected media context")
	}
	if ctx.Kind != memory.MediaContextKindRecognition || ctx.ToolName != "media_recognize" {
		t.Fatalf("unexpected context kind/tool: %+v", ctx)
	}
	if strings.Join(ctx.InputRefs, ",") != "upl_1,upl_2" {
		t.Fatalf("input refs = %#v", ctx.InputRefs)
	}
	if ctx.Prompt != "识别第一列" || ctx.ResultText != "col1\n1\n2" || ctx.ContextRefs[0] != "mctx_old" {
		t.Fatalf("recognition context lost data: %+v", ctx)
	}
}

func TestBuildMediaToolContext_GenerationCapturesOutputRefs(t *testing.T) {
	payload, err := json.Marshal(generation.Result{
		JobID:  "job_1",
		Status: generation.StatusSucceeded,
		Assets: []generation.Asset{{
			ID:   "asset_img_1",
			Kind: config.MediaImage,
			Name: "chart.png",
		}},
		Message: "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, ok := buildMediaToolContext(
		"session-1",
		"99",
		"call_2",
		"generate_image",
		`{"operation":"text_to_image","prompt":"make a chart","input_refs":["upl_1"]}`,
		&tool.CallResult{Content: string(payload), Summary: "text_to_image 已提交，状态 succeeded"},
	)
	if !ok {
		t.Fatal("expected media context")
	}
	if ctx.Kind != memory.MediaContextKindGeneration || strings.Join(ctx.OutputRefs, ",") != "asset_img_1" {
		t.Fatalf("generation context = %+v", ctx)
	}
	if !strings.Contains(ctx.Summary, "status=succeeded") || !strings.Contains(ctx.StructuredJSON, "asset_img_1") {
		t.Fatalf("generation summary/json missing output details: %+v", ctx)
	}
}

func TestRecordMediaToolContextUsesLastUserAsGroup(t *testing.T) {
	store, err := memory.OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	store.AddChatMessageToWithID(sessionID, llm.ChatMessage{
		Role:        llm.RoleUser,
		Type:        llm.TypeText,
		Content:     "识别第一列",
		MsgType:     llm.MsgTypeText,
		SubmitToLLM: 1,
	}, 12345)
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	a := &Agent{store: store}
	a.recordMediaToolContext(
		sessionID,
		"",
		0,
		"call_1",
		"media_recognize",
		`{"input_ref":"upl_1","question":"识别第一列"}`,
		&tool.CallResult{Content: "第一列\n10\n20", Summary: "recognized"},
	)
	contexts, err := store.RecentMediaContexts(sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 {
		t.Fatalf("contexts len = %d, want 1", len(contexts))
	}
	if contexts[0].RegenGroupID != "12345" {
		t.Fatalf("regen group = %q, want last user id", contexts[0].RegenGroupID)
	}
}

func TestResolveMediaContextForToolExpandsGraph(t *testing.T) {
	store, err := memory.OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMediaContext(memory.MediaContext{
		ID:         "mctx_parent",
		SessionID:  sessionID,
		Kind:       memory.MediaContextKindRecognition,
		ToolName:   "media_recognize",
		ResultText: "first column",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMediaContext(memory.MediaContext{
		ID:          "mctx_child",
		SessionID:   sessionID,
		Kind:        memory.MediaContextKindRecognition,
		ToolName:    "media_recognize",
		ContextRefs: []string{"mctx_parent"},
		ResultText:  "second column",
	}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{store: store}
	expansion, err := a.resolveMediaContextForTool(context.Background(), sessionID, tool.MediaContextResolveRequest{
		ContextRefs:    []string{"mctx_child"},
		ContextMode:    tool.MediaContextModeContinue,
		PreferredKinds: []string{memory.MediaContextKindRecognition},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(expansion.IDs, ",") != "mctx_parent,mctx_child" {
		t.Fatalf("ids = %#v", expansion.IDs)
	}
	if !strings.Contains(expansion.Text, "first column") || !strings.Contains(expansion.Text, "second column") {
		t.Fatalf("expansion text missing graph content:\n%s", expansion.Text)
	}
}

func TestRecordCurrentImageRecognitionContextCreatesPendingContext(t *testing.T) {
	store, err := memory.OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	store.AddChatMessageToWithID(sessionID, llm.ChatMessage{
		Role:        llm.RoleUser,
		Type:        llm.TypeText,
		Content:     "识别图片",
		MsgType:     llm.MsgTypeText,
		SubmitToLLM: 1,
	}, 123)
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	a := &Agent{store: store}
	a.recordCurrentImageRecognitionContext(sessionID, "", "识别图片", []tool.ImageRecognitionImage{{UploadID: "upl_img", Name: "img.png", MIME: "image/png"}}, "图片里有表格")
	contexts, err := store.RecentMediaContexts(sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 {
		t.Fatalf("contexts len = %d", len(contexts))
	}
	if contexts[0].MessageID != 0 || contexts[0].RegenGroupID != "123" || contexts[0].InputRefs[0] != "upl_img" {
		t.Fatalf("pending current image context = %+v", contexts[0])
	}
}

func TestBuildRecentMediaContextBlockPreservesRecognitionLines(t *testing.T) {
	store, err := memory.OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMediaContext(memory.MediaContext{
		SessionID:  sessionID,
		Kind:       memory.MediaContextKindRecognition,
		ToolName:   "media_recognize",
		InputRefs:  []string{"upl_1"},
		Prompt:     "识别第一列",
		ResultText: "姓名,金额\nAlice,10\nBob,20",
		Summary:    "recognized table",
	}); err != nil {
		t.Fatal(err)
	}

	a := &Agent{store: store}
	block := a.buildRecentMediaContextBlock(sessionID)
	if !strings.Contains(block, "context_id=") || !strings.Contains(block, "重新调用识别工具") {
		t.Fatalf("block missing reuse boundary instructions:\n%s", block)
	}
	if !strings.Contains(block, "     姓名,金额\n     Alice,10\n     Bob,20") {
		t.Fatalf("block did not preserve recognition lines:\n%s", block)
	}
}
