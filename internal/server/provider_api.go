package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
)

// ProviderFull is the rich provider view returned by
// GET /api/v1/providers/:name. It carries the full Models list
// (each with its per-model settings: display name, max tokens
// context/output, capabilities) plus the legacy single-model
// fallback field, so a UI can render every model in one place.
//
// For the v0.9 "list all providers" view, see Handler.Providers —
// it returns a slimmer shape (name/model/protocol only) suitable
// for the model picker in the chat input.
type ProviderFull struct {
	Name      string               `json:"name"`
	Protocol  string               `json:"protocol"`
	BaseURL   string               `json:"base_url"`
	APIKey    string               `json:"api_key"`
	IsDefault bool                 `json:"is_default"`
	Models    []config.ModelConfig `json:"models"`
	// Legacy single-model form (kept for backward compat; equals
	// the first entry of Models when populated).
	Model string `json:"model,omitempty"`
}

const providerTestTimeout = 30 * time.Second

// TestProviderRequest 是 POST /api/v1/providers/:name/test 的可选请求体；
// Model 为空时使用供应商配置的默认模型。
// TestProviderRequest is the optional request body; an empty Model selects
// the provider's configured default model.
type TestProviderRequest struct {
	Model string `json:"model,omitempty"`
}

// TestProvider 向选中的供应商与模型发送一次无状态的 "sayhi" 小请求；
// 它不会创建会话，也不会修改供应商的默认模型。
// TestProvider sends a small, stateless "sayhi" request to the selected
// provider and model without creating a conversation or changing defaults.
func (h *Handler) TestProvider(c *gin.Context) {
	cfg := h.getCfg()
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}

	var req TestProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}

	providerName := c.Param("name")
	var provider *config.ProviderConfig
	for i := range cfg.LLM.Providers {
		if cfg.LLM.Providers[i].Name == providerName {
			provider = &cfg.LLM.Providers[i]
			break
		}
	}
	if provider == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found: " + providerName})
		return
	}

	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = provider.EffectiveModel()
	} else {
		found := false
		for _, model := range provider.AllModels() {
			if model.Name == modelName && model.EffectiveType() == config.ModelTypeLLM {
				found = true
				break
			}
		}
		if !found {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("model %q not found under provider %q", modelName, providerName)})
			return
		}
	}
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider has no model configured"})
		return
	}

	// 构造请求级隔离客户端，避免连接测试修改在线客户端或继承过大的模型输出额度；
	// provider 副本独占 Models 切片，并发设置请求期间配置快照保持只读。
	// Build an isolated request-local client so the check cannot mutate the live
	// client or inherit a large output allowance; the copied Models slice keeps
	// the atomic config snapshot read-only during concurrent settings requests.
	testProvider := *provider
	testProvider.Models = append([]config.ModelConfig(nil), provider.Models...)
	for i := range testProvider.Models {
		if testProvider.Models[i].Name == modelName {
			testProvider.Models[i].MaxTokensOutput = 64
		}
	}
	testLLM := cfg.LLM
	testLLM.Default = providerName
	testLLM.Providers = []config.ProviderConfig{testProvider}
	testLLM.MaxTokens = 64
	client, err := llm.NewClient(&testLLM)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create LLM client: " + err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), providerTestTimeout)
	defer cancel()
	started := time.Now()
	response, err := client.ChatCM(ctx, providerName, modelName, []llm.ChatMessage{{
		Role:        llm.RoleUser,
		Type:        llm.TypeText,
		Content:     "sayhi",
		SubmitToLLM: 1,
	}}, llm.ChatOptions{MaxTokens: 64})
	if err != nil {
		kind := llm.KindUnknown.String()
		status := http.StatusBadGateway
		var apiErr *llm.APIError
		if errors.As(err, &apiErr) {
			kind = apiErr.Kind.String()
			if apiErr.Kind == llm.KindTimeout {
				status = http.StatusGatewayTimeout
			}
		}
		c.JSON(status, gin.H{
			"error":      err.Error(),
			"error_kind": kind,
			"provider":   providerName,
			"model":      modelName,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"provider":   providerName,
		"model":      modelName,
		"response":   response,
		"elapsed_ms": time.Since(started).Milliseconds(),
	})
}

