package generation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
)

type stubInputResolver struct {
	source InputSource
	err    error
}

func (r stubInputResolver) ResolveInput(_ context.Context, _, _ string, _ config.MediaKind) (InputSource, error) {
	return r.source, r.err
}

func TestHTTPExecutorImmediateImageMaterializesBase64(t *testing.T) {
	imageBytes := []byte("generated-image")
	var received map[string]any
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(imageBytes)}},
		})
	}))
	defer server.Close()

	store := NewLocalAssetStore(t.TempDir(), "/api/v1/generated")
	executor := NewHTTPExecutor(stubInputResolver{source: InputSource{
		ID: "upload-1", Kind: config.MediaImage, MIMEType: "image/png", Name: "source.png", Data: []byte("source"),
	}}, store)
	executor.Client = server.Client()

	result, err := executor.Generate(context.Background(), Request{
		SessionID: "session-1", Operation: config.GenerationImageToImage,
		Target: config.GenerationModelTarget{Provider: "volc", Model: "image-model"},
		Prompt: "make it blue", InputRefs: []string{"upload-1"},
		Dispatch: Dispatch{
			Target: config.GenerationModelTarget{Provider: "volc", Model: "image-model"},
			Vendor: "volcengine", BaseURL: server.URL,
			OperationConfig: config.GenerationOperationConfig{Endpoint: "/generate", TimeoutSeconds: 10},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if received["model"] != "image-model" || received["prompt"] != "make it blue" {
		t.Fatalf("unexpected payload: %#v", received)
	}
	if image, _ := received["image"].(string); !strings.HasPrefix(image, "data:image/png;base64,") {
		t.Fatalf("image input was not materialized internally: %#v", received["image"])
	}
	if result.Status != StatusSucceeded || len(result.Assets) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Assets[0].ID == "" || !strings.HasPrefix(result.Assets[0].URL, "/api/v1/generated/") {
		t.Fatalf("asset was not localized: %#v", result.Assets[0])
	}
	path, _, err := store.Resolve(result.Assets[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(imageBytes) {
		t.Fatalf("stored bytes = %q", got)
	}
}

func TestHTTPExecutorPollsVideoTaskAndStoresRemoteAsset(t *testing.T) {
	var polls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/tasks":
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "task-1", "status": "queued"})
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/task-1":
			if polls.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "running"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "video_url": server.URL + "/asset.mp4"})
		case r.URL.Path == "/asset.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := NewLocalAssetStore(t.TempDir(), "/api/v1/generated")
	executor := NewHTTPExecutor(nil, store)
	executor.Client = server.Client()
	executor.PollInterval = time.Millisecond

	result, err := executor.Generate(context.Background(), Request{
		SessionID: "session-1", Operation: config.GenerationTextToVideo,
		Target: config.GenerationModelTarget{Provider: "minimax", Model: "video-model"},
		Prompt: "waves on a beach",
		Dispatch: Dispatch{
			Target: config.GenerationModelTarget{Provider: "minimax", Model: "video-model"},
			Vendor: "minimax", BaseURL: server.URL,
			OperationConfig: config.GenerationOperationConfig{
				Endpoint: "/tasks", QueryEndpoint: "/tasks/{task_id}", TimeoutSeconds: 10,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if polls.Load() < 2 || result.JobID != "task-1" || result.Status != StatusSucceeded {
		t.Fatalf("polling result = %#v, polls=%d", result, polls.Load())
	}
	if len(result.Assets) != 1 || result.Assets[0].Kind != config.MediaVideo {
		t.Fatalf("unexpected assets: %#v", result.Assets)
	}
}

func TestHTTPExecutorDoesNotForwardProviderCredentialsToAssetURL(t *testing.T) {
	var assetAuthorization, assetAPIKey string
	var providerServer *httptest.Server
	providerServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/result.png" {
			assetAuthorization = r.Header.Get("Authorization")
			assetAPIKey = r.Header.Get("x-api-key")
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("image"))
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer provider-secret" {
			t.Errorf("provider Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"url": providerServer.URL + "/result.png"}}})
	}))
	defer providerServer.Close()

	executor := NewHTTPExecutor(nil, NewLocalAssetStore(t.TempDir(), "/generated"))
	result, err := executor.Generate(context.Background(), Request{
		SessionID: "session-1", Operation: config.GenerationTextToImage,
		Target: config.GenerationModelTarget{Provider: "openai", Model: "image-model"}, Prompt: "a lake",
		Dispatch: Dispatch{
			Target: config.GenerationModelTarget{Provider: "openai", Model: "image-model"},
			Vendor: "openai", BaseURL: providerServer.URL, APIKey: "provider-secret",
			OperationConfig: config.GenerationOperationConfig{Endpoint: "/images", TimeoutSeconds: 10},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Assets) != 1 {
		t.Fatalf("assets = %#v", result.Assets)
	}
	if assetAuthorization != "" || assetAPIKey != "" {
		t.Fatalf("credentials leaked to asset server: Authorization=%q x-api-key=%q", assetAuthorization, assetAPIKey)
	}
}

func TestValidateRemoteAssetURLBlocksPrivateCrossOriginTargets(t *testing.T) {
	if err := validateRemoteAssetURL(context.Background(), "http://127.0.0.1/private.png", "https://api.example.test/create"); err == nil {
		t.Fatal("cross-origin loopback asset URL must be rejected")
	}
	if err := validateRemoteAssetURL(context.Background(), "http://127.0.0.1:1234/result.png", "http://127.0.0.1:1234/create"); err != nil {
		t.Fatalf("same-origin custom provider asset should remain available: %v", err)
	}
}

func TestPinnedAssetClientOnlyDialsValidatedHosts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("image"))
	}))
	defer server.Close()

	client, _, err := newPinnedAssetClient(context.Background(), server.Client(), server.URL+"/result.png", server.URL+"/create")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL + "/result.png")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.DialContext == nil {
		t.Fatalf("asset client transport is not safely pinned: %#v", client.Transport)
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "example.com:80"); err == nil || !strings.Contains(err.Error(), "unvalidated host") {
		t.Fatalf("unvalidated dial should be rejected before DNS/network access, got %v", err)
	}
}

