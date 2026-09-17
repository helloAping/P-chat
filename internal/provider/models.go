package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/requestheader"
)

// ModelListRequest contains all inputs needed to discover provider models.
type ModelListRequest struct {
	ProviderID      string
	StrategyVariant string
	Protocol        string
	BaseURL         string
	APIKey          string
	CustomHeaders   map[string]string
	Existing        map[string]bool
	Capability      Capability
	Operation       string
}

// ModelListItem is one upstream or fallback model option returned to the UI.
type ModelListItem struct {
	ID              string `json:"id"`
	DisplayName     string `json:"display_name,omitempty"`
	Created         int64  `json:"created"`
	OwnedBy         string `json:"owned_by"`
	Added           bool   `json:"added"`
	Source          string `json:"source,omitempty"`
	DefaultEndpoint string `json:"default_endpoint,omitempty"`
}

// ModelListResult describes a completed model discovery attempt.
type ModelListResult struct {
	Models          []ModelListItem `json:"models"`
	BaseURL         string          `json:"base_url,omitempty"`
	Endpoint        string          `json:"endpoint,omitempty"`
	Source          string          `json:"source,omitempty"`
	DefaultEndpoint string          `json:"default_endpoint,omitempty"`
	RemoteError     string          `json:"error,omitempty"`
	ManualAllowed   bool            `json:"manual_allowed,omitempty"`
}

// ListModels lists models using the provider strategy preset. Custom and other
// manual-capable strategies can return a non-fatal RemoteError with an empty
// model list so configuration remains possible when /models is unavailable.
func ListModels(ctx context.Context, req ModelListRequest) (ModelListResult, int, error) {
	preset, ok := Get(req.ProviderID)
	if !ok {
		preset, _ = Get("custom")
	}

	result := ModelListResult{
		BaseURL:         strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		Source:          "remote",
		DefaultEndpoint: defaultModelEndpoint(preset, req.Protocol),
		ManualAllowed:   modelListAllowsManual(preset.ModelList.Mode),
	}
	result.Endpoint = result.DefaultEndpoint

	if result.Endpoint == "" {
		result.Endpoint = "/models"
	}
	if result.BaseURL == "" {
		result.BaseURL = defaultBaseURL(preset, req.Protocol, req.StrategyVariant)
	}
	if modelListUsesStaticOnly(preset.ModelList.Mode) {
		result.Source = "static"
		result.Models = fallbackModels(preset, req)
		return result, http.StatusOK, nil
	}

	if result.BaseURL == "" {
		return result, http.StatusBadRequest, fmt.Errorf("base_url is required")
	}
	if strings.TrimSpace(req.APIKey) == "" {
		return result, http.StatusBadRequest, fmt.Errorf("api_key is required")
	}
	models, status, err := fetchRemoteModels(ctx, result.BaseURL, result.Endpoint, req.APIKey, req.CustomHeaders, req.Existing)
	if err != nil {
		if modelListAllowsStaticFallback(preset.ModelList.Mode) {
			fallback := fallbackModels(preset, req)
			if len(fallback) > 0 {
				result.Source = "static"
				result.RemoteError = err.Error()
				result.Models = fallback
				return result, http.StatusOK, nil
			}
		}
		if result.ManualAllowed {
			result.Source = "manual"
			result.RemoteError = err.Error()
			result.Models = []ModelListItem{}
			return result, http.StatusOK, nil
		}
		return result, status, err
	}
	for i := range models {
		models[i].Source = "remote"
		models[i].DefaultEndpoint = defaultEndpointForCapability(req)
	}
	result.Models = models
	return result, http.StatusOK, nil
}

func fetchRemoteModels(ctx context.Context, baseURL, endpoint, apiKey string, customHeaders map[string]string, existing map[string]bool) ([]ModelListItem, int, error) {
	url := config.JoinAPIURL(baseURL, endpoint)
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	// Anthropic-compatible gateways often accept either; setting both keeps
	// the official Anthropic /v1/models and OpenAI-compatible proxies working.
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	if err := requestheader.Apply(ctx, req.Header, customHeaders); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid custom_headers: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, http.StatusBadGateway, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}

	models, err := parseModelList(resp)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("parse upstream response: %w", err)
	}
	if existing == nil {
		existing = map[string]bool{}
	}
	for i := range models {
		models[i].Added = existing[models[i].ID]
	}
	return models, http.StatusOK, nil
}

func parseModelList(resp *http.Response) ([]ModelListItem, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	var envelope struct {
		Data   []rawModel `json:"data"`
		Models []rawModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if len(envelope.Data) > 0 {
			return rawModelsToItems(envelope.Data), nil
		}
		if len(envelope.Models) > 0 {
			return rawModelsToItems(envelope.Models), nil
		}
	}

	var direct []rawModel
	if err := json.Unmarshal(raw, &direct); err == nil {
		return rawModelsToItems(direct), nil
	}
	return nil, fmt.Errorf("unsupported model list shape")
}

type rawModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Model       string `json:"model"`
	DisplayName string `json:"display_name"`
	Created     int64  `json:"created"`
	CreatedAt   string `json:"created_at"`
	OwnedBy     string `json:"owned_by"`
	Type        string `json:"type"`
}

func rawModelsToItems(raw []rawModel) []ModelListItem {
	out := make([]ModelListItem, 0, len(raw))
	for _, model := range raw {
		id := firstNonEmpty(model.ID, model.Name, model.Model)
		if id == "" {
			continue
		}
		out = append(out, ModelListItem{
			ID:          id,
			DisplayName: model.DisplayName,
			Created:     modelCreatedUnix(model),
			OwnedBy:     firstNonEmpty(model.OwnedBy, model.Type),
		})
	}
	return out
}

func modelCreatedUnix(model rawModel) int64 {
	if model.Created > 0 {
		return model.Created
	}
	if model.CreatedAt == "" {
		return 0
	}
	if ts, err := strconv.ParseInt(model.CreatedAt, 10, 64); err == nil {
		return ts
	}
	if t, err := time.Parse(time.RFC3339, model.CreatedAt); err == nil {
		return t.Unix()
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func defaultModelEndpoint(preset ProviderPreset, protocol string) string {
	normalized := normalizeProtocol(protocol)
	for _, p := range preset.Protocols {
		if string(p.ID) != normalized {
			continue
		}
		if p.EndpointDefaults.ModelList != "" {
			return p.EndpointDefaults.ModelList
		}
	}
	if preset.ModelList.DefaultEndpoint != "" {
		return preset.ModelList.DefaultEndpoint
	}
	return "/models"
}

func defaultBaseURL(preset ProviderPreset, protocol, variant string) string {
	normalized := normalizeProtocol(protocol)
	variant = strings.ToLower(strings.TrimSpace(variant))
	if variant == "" {
		variant = strings.ToLower(strings.TrimSpace(preset.DefaultVariant))
	}
	for _, v := range preset.Variants {
		if strings.ToLower(strings.TrimSpace(v.ID)) != variant {
			continue
		}
		if v.DefaultBaseURLs != nil {
			if baseURL := strings.TrimSpace(v.DefaultBaseURLs[ProtocolID(normalized)]); baseURL != "" {
				return strings.TrimRight(baseURL, "/")
			}
		}
	}
	for _, p := range preset.Protocols {
		if string(p.ID) == normalized && p.DefaultBaseURL != "" {
			return strings.TrimRight(p.DefaultBaseURL, "/")
		}
	}
	return ""
}

func defaultEndpointForCapability(req ModelListRequest) string {
	switch req.Capability {
	case CapabilityImageGeneration:
		return "/images/generations"
	case CapabilityVideoGeneration:
		return "/videos/generations"
	case CapabilityAudioGeneration:
		return "/audio/speech"
	default:
		return config.DefaultLLMAPIEndpoint(req.Protocol)
	}
}

func modelListAllowsManual(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "remote_then_manual", "manual", "static_then_manual", "remote_then_static_manual":
		return true
	default:
		return false
	}
}

func modelListUsesStaticOnly(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "static", "static_then_manual":
		return true
	default:
		return false
	}
}

func modelListAllowsStaticFallback(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "remote_then_static", "remote_then_static_manual":
		return true
	default:
		return false
	}
}

func fallbackModels(preset ProviderPreset, req ModelListRequest) []ModelListItem {
	if len(preset.ModelList.Fallback) == 0 {
		return []ModelListItem{}
	}
	if req.Existing == nil {
		req.Existing = map[string]bool{}
	}
	out := make([]ModelListItem, 0, len(preset.ModelList.Fallback))
	for _, model := range preset.ModelList.Fallback {
		if model.ID == "" || !modelMatchesCapability(model, req.Capability) {
			continue
		}
		defaultEndpoint := model.DefaultEndpoint
		if defaultEndpoint == "" {
			defaultEndpoint = defaultEndpointForCapability(req)
		}
		out = append(out, ModelListItem{
			ID:              model.ID,
			DisplayName:     model.DisplayName,
			OwnedBy:         model.OwnedBy,
			Added:           req.Existing[model.ID],
			Source:          "static",
			DefaultEndpoint: defaultEndpoint,
		})
	}
	return out
}

func modelMatchesCapability(model ModelPreset, capability Capability) bool {
	if capability == "" || capability == CapabilityModels {
		return true
	}
	for _, candidate := range model.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

func normalizeProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "", "openai":
		return string(ProtocolOpenAIChat)
	case "anthropic":
		return string(ProtocolAnthropicMessages)
	default:
		return strings.ToLower(strings.TrimSpace(protocol))
	}
}