// GetProvider GET /api/v1/providers/:name — rich view of a single
// provider, including every model and its per-model configuration
// (max_tokens_context, max_tokens_output, display_name, capabilities).
func (h *Handler) GetProvider(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	for _, p := range h.getCfg().LLM.Providers {
		if p.Name != name {
			continue
		}
		models := p.AllModels()
		c.JSON(http.StatusOK, ProviderFull{
			Name:      p.Name,
			Protocol:  p.GetProtocol(),
			BaseURL:   p.EffectiveBaseURL(),
			APIKey:    p.APIKey,
			IsDefault: p.Name == h.getCfg().LLM.Default,
			Models:    models,
			Model:     p.EffectiveModel(),
		})
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "provider not found: " + name})
}

// UpdateModelRequest is the body of PUT /api/v1/providers/:name/models/:model.
//
// Per-model fields:
//   - display_name, description, max_tokens_context, max_tokens_output
//
// A zero value for a numeric field means "leave it as is" (the
// API treats 0 as "not provided"). Pass an explicit negative
// value (e.g. -1) to clear the field. DisplayName and
// Description accept empty string to clear.
type UpdateModelRequest struct {
	APIEndpoint      string                             `json:"api_endpoint,omitempty"`
	DisplayName      string                             `json:"display_name"`
	Description      string                             `json:"description"`
	MaxTokensContext int                                `json:"max_tokens_context"`
	MaxTokensOutput  int                                `json:"max_tokens_output"`
	Type             config.ModelType                   `json:"type,omitempty"`
	Generation       *config.MediaGenerationModelConfig `json:"generation,omitempty"`
	ClearAll         bool                               `json:"clear_all,omitempty"`
}

