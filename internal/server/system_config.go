package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
)

type limitsResponse struct {
	AutoCompactBuffer    int    `json:"auto_compact_buffer"`
	ToolResultExecCap    int    `json:"tool_result_exec_cap"`
	ToolResultReadCap    int    `json:"tool_result_read_cap"`
	ToolResultDefaultCap int    `json:"tool_result_default_cap"`
	PruneAfterRounds     int    `json:"prune_after_rounds"`
	MaxRounds            int    `json:"max_rounds"`
	TodoLongRunMode      string `json:"todo_long_run_mode"`
	MaxStoredMessages    int    `json:"max_stored_messages"`
}

type subAgentResponse struct {
	CacheTTL string `json:"cache_ttl"`
	Timeout  string `json:"timeout"`
}

type workModeResponse struct {
	Default string `json:"default"`
}

type uiResponse struct {
	CloseBehavior string `json:"close_behavior"`
}

type visionRecognitionResponse struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxImageBytes  int64  `json:"max_image_bytes"`
}

type recognitionRouteResponse struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxBytes       int64  `json:"max_bytes"`
	Available      bool   `json:"available"`
}

type recognitionResponse struct {
	Routes map[config.MediaKind]recognitionRouteResponse `json:"routes"`
}

type systemConfigResponse struct {
	Limits      limitsResponse            `json:"limits"`
	SubAgent    subAgentResponse          `json:"sub_agent"`
	WorkMode    workModeResponse          `json:"work_mode"`
	UI          uiResponse                `json:"ui"`
	Vision      visionRecognitionResponse `json:"vision_recognition"`
	Recognition recognitionResponse       `json:"recognition"`
}

func limitsToResp(l config.LimitsConfig) limitsResponse {
	return limitsResponse{
		AutoCompactBuffer:    l.AutoCompactBuffer,
		ToolResultExecCap:    l.ToolResultExecCap,
		ToolResultReadCap:    l.ToolResultReadCap,
		ToolResultDefaultCap: l.ToolResultDefaultCap,
		PruneAfterRounds:     l.PruneAfterRounds,
		MaxRounds:            l.MaxRounds,
		TodoLongRunMode:      string(config.NormalizeTodoLongRunMode(l.TodoLongRunMode)),
		MaxStoredMessages:    l.MaxStoredMessages,
	}
}

func visionRecognitionToResp(v config.VisionRecognitionConfig) visionRecognitionResponse {
	v.Normalize()
	return visionRecognitionResponse{
		Enabled:        v.Enabled,
		Provider:       v.Provider,
		Model:          v.Model,
		TimeoutSeconds: v.TimeoutSeconds,
		MaxImageBytes:  v.MaxImageBytes,
	}
}

func (h *Handler) recognitionToResp(r config.RecognitionConfig) recognitionResponse {
	r.Normalize()
	routes := make(map[config.MediaKind]recognitionRouteResponse, 3)
	for _, kind := range []config.MediaKind{config.MediaImage, config.MediaVideo, config.MediaAudio} {
		route := r.Routes[kind]
		route.Normalize()
		available := h.recognitionRouteAvailable(kind, route)
		routes[kind] = recognitionRouteResponse{
			Enabled: route.Enabled, Provider: route.Provider, Model: route.Model,
			TimeoutSeconds: route.TimeoutSeconds, MaxBytes: route.MaxBytes,
			Available: available,
		}
	}
	return recognitionResponse{Routes: routes}
}

func (h *Handler) recognitionRouteAvailable(kind config.MediaKind, route config.RecognitionRoute) bool {
	if !route.Available() || !h.validModel(route.Provider, route.Model) {
		return false
	}
	for _, provider := range h.getCfg().LLM.Providers {
		if provider.Name != route.Provider {
			continue
		}
		if kind != config.MediaImage && provider.GetProtocol() != "openai" {
			return false
		}
		for _, model := range provider.Models {
			if model.Name == route.Model {
				return model.Capabilities.SupportsInput(kind)
			}
		}
		return false
	}
	return false
}

func subAgentToResp(s config.SubAgentConfig) subAgentResponse {
	return subAgentResponse{
		CacheTTL: s.CacheTTL,
		Timeout:  s.Timeout,
	}
}

// GetSystemConfig GET /api/v1/config
func (h *Handler) GetSystemConfig(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	resp := systemConfigResponse{
		Limits:   limitsToResp(h.getCfg().Limits),
		SubAgent: subAgentToResp(h.getCfg().SubAgent),
		WorkMode: workModeResponse{
			Default: string(h.getCfg().WorkMode.Default.Normalize()),
		},
		UI: uiResponse{
			CloseBehavior: string(h.getCfg().UI.CloseBehavior.Normalize()),
		},
		Vision:      visionRecognitionToResp(h.getCfg().Vision),
		Recognition: h.recognitionToResp(h.getCfg().Recognition),
	}
	c.JSON(http.StatusOK, resp)
}

// UpdateSystemConfig PATCH /api/v1/config
func (h *Handler) UpdateSystemConfig(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	var patch config.SystemConfigPatch
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	updated, err := config.UpdateSystemConfig(patch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	resp := systemConfigResponse{
		Limits:      limitsToResp(updated.Limits),
		SubAgent:    subAgentToResp(updated.SubAgent),
		WorkMode:    workModeResponse{Default: string(updated.WorkMode.Default.Normalize())},
		UI:          uiResponse{CloseBehavior: string(updated.UI.CloseBehavior.Normalize())},
		Vision:      visionRecognitionToResp(updated.Vision),
		Recognition: h.recognitionToResp(updated.Recognition),
	}
	c.JSON(http.StatusOK, resp)
}
