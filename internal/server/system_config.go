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

type systemConfigResponse struct {
	Limits   limitsResponse            `json:"limits"`
	SubAgent subAgentResponse          `json:"sub_agent"`
	WorkMode workModeResponse          `json:"work_mode"`
	UI       uiResponse                `json:"ui"`
	Vision   visionRecognitionResponse `json:"vision_recognition"`
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

// GetSystemConfig GET /api/v1/config
func (h *Handler) GetSystemConfig(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	resp := systemConfigResponse{
		Limits: limitsToResp(h.getCfg().Limits),
		SubAgent: subAgentResponse{
			CacheTTL: h.getCfg().SubAgent.CacheTTL,
			Timeout:  h.getCfg().SubAgent.Timeout,
		},
		WorkMode: workModeResponse{
			Default: string(h.getCfg().WorkMode.Default.Normalize()),
		},
		UI: uiResponse{
			CloseBehavior: string(h.getCfg().UI.CloseBehavior.Normalize()),
		},
		Vision: visionRecognitionToResp(h.getCfg().Vision),
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
		Limits:   limitsToResp(updated.Limits),
		SubAgent: subAgentResponse{CacheTTL: updated.SubAgent.CacheTTL, Timeout: updated.SubAgent.Timeout},
		WorkMode: workModeResponse{Default: string(updated.WorkMode.Default.Normalize())},
		UI:       uiResponse{CloseBehavior: string(updated.UI.CloseBehavior.Normalize())},
		Vision:   visionRecognitionToResp(updated.Vision),
	}
	c.JSON(http.StatusOK, resp)
}