// UpdateModel PUT /api/v1/providers/:name/models/:model
//
// Replaces the editable fields of a model. The model Name is the
// URL path segment and cannot be changed (callers should
// delete-and-recreate to rename). Writes to
// ~/.p-chat/config.json and reloads the in-memory LLM client.
func (h *Handler) UpdateModel(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	providerName := c.Param("name")
	modelName := c.Param("model")
	var req UpdateModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	patch := config.ModelConfig{
		APIEndpoint:      req.APIEndpoint,
		DisplayName:      req.DisplayName,
		Description:      req.Description,
		MaxTokensContext: req.MaxTokensContext,
		MaxTokensOutput:  req.MaxTokensOutput,
		Type:             req.Type,
		Generation:       req.Generation,
	}
	updated, err := config.UpdateModel(providerName, modelName, patch, req.ClearAll)
	if err != nil {
		if strings.Contains(err.Error(), "used by generation default") {
			c.JSON(http.StatusConflict, gin.H{"error": "update failed: " + err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "update failed: " + err.Error()})
		return
	}
	_ = updated
	h.reloadAfterConfigChange()
	c.JSON(http.StatusOK, gin.H{
		"ok":       true,
		"provider": providerName,
		"model":    modelName,
	})
}

// SetCapabilitiesRequest is the body of PATCH
// /api/v1/providers/:name/models/:model/capabilities.
//
// All fields are optional — pass `{}` to clear. The server
// validates ThinkingEffort before writing.
type SetCapabilitiesRequest struct {
	ThinkingEffort  string              `json:"thinking_effort,omitempty"`
	ContextWindow   int                 `json:"context_window,omitempty"`
	SupportsVision  bool                `json:"supports_vision,omitempty"`
	SupportsAudio   bool                `json:"supports_audio,omitempty"`
	InputModalities *[]config.MediaKind `json:"input_modalities,omitempty"`
}

// SetCapabilities PATCH /api/v1/providers/:name/models/:model/capabilities
//
// Replaces the model entry's Capabilities block. Writes to
// ~/.p-chat/config.json and reloads the in-memory LLM client.
func (h *Handler) SetCapabilities(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	model := c.Param("model")
	var req SetCapabilitiesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.InputModalities != nil {
		seen := make(map[config.MediaKind]struct{}, len(*req.InputModalities))
		normalized := make([]config.MediaKind, 0, len(*req.InputModalities))
		for _, kind := range *req.InputModalities {
			if !kind.IsValid() {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unsupported input modality %q", kind)})
				return
			}
			if _, exists := seen[kind]; exists {
				continue
			}
			seen[kind] = struct{}{}
			normalized = append(normalized, kind)
		}
		req.InputModalities = &normalized
	}
	if err := config.SetModelCapabilities(name, model, config.Capabilities{
		ThinkingEffort:  config.ThinkingEffort(req.ThinkingEffort),
		ContextWindow:   req.ContextWindow,
		SupportsVision:  req.SupportsVision,
		SupportsAudio:   req.SupportsAudio,
		InputModalities: req.InputModalities,
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	c.JSON(http.StatusOK, gin.H{"ok": true, "provider": name, "model": model})
}

// upstreamModel mirrors one entry from the OpenAI GET /v1/models response.
type upstreamModel struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// upstreamModelsResponse is the OpenAI /v1/models response shape.
type upstreamModelsResponse struct {
	Data []upstreamModel `json:"data"`
}

// UpstreamModelsItem is the slim item returned to the frontend.
type UpstreamModelsItem struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Added   bool   `json:"added"` // already exists in this provider
}

// ProbeUpstreamModelsRequest is the body of
// POST /api/v1/providers/probe-models. Used by the "add
// provider" dialog before a provider row exists — the UI
// supplies ephemeral base_url + api_key to list upstream
// models for the default-model picker.
type ProbeUpstreamModelsRequest struct {
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	Protocol string `json:"protocol"`
}

// fetchUpstreamModelList GETs {baseURL}/models with the given
// API key and maps the OpenAI-shaped response into
// UpstreamModelsItem. existing marks ids already configured
// locally (nil = none).
func fetchUpstreamModelList(baseURL, apiKey string, existing map[string]bool) ([]UpstreamModelsItem, int, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("base_url is required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("api_key is required")
	}

	url := baseURL + "/models"
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	// Anthropic-compatible gateways often accept either; set
	// both so official Anthropic /v1/models works too.
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, http.StatusBadGateway, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}

	var parsed upstreamModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("parse upstream response: %w", err)
	}

	if existing == nil {
		existing = map[string]bool{}
	}
	out := make([]UpstreamModelsItem, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		out = append(out, UpstreamModelsItem{
			ID:      m.ID,
			Created: m.Created,
			OwnedBy: m.OwnedBy,
			Added:   existing[m.ID],
		})
	}
	return out, http.StatusOK, nil
}

func defaultProbeBaseURL(protocol, baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL != "" {
		return baseURL
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anthropic":
		return "https://api.anthropic.com/v1"
	default:
		return "https://api.openai.com/v1"
	}
}

// ProbeUpstreamModels POST /api/v1/providers/probe-models
// Lists models from an upstream endpoint using credentials
// supplied in the request body (no saved provider required).
func (h *Handler) ProbeUpstreamModels(c *gin.Context) {
	var req ProbeUpstreamModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	baseURL := defaultProbeBaseURL(req.Protocol, req.BaseURL)
	out, status, err := fetchUpstreamModelList(baseURL, req.APIKey, nil)
	if err != nil {
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"models": out, "base_url": baseURL})
}

// FetchUpstreamModels GET /api/v1/providers/:name/upstream-models
// Calls the upstream provider's GET {BaseURL}/models with the stored API key
// and returns the model list so the user can pick which to add.
func (h *Handler) FetchUpstreamModels(c *gin.Context) {
	name := c.Param("name")
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}

	var provider *config.ProviderConfig
	for i := range h.getCfg().LLM.Providers {
		if h.getCfg().LLM.Providers[i].Name == name {
			provider = &h.getCfg().LLM.Providers[i]
			break
		}
	}
	if provider == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found: " + name})
		return
	}
	if provider.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider has no API key configured"})
		return
	}

	existing := map[string]bool{}
	for _, m := range provider.AllModels() {
		existing[m.Name] = true
	}

	out, status, err := fetchUpstreamModelList(provider.EffectiveBaseURL(), provider.APIKey, existing)
	if err != nil {
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"models": out})
}