func TestHTTPExecutorLimitsConcurrentGenerationRequests(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var enteredOnce sync.Once
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		enteredOnce.Do(func() { close(entered) })
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString([]byte("image"))}},
		})
	}))
	defer server.Close()

	executor := NewHTTPExecutor(nil, NewLocalAssetStore(t.TempDir(), "/generated"))
	executor.Client = server.Client()
	request := Request{
		SessionID: "session-1", Operation: config.GenerationTextToImage,
		Target: config.GenerationModelTarget{Provider: "custom", Model: "image-model"}, Prompt: "lake",
		Dispatch: Dispatch{
			Target: config.GenerationModelTarget{Provider: "custom", Model: "image-model"}, BaseURL: server.URL,
			OperationConfig: config.GenerationOperationConfig{Endpoint: "/generate", TimeoutSeconds: 10},
		},
	}
	firstResult := make(chan error, 1)
	go func() {
		_, err := executor.Generate(context.Background(), request)
		firstResult <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first generation request did not reach provider")
	}
	secondCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := executor.Generate(secondCtx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second generation should time out while waiting for the executor slot, got %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider received %d concurrent requests, want 1", got)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-firstResult; err != nil {
		t.Fatalf("first generation failed: %v", err)
	}
}

func TestHTTPExecutorRejectsAsyncTaskWithoutQueryEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "task-1", "status": "queued"})
	}))
	defer server.Close()

	executor := NewHTTPExecutor(nil, NewLocalAssetStore(t.TempDir(), "/generated"))
	_, err := executor.Generate(context.Background(), Request{
		SessionID: "session-1", Operation: config.GenerationTextToVideo,
		Target: config.GenerationModelTarget{Provider: "custom", Model: "video-model"}, Prompt: "a lake",
		Dispatch: Dispatch{
			Target: config.GenerationModelTarget{Provider: "custom", Model: "video-model"}, BaseURL: server.URL,
			OperationConfig: config.GenerationOperationConfig{Endpoint: "/tasks", TimeoutSeconds: 10},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "no query_endpoint") {
		t.Fatalf("expected missing query endpoint error, got %v", err)
	}
}

func TestHTTPExecutorRejectsUnauthorizedInputBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	executor := NewHTTPExecutor(stubInputResolver{err: errors.New("input is not owned by session")}, NewLocalAssetStore(t.TempDir(), "/generated"))
	executor.Client = server.Client()

	_, err := executor.Generate(context.Background(), Request{
		SessionID: "session-1", Operation: config.GenerationImageToVideo,
		Target: config.GenerationModelTarget{Provider: "volc", Model: "video-model"},
		Prompt: "animate", InputRefs: []string{"foreign-id"},
		Dispatch: Dispatch{
			Target: config.GenerationModelTarget{Provider: "volc", Model: "video-model"}, BaseURL: server.URL,
			OperationConfig: config.GenerationOperationConfig{Endpoint: "/tasks", TimeoutSeconds: 10},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "owned") {
		t.Fatalf("expected ownership error, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("vendor called %d times", calls.Load())
	}
}

func TestCollectAssetCandidatesSupportsPluralURLsAndHexAudio(t *testing.T) {
	images := collectAssetCandidates(map[string]any{
		"data": map[string]any{"image_urls": []any{"https://example.test/one.png", "https://example.test/two.png"}},
	}, config.MediaImage)
	if len(images) != 2 || images[0].URL == "" || images[1].URL == "" {
		t.Fatalf("plural image URLs were not collected: %#v", images)
	}

	audio := collectAssetCandidates(map[string]any{"data": map[string]any{"audio_file": "000102ff"}}, config.MediaAudio)
	if len(audio) != 1 || string(audio[0].Data) != string([]byte{0, 1, 2, 255}) {
		t.Fatalf("hex audio was not decoded correctly: %#v", audio)
	}
}

func TestMaterializeResponseBoundsAssetCountBeforeWriting(t *testing.T) {
	values := make([]any, 0, maxAssetsPerResponse+1)
	for i := 0; i < maxAssetsPerResponse+1; i++ {
		values = append(values, "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte{byte(i + 1)}))
	}
	store := NewLocalAssetStore(t.TempDir(), "/generated")
	executor := NewHTTPExecutor(nil, store)
	_, err := executor.materializeResponse(context.Background(), "session-1", config.MediaImage, map[string]any{
		"image_urls": values,
	}, "https://api.example.test/create")
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("expected bounded asset count error, got %v", err)
	}
	entries, readErr := os.ReadDir(store.Dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected response wrote files: %#v", entries)
	}
}

func TestLocalAssetStoreEnforcesDirectoryQuota(t *testing.T) {
	store := NewLocalAssetStore(t.TempDir(), "/generated")
	store.MaxBytes = 4
	if _, err := store.Save("session-1", config.MediaImage, "image/png", "large.png", []byte("12345")); err == nil {
		t.Fatal("asset larger than store quota must be rejected")
	}
}

func TestFirstStringForKeysPreservesCallerPriority(t *testing.T) {
	response := map[string]any{
		"id":   "generic-id",
		"data": map[string]any{"task_id": "preferred-task-id"},
	}
	if got := firstStringForKeys(response, "task_id", "id"); got != "preferred-task-id" {
		t.Fatalf("firstStringForKeys() = %q", got)
	}
}

func TestBuildVendorPayloadUsesVendorSpecificPromptShapes(t *testing.T) {
	target := config.GenerationModelTarget{Provider: "provider", Model: "model"}
	volc := buildVendorPayload(Request{
		Operation: config.GenerationTextToVideo, Target: target, Prompt: "move slowly",
		Dispatch: Dispatch{Vendor: "volcengine"},
	}, nil)
	if _, exists := volc["prompt"]; exists {
		t.Fatalf("Volcengine video payload must not contain top-level prompt: %#v", volc)
	}
	content, ok := volc["content"].([]map[string]any)
	if !ok || len(content) != 1 || content[0]["text"] != "move slowly" {
		t.Fatalf("Volcengine video content = %#v", volc["content"])
	}

	minimax := buildVendorPayload(Request{
		Operation: config.GenerationTextToSpeech, Target: target, Prompt: "你好",
		Dispatch: Dispatch{Vendor: "minimax"},
	}, nil)
	if minimax["text"] != "你好" {
		t.Fatalf("MiniMax TTS text = %#v", minimax)
	}
	if _, exists := minimax["prompt"]; exists {
		t.Fatalf("MiniMax TTS payload must not contain prompt: %#v", minimax)
	}

	adapterOverride := buildVendorPayload(Request{
		Operation: config.GenerationTextToSpeech, Target: target, Prompt: "adapter text",
		Dispatch: Dispatch{Vendor: "custom", Adapter: "minimax"},
	}, nil)
	if adapterOverride["text"] != "adapter text" {
		t.Fatalf("model-level adapter override was ignored: %#v", adapterOverride)
	}

	minimaxImage := buildVendorPayload(Request{
		Operation: config.GenerationImageToImage, Target: target, Prompt: "keep the character",
		Options: map[string]any{"count": int64(2)}, Dispatch: Dispatch{Vendor: "minimax"},
	}, []InputSource{{MIMEType: "image/png", Data: []byte("reference")}})
	references, ok := minimaxImage["subject_reference"].([]map[string]any)
	if !ok || len(references) != 1 {
		t.Fatalf("MiniMax image references = %#v", minimaxImage)
	}
	imageFile, _ := references[0]["image_file"].(string)
	if !strings.HasPrefix(imageFile, "data:image/png;base64,") || minimaxImage["n"] != int64(2) {
		t.Fatalf("MiniMax image payload = %#v", minimaxImage)
	}

	volcImage := buildVendorPayload(Request{
		Operation: config.GenerationImageToImage, Target: target, Prompt: "blend references",
		Dispatch: Dispatch{Vendor: "volcengine"},
	}, []InputSource{{MIMEType: "image/png", Data: []byte("one")}, {MIMEType: "image/png", Data: []byte("two")}})
	if images, ok := volcImage["image"].([]string); !ok || len(images) != 2 {
		t.Fatalf("Volcengine multi-image payload = %#v", volcImage)
	}
}

func TestCollectAssetCandidatesSupportsMiniMaxImageBase64(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("image"))
	assets := collectAssetCandidates(map[string]any{"data": map[string]any{"image_base64": []any{encoded}}}, config.MediaImage)
	if len(assets) != 1 || string(assets[0].Data) != "image" {
		t.Fatalf("MiniMax base64 assets = %#v", assets)
	}
}
