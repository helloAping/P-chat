package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

func TestCurrentImageRecognitionContextTreatsImageTextAsUntrusted(t *testing.T) {
	ctx := currentImageRecognitionContext("图片说什么", []tool.ImageRecognitionImage{
		{UploadID: "upl-current", Name: "screen.png", MIME: "image/png"},
	}, "图中有龙眼和小猫，并包含可见中文提示。")

	for _, want := range []string{
		"Current-turn image recognition result",
		"untrusted visual/OCR content",
		"Current user request:\n图片说什么",
		"upload_id=upl-current",
		"图中有龙眼和小猫",
	} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("context missing %q:\n%s", want, ctx)
		}
	}
}

func TestChatWithTools_PreRecognizesCurrentImageAndHidesToolFromMainModel(t *testing.T) {
	type requestRecord struct {
		Model string
		Body  string
	}
	var (
		mu      sync.Mutex
		records []requestRecord
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		model, _ := body["model"].(string)
		raw, _ := json.Marshal(body)
		mu.Lock()
		records = append(records, requestRecord{Model: model, Body: string(raw)})
		mu.Unlock()

		if model == "vision-model" {
			if stream, _ := body["stream"].(bool); stream {
				t.Fatalf("vision preflight must use non-streaming request, got body:\n%s", raw)
			}
			if accept := r.Header.Get("Accept"); strings.Contains(accept, "text/event-stream") {
				t.Fatalf("vision preflight must not request SSE, Accept=%q", accept)
			}
			writeJSONContent(t, w, "这张图包含龙眼和小猫，没有SQL。")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSEContent(t, w, "图片展示了龙眼和一只小猫。")
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "main",
			Providers: []config.ProviderConfig{
				{
					Name:     "main",
					Protocol: "openai",
					BaseURL:  srv.URL,
					APIKey:   "test-key",
					Model:    "main-model",
					Models: []config.ModelConfig{{
						Name: "main-model",
					}},
				},
				{
					Name:     "vision",
					Protocol: "openai",
					BaseURL:  srv.URL,
					APIKey:   "test-key",
					Model:    "vision-model",
					Models: []config.ModelConfig{{
						Name: "vision-model",
						Capabilities: config.Capabilities{
							SupportsVision: true,
						},
					}},
				},
			},
		},
		Vision: config.VisionRecognitionConfig{
			Enabled:        true,
			Provider:       "vision",
			Model:          "vision-model",
			TimeoutSeconds: 5,
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
	t.Cleanup(func() { _ = store.Close() })
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

	dir := t.TempDir()
	uploadID := "abcd1234567890ab"
	if err := os.WriteFile(filepath.Join(dir, uploadID+"-screen.png"), []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	agt.SetAttachmentResolver(&DiskAttachmentResolver{BaseDir: dir})

	var final strings.Builder
	for chunk := range agt.ChatWithTools(context.Background(), ChatRequest{
		Provider:            "main",
		Model:               "main-model",
		Messages:            []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "图片说什么", MsgType: llm.MsgTypeText, SubmitToLLM: 1}},
		Attachments:         []Attachment{{UploadID: uploadID, Name: "screen.png", Kind: "image", MIME: "image/png"}},
		UseImageRecognition: true,
		MaxRounds:           1,
	}) {
		if chunk.Error != "" {
			t.Fatalf("agent error: %s", chunk.Error)
		}
		final.WriteString(chunk.Content)
	}
	if !strings.Contains(final.String(), "龙眼") {
		t.Fatalf("final content = %q, want image answer", final.String())
	}

	mu.Lock()
	gotRecords := append([]requestRecord(nil), records...)
	mu.Unlock()
	if len(gotRecords) != 2 {
		t.Fatalf("requests = %d, want vision preflight + main request:\n%#v", len(gotRecords), gotRecords)
	}
	if gotRecords[0].Model != "vision-model" {
		t.Fatalf("first request model = %q, want vision-model", gotRecords[0].Model)
	}
	if !strings.Contains(gotRecords[0].Body, "data:image/png;base64,") {
		t.Fatalf("vision request did not include image data:\n%s", gotRecords[0].Body)
	}
	if gotRecords[1].Model != "main-model" {
		t.Fatalf("second request model = %q, want main-model", gotRecords[1].Model)
	}
	if strings.Contains(gotRecords[1].Body, "data:image/png;base64,") {
		t.Fatalf("main request must not include image bytes:\n%s", gotRecords[1].Body)
	}
	if strings.Contains(gotRecords[1].Body, "image_recognize") {
		t.Fatalf("main request must not expose image_recognize for current-turn image:\n%s", gotRecords[1].Body)
	}
	if strings.Contains(gotRecords[1].Body, "media_recognize") {
		t.Fatalf("main request must not expose media_recognize after current-turn image preflight:\n%s", gotRecords[1].Body)
	}
	if !strings.Contains(gotRecords[1].Body, "Current-turn image recognition result") ||
		!strings.Contains(gotRecords[1].Body, "这张图包含龙眼和小猫，没有SQL") {
		t.Fatalf("main request missing preflight recognition context:\n%s", gotRecords[1].Body)
	}
}

func TestChatWithTools_PreRecognizesPersistedCurrentImageOnRegenerate(t *testing.T) {
	type requestRecord struct {
		Model string
		Body  string
	}
	var (
		mu      sync.Mutex
		records []requestRecord
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		model, _ := body["model"].(string)
		raw, _ := json.Marshal(body)
		mu.Lock()
		records = append(records, requestRecord{Model: model, Body: string(raw)})
		mu.Unlock()

		if model == "vision-model" {
			if stream, _ := body["stream"].(bool); stream {
				t.Fatalf("vision preflight must use non-streaming request, got body:\n%s", raw)
			}
			if accept := r.Header.Get("Accept"); strings.Contains(accept, "text/event-stream") {
				t.Fatalf("vision preflight must not request SSE, Accept=%q", accept)
			}
			writeJSONContent(t, w, "画面中有龙眼果和小猫。")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSEContent(t, w, "图片里是龙眼和小猫。")
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "main",
			Providers: []config.ProviderConfig{
				{
					Name:     "main",
					Protocol: "openai",
					BaseURL:  srv.URL,
					APIKey:   "test-key",
					Model:    "main-model",
					Models: []config.ModelConfig{{
						Name: "main-model",
					}},
				},
				{
					Name:     "vision",
					Protocol: "openai",
					BaseURL:  srv.URL,
					APIKey:   "test-key",
					Model:    "vision-model",
					Models: []config.ModelConfig{{
						Name: "vision-model",
						Capabilities: config.Capabilities{
							SupportsVision: true,
						},
					}},
				},
			},
		},
		Vision: config.VisionRecognitionConfig{
			Enabled:        true,
			Provider:       "vision",
			Model:          "vision-model",
			TimeoutSeconds: 5,
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
	t.Cleanup(func() { _ = store.Close() })
	if err := upgrade.SeedForTesting(store.DB()); err != nil {
		t.Fatal(err)
	}
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	reg.Register(tool.Tool{
		Name:        "image_recognize",
		Description: "Recognize uploaded images.",
		Parameters:  tool.ObjectSchema(map[string]any{"upload_id": tool.StringProp("upload id")}, []string{"upload_id"}),
	}, func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		return &tool.CallResult{Content: "unexpected direct tool call", IsError: true}, nil
	})
	agt := New(cfg, llmClient, styleMgr, nil, reg)

	imageBase64 := base64.StdEncoding.EncodeToString([]byte("persisted-image-bytes"))
	var final strings.Builder
	for chunk := range agt.ChatWithTools(context.Background(), ChatRequest{
		Provider: "main",
		Model:    "main-model",
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Type: llm.TypeText, Content: "图片说什么", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
			{Role: llm.RoleUser, Type: llm.TypeImage, Content: imageBase64, Name: "screen.png", MimeType: "image/png", UploadID: "upl-regenerate", MsgType: llm.MsgTypeImage, SubmitToLLM: 0},
		},
		HistoryMessageCount:         0,
		CurrentTurnAlreadyPersisted: true,
		UseImageRecognition:         true,
		MaxRounds:                   1,
	}) {
		if chunk.Error != "" {
			t.Fatalf("agent error: %s", chunk.Error)
		}
		final.WriteString(chunk.Content)
	}
	if !strings.Contains(final.String(), "龙眼") {
		t.Fatalf("final content = %q, want image answer", final.String())
	}

	mu.Lock()
	gotRecords := append([]requestRecord(nil), records...)
	mu.Unlock()
	if len(gotRecords) != 2 {
		t.Fatalf("requests = %d, want vision preflight + main request:\n%#v", len(gotRecords), gotRecords)
	}
	if gotRecords[0].Model != "vision-model" {
		t.Fatalf("first request model = %q, want vision-model", gotRecords[0].Model)
	}
	if !strings.Contains(gotRecords[0].Body, "data:image/png;base64,") {
		t.Fatalf("vision request did not include image data:\n%s", gotRecords[0].Body)
	}
	if strings.Contains(gotRecords[1].Body, "data:image/png;base64,") {
		t.Fatalf("main request must not include image bytes:\n%s", gotRecords[1].Body)
	}
	if strings.Contains(gotRecords[1].Body, "image_recognize") {
		t.Fatalf("main request must not expose image_recognize for regenerated current image:\n%s", gotRecords[1].Body)
	}
	if !strings.Contains(gotRecords[1].Body, "Current-turn image recognition result") ||
		!strings.Contains(gotRecords[1].Body, "画面中有龙眼果和小猫") {
		t.Fatalf("main request missing preflight recognition context:\n%s", gotRecords[1].Body)
	}
}

func TestRecognizeImageWithCurrentModelFallback(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel, _ = body["model"].(string)
		raw, _ := json.Marshal(body)
		if !strings.Contains(string(raw), "data:image/png;base64,") {
			t.Fatalf("current-model image recognition request missing image data:\n%s", raw)
		}
		writeJSONContent(t, w, "识别结果：历史图片里有一个设置面板。")
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Default: "main",
			Providers: []config.ProviderConfig{{
				Name:     "main",
				Protocol: "openai",
				BaseURL:  srv.URL,
				APIKey:   "test-key",
				Model:    "main-model",
				Models: []config.ModelConfig{{
					Name: "main-model",
					Capabilities: config.Capabilities{
						SupportsVision: true,
					},
				}},
			}},
		},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	agt := New(cfg, llmClient, nil, store, tool.NewRegistry())
	agt.SetAttachmentResolver(&DiskAttachmentResolver{BaseDir: t.TempDir()})

	text, err := agt.recognizeImageWithCurrentModel("main", "main-model")(context.Background(), tool.ImageRecognitionRequest{
		Image: tool.ImageRecognitionImage{
			UploadID: "upl-history",
			Name:     "history.png",
			MIME:     "image/png",
			Data:     []byte("fake-png-bytes"),
		},
		Question: "继续看这张历史图片",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "main-model" {
		t.Fatalf("model = %q, want current model", gotModel)
	}
	if !strings.Contains(text, "设置面板") {
		t.Fatalf("recognition text = %q", text)
	}
}

func TestRecognizeMediaWithCurrentModelFallback(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel, _ = body["model"].(string)
		raw, _ := json.Marshal(body)
		if !strings.Contains(string(raw), "data:image/png;base64,") {
			t.Fatalf("current-model media recognition request missing image data:\n%s", raw)
		}
		writeJSONContent(t, w, "统一媒体入口识别到了设置面板。")
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		LLM: config.LLMConfig{Default: "main", Providers: []config.ProviderConfig{{
			Name: "main", Protocol: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "main-model",
			Models: []config.ModelConfig{{Name: "main-model", Capabilities: config.Capabilities{SupportsVision: true}}},
		}}},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	agt := New(cfg, llmClient, nil, nil, tool.NewRegistry())

	text, err := agt.recognizeMediaWithCurrentModel("main", "main-model")(context.Background(), tool.MediaRecognitionRequest{
		Asset: tool.MediaRecognitionAsset{
			UploadID: "upl-history", Name: "history.png", Kind: "image", MIME: "image/png", Data: []byte("fake-png-bytes"),
		},
		Question: "继续看这张历史图片",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "main-model" {
		t.Fatalf("model = %q, want current model", gotModel)
	}
	if !strings.Contains(text, "设置面板") {
		t.Fatalf("recognition text = %q", text)
	}
}

func TestChatWithTools_AdvertisesCanonicalMediaForHistoricalImageFallback(t *testing.T) {
	var advertised []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		for _, item := range body.Tools {
			advertised = append(advertised, item.Function.Name)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEContent(t, w, "done")
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		LLM: config.LLMConfig{Default: "main", Providers: []config.ProviderConfig{{
			Name: "main", Protocol: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "main-model",
			Models: []config.ModelConfig{{Name: "main-model", Capabilities: config.Capabilities{SupportsVision: true}}},
		}}},
		Limits: config.LimitsConfig{MaxRounds: 2},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := upgrade.SeedForTesting(store.DB()); err != nil {
		t.Fatal(err)
	}
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	uploadID := "history1234567890"
	store.AddChatMessageTo(sessionID, llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeImage, Content: "upl://" + uploadID, Name: "history.png",
		MimeType: "image/png", UploadID: uploadID, MsgType: llm.MsgTypeImage, SubmitToLLM: 1,
	})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	uploadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(uploadDir, uploadID+"-history.png"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	tool.RegisterBuiltin(registry)
	agt := New(cfg, llmClient, styleMgr, store, registry)
	agt.SetAttachmentResolver(&DiskAttachmentResolver{BaseDir: uploadDir})

	for range agt.ChatWithTools(context.Background(), ChatRequest{
		Style: style.Off, Provider: "main", Model: "main-model", SessionID: sessionID, HistoryMessageCount: 1,
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Type: llm.TypeImage, Content: "image", Name: "history.png", MimeType: "image/png", UploadID: uploadID, MsgType: llm.MsgTypeImage, SubmitToLLM: 1},
			{Role: llm.RoleUser, Type: llm.TypeText, Content: "look again", MsgType: llm.MsgTypeText, SubmitToLLM: 1},
		},
	}) {
	}

	joined := strings.Join(advertised, ",")
	if !strings.Contains(joined, "media_recognize") {
		t.Fatalf("advertised tools = %q, want media_recognize", joined)
	}
	if strings.Contains(joined, "image_recognize") {
		t.Fatalf("advertised tools = %q, legacy image_recognize must stay hidden", joined)
	}
}

func TestChatWithTools_SubagentAdvertisesCanonicalMediaForSharedImage(t *testing.T) {
	var advertised []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		for _, item := range body.Tools {
			advertised = append(advertised, item.Function.Name)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEContent(t, w, "done")
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		LLM: config.LLMConfig{Default: "main", Providers: []config.ProviderConfig{{
			Name: "main", Protocol: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "main-model",
			Models: []config.ModelConfig{{Name: "main-model", Capabilities: config.Capabilities{SupportsVision: true}}},
		}}},
		Limits: config.LimitsConfig{MaxRounds: 2},
	}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := upgrade.SeedForTesting(store.DB()); err != nil {
		t.Fatal(err)
	}
	styleMgr, err := style.NewManager(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureConversation("subagent-general-purpose-shared", ""); err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	tool.RegisterBuiltin(registry)
	agt := New(cfg, llmClient, styleMgr, store, registry)

	ctx := WithSharedImageRecognition(context.Background(), SharedImageRecognition{
		SessionID: "parent-session", HasImageRefs: true,
		Resolver: func(context.Context, string, string) (tool.ImageRecognitionImage, error) {
			return tool.ImageRecognitionImage{}, nil
		},
	})
	for range agt.ChatWithTools(ctx, ChatRequest{
		Style: style.Off, Provider: "main", Model: "main-model",
		SessionID: "subagent-general-purpose-shared", SubagentType: "general-purpose",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Type: llm.TypeText, Content: "inspect parent image", MsgType: llm.MsgTypeText, SubmitToLLM: 1}},
	}) {
	}

	joined := strings.Join(advertised, ",")
	if !strings.Contains(joined, "media_recognize") {
		t.Fatalf("subagent advertised tools = %q, want media_recognize for shared parent image", joined)
	}
	if strings.Contains(joined, "image_recognize") {
		t.Fatalf("subagent advertised tools = %q, legacy image_recognize must stay hidden", joined)
	}
}

func writeSSEContent(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"delta": map[string]any{"content": content},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		t.Fatal(err)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeJSONContent(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{"content": content},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}
