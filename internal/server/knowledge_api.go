package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
	"github.com/p-chat/pchat/internal/llm"
)

var scanJobs sync.Map // map[string]*scanJob 閳?baseName 閳?job state

type scanJob struct {
	status    string
	startedAt time.Time
	current   int // files processed
	total     int // total files found
	chunks    int // chunks indexed
	cancel    context.CancelFunc
}

type scanProgressResp struct {
	Chunks   int    `json:"chunks"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
	Current  int    `json:"current"`
	Total    int    `json:"total"`
	Message  string `json:"message,omitempty"`
}

// ---- Request / response types ----

type knowledgeConfigResponse struct {
	Enabled   bool                     `json:"enabled"`
	AutoIndex bool                     `json:"auto_index"`
	Bases     []knowledgeBaseResponse  `json:"bases"`
}

type knowledgeBaseResponse struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Enabled   bool     `json:"enabled"`
	FileTypes []string `json:"file_types,omitempty"`

	ScanModel       string   `json:"scan_model"`
	ScanMediaTypes  []string `json:"scan_media_types"`
	AutoScan        bool     `json:"auto_scan"`
	ExcludePatterns []string `json:"exclude_patterns"`
	MaxFileSize     int64    `json:"max_file_size"`

	Status   string `json:"status,omitempty"`
	DocCount int    `json:"doc_count,omitempty"`
}

func baseToResp(b config.KnowledgeBase) knowledgeBaseResponse {
	return knowledgeBaseResponse{
		Name:      b.Name,
		Path:      b.Path,
		Enabled:   b.Enabled,
		FileTypes: b.FileTypes,

		ScanModel:       b.ScanModel,
		ScanMediaTypes:  b.ScanMediaTypes,
		AutoScan:        b.AutoScan,
		ExcludePatterns: b.ExcludePatterns,
		MaxFileSize:     b.MaxFileSize,
	}
}

func baseFromResp(r knowledgeBaseResponse) config.KnowledgeBase {
	return config.KnowledgeBase{
		Name:      r.Name,
		Path:      r.Path,
		Enabled:   r.Enabled,
		FileTypes: r.FileTypes,

		ScanModel:       r.ScanModel,
		ScanMediaTypes:  r.ScanMediaTypes,
		AutoScan:        r.AutoScan,
		ExcludePatterns: r.ExcludePatterns,
		MaxFileSize:     r.MaxFileSize,
	}
}

// ---- Handlers ----

// GetKnowledgeConfig GET /api/v1/knowledge/config
func (h *Handler) GetKnowledgeConfig(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	kc := h.getCfg().Knowledge
	resp := knowledgeConfigResponse{
		Enabled:   kc.Enabled,
		AutoIndex: kc.AutoIndex,
	}
	for _, b := range kc.Bases {
		resp.Bases = append(resp.Bases, baseToResp(b))
	}
	c.JSON(http.StatusOK, resp)
}

// UpdateKnowledgeConfig PATCH /api/v1/knowledge/config
func (h *Handler) UpdateKnowledgeConfig(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	var patch config.KnowledgeConfigPatch
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	updated, err := config.UpdateKnowledgeConfig(patch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	resp := knowledgeConfigResponse{
		Enabled:   updated.Enabled,
		AutoIndex: updated.AutoIndex,
	}
	for _, b := range updated.Bases {
		resp.Bases = append(resp.Bases, baseToResp(b))
	}
	c.JSON(http.StatusOK, resp)
}

// GetKnowledgeModels GET /api/v1/knowledge/models
// Returns all available models across all providers for knowledge-base scanning.
func (h *Handler) GetKnowledgeModels(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	type modelItem struct {
		Provider       string `json:"provider"`
		Protocol       string `json:"protocol,omitempty"`
		Model          string `json:"model"`
		SupportsVision bool   `json:"supports_vision"`
	}
	var out []modelItem
	for _, p := range h.getCfg().LLM.Providers {
		for _, m := range p.AllModels() {
			out = append(out, modelItem{
				Provider:       p.Name,
				Protocol:       p.GetProtocol(),
				Model:          m.Name,
				SupportsVision: m.Capabilities.SupportsVision,
			})
		}
	}
	if out == nil {
		out = []modelItem{}
	}
	c.JSON(http.StatusOK, out)
}

// ListKnowledgeBases GET /api/v1/knowledge/bases
func (h *Handler) ListKnowledgeBases(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	out := make([]knowledgeBaseResponse, 0, len(h.getCfg().Knowledge.Bases))
	for _, b := range h.getCfg().Knowledge.Bases {
		resp := baseToResp(b)
		// Enrich with scan job status.
		if v, ok := scanJobs.Load(b.Name); ok {
			j := v.(*scanJob)
			if strings.HasPrefix(j.status, "ok: ") {
				resp.Status = "ok"
				resp.DocCount = j.chunks
			} else if strings.Contains(j.status, "error") {
				resp.Status = "error"
			} else {
				resp.Status = "scanning"
			}
		}
		out = append(out, resp)
	}
	c.JSON(http.StatusOK, out)
}

// AddKnowledgeBase POST /api/v1/knowledge/bases
func (h *Handler) AddKnowledgeBase(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	var req knowledgeBaseResponse
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if req.Name == "" || req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and path are required"})
		return
	}

	base := baseFromResp(req)
	if err := config.AddKnowledgeBaseRecord(base); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	h.reloadAfterConfigChange()

	c.JSON(http.StatusCreated, gin.H{"ok": true, "name": req.Name})
}

// RemoveKnowledgeBase DELETE /api/v1/knowledge/bases/:name
func (h *Handler) RemoveKnowledgeBase(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if err := config.RemoveKnowledgeBaseRecord(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": name})
}

// ScanKnowledgeBase POST /api/v1/knowledge/bases/:name/scan
func (h *Handler) ScanKnowledgeBase(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	err := h.startScanJob(name)
	if err != nil {
		if strings.Contains(err.Error(), "already running") {
			c.JSON(http.StatusConflict, gin.H{"ok": false, "message": err.Error()})
		} else if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not configured") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "message": "scan started"})
}

// ScanStatus GET /api/v1/knowledge/bases/:name/scan/status
func (h *Handler) ScanStatus(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	v, ok := scanJobs.Load(name)
	if !ok {
		// No active scan — return current section count from wiki store.
		resp := scanProgressResp{Done: false}
		for _, b := range h.getCfg().Knowledge.Bases {
			if b.Name == name {
				store, err := knowledge.GetOrOpenWikiStore(b.Name, b.Path)
				if err == nil {
					resp.Chunks = store.CountNodes(context.Background(), b.Name)
				}
				break
			}
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	j := v.(*scanJob)
	if strings.HasPrefix(j.status, "ok: ") {
		c.JSON(http.StatusOK, scanProgressResp{Chunks: j.chunks, Current: j.current, Total: j.total, Done: true})
	} else if strings.HasPrefix(j.status, "error: ") {
		errMsg := strings.TrimPrefix(j.status, "error: ")
		c.JSON(http.StatusOK, scanProgressResp{Error: errMsg, Done: true})
	} else {
		msg := "扫描中..."
		if j.status == "counting" {
			msg = "正在统计文件..."
		}
		c.JSON(http.StatusOK, scanProgressResp{Current: j.current, Total: j.total, Chunks: j.chunks, Message: msg})
	}
}

// CancelScan DELETE /api/v1/knowledge/bases/:name/scan
func (h *Handler) CancelScan(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	v, ok := scanJobs.Load(name)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"ok": true, "message": "没有正在进行的扫描"})
		return
	}
	j := v.(*scanJob)
	if j.cancel != nil {
		j.cancel()
	}
	scanJobs.Delete(name)
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "scan cancelled"})
}

// ClearKnowledgeBase DELETE /api/v1/knowledge/bases/:name/clear
func (h *Handler) ClearKnowledgeBase(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var base config.KnowledgeBase
	found := false
	for _, b := range h.getCfg().Knowledge.Bases {
		if b.Name == name {
			base = b
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "base not found"})
		return
	}

	store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	if err := store.ClearBase(c.Request.Context(), base.Name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListNodes GET /api/v1/knowledge/bases/:name/nodes
func (h *Handler) ListNodes(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var base config.KnowledgeBase
	found := false
	for _, b := range h.getCfg().Knowledge.Bases {
		if b.Name == name {
			base = b
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "base not found"})
		return
	}

	store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	nodes, err := store.ListNodes(c.Request.Context(), base.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"nodes": nodes})
}

// GetNodeContent GET /api/v1/knowledge/bases/:name/nodes/:id/content
func (h *Handler) GetNodeContent(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	name := c.Param("name")
	var base config.KnowledgeBase
	found := false
	for _, b := range h.getCfg().Knowledge.Bases {
		if b.Name == name {
			base = b
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "base not found"})
		return
	}
	store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	contents, err := store.GetNodeContent(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node content not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"contents": contents})
}

// DeleteNode DELETE /api/v1/knowledge/bases/:name/nodes/:id
func (h *Handler) DeleteNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var base config.KnowledgeBase
	found := false
	for _, b := range h.getCfg().Knowledge.Bases {
		if b.Name == name {
			base = b
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "base not found"})
		return
	}
	store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	if err := store.DeleteNode(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SearchKnowledge POST /api/v1/knowledge/search

// SearchKnowledge POST /api/v1/knowledge/search
func (h *Handler) SearchKnowledge(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	var req struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
		Grep  string `json:"grep"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if req.Query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query is required"})
		return
	}
	if req.TopK <= 0 {
		req.TopK = 5
	}

	kc := h.getCfg().Knowledge
	if !kc.Enabled || len(kc.Bases) == 0 {
		c.JSON(http.StatusOK, gin.H{"query": req.Query, "results": []any{}})
		return
	}

	ctx := c.Request.Context()
	type resultItem struct {
		Source     string  `json:"source"`
		Content    string  `json:"content"`
		Similarity float64 `json:"similarity"`
		Rank       int     `json:"rank"`
	}
	var out []resultItem
	// Search ALL configured bases (not just Bases[0]). For each
	// base, take up to req.TopK matches, then merge and re-truncate
	// to req.TopK so a multi-base search returns the best hits
	// overall rather than silently dropping everything past the
	// first base.
	for _, base := range kc.Bases {
		if len(out) >= req.TopK {
			break
		}
		store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
		if err != nil {
			log.Printf("[search] open wiki store %q: %v", base.Name, err)
			continue
		}
		res, err := store.LookupSearch(ctx, req.Query, base.Name, true, 0, 1, req.TopK)
		if err != nil {
			log.Printf("[search] lookup in %q: %v", base.Name, err)
			continue
		}
		for _, it := range res.Items {
			if len(out) >= req.TopK {
				break
			}
			content := it.Overview
			if len(it.Children) > 0 {
				for _, c := range it.Children {
					content += "\n" + c.Content
				}
			}
			if content == "" {
				content = it.Title
			}
			out = append(out, resultItem{
				Source:     it.Source,
				Content:    content,
				Similarity: it.Rank,
				Rank:       len(out),
			})
		}
	}

	// Grep actual files.
	if req.Grep != "" {
		for _, gr := range grepKB(h.getCfg(), req.Grep, req.TopK) {
			if len(out) >= req.TopK {
				break
			}
			out = append(out, resultItem{
				Source:     fmt.Sprintf("%s:%d", gr.Path, gr.Line),
				Content:    gr.Content,
				Similarity: 1.0,
				Rank:       len(out) + 1,
			})
		}
	}

	if len(out) > req.TopK {
		out = out[:req.TopK]
	}
	c.JSON(http.StatusOK, gin.H{"query": req.Query, "results": out})
}

// (removed: GetEmbedders — vector embedding system deprecated)

// AutoIndexKnowledgeBases starts background scans for every enabled
// knowledge base when Knowledge.AutoIndex is true. Safe to call even if
// the feature is disabled or no bases are configured.
func (h *Handler) AutoIndexKnowledgeBases() {
	if h.getCfg() == nil || !h.getCfg().Knowledge.AutoIndex || !h.getCfg().Knowledge.Enabled {
		return
	}
	kc := h.getCfg().Knowledge
	for _, base := range kc.Bases {
		if !base.Enabled {
			continue
		}
		if err := h.startScanJob(base.Name); err != nil {
			log.Printf("[auto-index %s] %v", base.Name, err)
		} else {
			log.Printf("[auto-index %s] scan started", base.Name)
		}
	}
}

// ---- helpers ----

func (h *Handler) startScanJob(name string) error {
	// Check if a scan is already running. Allow new scan if the
	// previous job has finished (ok / error status) or is stale
	// (older than 30min 閳?leftover from a crashed instance).
	if v, ok := scanJobs.Load(name); ok {
		j := v.(*scanJob)
		if strings.HasPrefix(j.status, "ok: ") || strings.HasPrefix(j.status, "error: ") {
			scanJobs.Delete(name) // completed, allow new scan
		} else if time.Since(j.startedAt) < 30*time.Minute {
			return fmt.Errorf("scan running")
		} else {
			scanJobs.Delete(name) // stale, clean up
		}
	}

	var base *config.KnowledgeBase
	for i := range h.getCfg().Knowledge.Bases {
		if h.getCfg().Knowledge.Bases[i].Name == name {
			base = &h.getCfg().Knowledge.Bases[i]
			break
		}
	}
	if base == nil {
		return fmt.Errorf("knowledge base %q not found", name)
	}

	kc := h.getCfg().Knowledge
	needsReload := false
	if !kc.Enabled {
		kc.Enabled = true
		needsReload = true
	}
	if !base.Enabled {
		base.Enabled = true
		needsReload = true
	}
	if needsReload {
		h.getCfg().Knowledge = kc
		h.reloadAfterConfigChange()
		log.Printf("[scan %s] auto-enabled knowledge base temporarily for scan (not persisted to config)", name)
	}

	basePath, err := filepath.Abs(base.Path)
	if err != nil {
		return fmt.Errorf("abs path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	job := &scanJob{status: "counting", startedAt: time.Now(), cancel: cancel}
	scanJobs.Store(name, job)

	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[scan %s] panic: %v", name, r)
				job.status = fmt.Sprintf("error: panic: %v", r)
			}
		}()

		store, err := knowledge.GetOrOpenWikiStore(base.Name, basePath)
		if err != nil {
			job.status = fmt.Sprintf("error: wiki store: %v", err)
			log.Printf("[scan %s] wiki store: %v", name, err)
			return
		}

		fileCount := countIndexableFiles(basePath, base.ExcludePatterns)
		job.total = fileCount
		job.current = 0
		job.status = "running"

		if fileCount == 0 {
			log.Printf("[scan %s] no indexable files found in %s", name, basePath)
		}

		l2Count, l3Count, idxErr := h.indexScan(ctx, store, base, basePath, name, func(current int) {
			job.current = current
		})
		if idxErr != nil {
			job.status = fmt.Sprintf("error: %v", idxErr)
			if strings.Contains(idxErr.Error(), "delete") && strings.Contains(idxErr.Error(), "re-scan") {
				job.status += " | 恢复：删除 wiki.db 后重新扫描即可重建索引"
			}
			log.Printf("[scan %s] index scan: %v", name, idxErr)
			return
		}

		job.status = fmt.Sprintf("ok: %d L2 files, %d L3 sections", l2Count, l3Count)
		job.total = fileCount
		job.current = fileCount
		job.chunks = l3Count
		log.Printf("[scan %s] done: %d L2 files, %d L3 sections", name, l2Count, l3Count)
	}()
	return nil
}

// describeMediaFile calls the configured LLM to describe an image/PDF/other media file.
// Uses SHA256 caching to avoid re-processing identical files.
func (h *Handler) describeMediaFile(ctx context.Context, base *config.KnowledgeBase, path, mediaType string) (string, error) {
	if base.ScanModel == "" || h.agent == nil {
		return "", fmt.Errorf("no scan model configured")
	}
	parts := strings.SplitN(base.ScanModel, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid scan_model format, expected provider/model")
	}
	provider, model := parts[0], parts[1]

	// SHA256 cache check.
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hsh := sha256.New()
	if _, err := io.Copy(hsh, f); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	sum := fmt.Sprintf("%x", hsh.Sum(nil))

	store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
	if err != nil {
		return "", err
	}
	if cached, err := store.GetCachedMediaDescription(ctx, sum); err == nil && cached != "" {
		return cached, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") && !strings.HasPrefix(mime, "video/") {
		mime = "application/octet-stream"
	}

	prompt := "Describe this file in detail. Focus on text content, visual elements, and structure. Write in the original language of any text found. If no text is visible, describe what you see."
	msgs := []llm.ChatMessage{
		{Role: "user", Type: "text", Content: prompt},
		{Role: "user", Type: "image", Content: base64.StdEncoding.EncodeToString(data), Name: filepath.Base(path), MimeType: mime},
	}

	ch := h.agent.LLM().ChatStreamCM(ctx, provider, model, msgs, nil, llm.ChatOptions{})
	var sb strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return "", chunk.Err
		}
		if chunk.Done {
			break
		}
		sb.WriteString(chunk.Content)
	}
	desc := sb.String()

	if desc != "" {
		_ = store.CacheMediaDescription(ctx, sum, desc)
	}
	return desc, nil
}

// buildIndexEntry sends a heading node's aggregated content to the LLM
// and returns the formatted 3-line index entry (概览+关键词+搜索匹配).
// Uses SHA256 caching on the aggregated content to avoid duplicate calls.
func (h *Handler) buildIndexEntry(ctx context.Context, base *config.KnowledgeBase, title, parentTitle, aggregatedContent string) (string, error) {
	if base.ScanModel == "" || h.agent == nil {
		return "", fmt.Errorf("no scan model configured")
	}
	parts := strings.SplitN(base.ScanModel, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid scan_model format")
	}
	provider, model := parts[0], parts[1]

	hsh := sha256.New()
	hsh.Write([]byte(title + aggregatedContent))
	sum := fmt.Sprintf("%x", hsh.Sum(nil))

	store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
	if err == nil {
		if cached, err := store.GetCachedMediaDescription(ctx, sum); err == nil && cached != "" {
			return cached, nil
		}
	}

	userPrompt := knowledge.BuildIndexPrompt(title, parentTitle, aggregatedContent)
	msgs := []llm.ChatMessage{
		{Role: "system", Type: "text", Content: indexerSystemPrompt},
		{Role: "user", Type: "text", Content: userPrompt},
	}

	ch := h.agent.LLM().ChatStreamCM(ctx, provider, model, msgs, nil, llm.ChatOptions{})
	var sb strings.Builder
	for chunk := range ch {
		select {
		case <-ctx.Done():
			return aggregatedContent, ctx.Err()
		default:
		}
		if chunk.Err != nil {
			return aggregatedContent, nil
		}
		if chunk.Done {
			break
		}
		sb.WriteString(chunk.Content)
	}
	raw := strings.TrimSpace(sb.String())
	if raw == "" {
		return aggregatedContent, nil
	}

	parsed := knowledge.ParseIndexEntry(raw)
	if parsed == nil {
		return raw, nil
	}
	result := knowledge.FormatIndexEntry(parsed)
	if result == "" {
		return aggregatedContent, nil
	}

	if store, err2 := knowledge.GetOrOpenWikiStore(base.Name, base.Path); err2 == nil {
		_ = store.CacheMediaDescription(ctx, sum, result)
	}
	return result, nil
}

// indexerSystemPrompt is the LLM system instruction for knowledge indexing.
const indexerSystemPrompt = `You are a knowledge-base indexing assistant. Given a document section with its full context (including all sub-sections), produce a searchable index entry.

## Output Format (exactly 3 lines, no extra text):

内容概览：<100 characters summarizing the core content of this section and its subsections>
关键词：<5-15 comma-separated keywords, mix of Chinese and English>
搜索匹配：<one sentence describing what search intents should match this entry, 30 chars max>

## Rules

1. "内容概览" must be a single concise sentence covering the main topic.
2. "关键词" must include both technical terms and user-facing search terms.
3. "搜索匹配" must describe the search intent, not repeat the title.
4. Write in the language of the source document.
5. Do NOT output JSON, markdown code blocks, or any extra formatting. Plain text only.
6. Do NOT prefix with "Output:" or any other label.`

func truncateContent(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}



// mediaScan walks the directory for media files (images/video/audio/pdf) and
// uses the configured LLM to describe each file. Results are added to the wiki
// store as sections keyed by relative path. Returns number of media sections indexed.


// ── Three-level index scan pipeline ──
// indexScan walks the base directory and generates L1/L2/L3 index nodes
// plus ContentNode leaves for FTS5 searching and prompt injection.

func (h *Handler) indexScan(ctx context.Context, store *knowledge.WikiStore, base *config.KnowledgeBase, dir, baseName string, progress func(current int)) (int, int, error) {
	if _, err := os.Stat(dir); err != nil {
		return 0, 0, fmt.Errorf("stat %s: %w", dir, err)
	}
	dir, _ = filepath.Abs(dir)

	// Phase 1: walk → collect L3 nodes + contents per file.
	type fileData struct {
		source   string
		kind     string
		nodes    []knowledge.IndexNode
		contents []knowledge.ContentNode
	}
	var files []fileData
	totalL3 := 0

	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		kind := "text"
		if knowledge.IsMediaFile(ext, []string{}) != "" {
			return nil // Media handled separately.
		}
		if !knowledge.IndexableExtensions[ext] {
			return nil
		}
		if info.Size() > 5*1024*1024 {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)

		text, readErr := knowledge.ReadFileText(path)
		if readErr != nil {
			return nil
		}

		// Build heading tree → L3 nodes.
		roots := knowledge.BuildHeadingTree(text, 3)
		var nodes []knowledge.IndexNode
		var contents []knowledge.ContentNode
		seq := 0
		var walkNodes func([]*knowledge.HeadingNode)
		walkNodes = func(list []*knowledge.HeadingNode) {
			for _, node := range list {
				if !node.HasContent() {
					walkNodes(node.Children)
					continue
				}
				aggregated := node.AggregatedContent()
				title := node.Title
				overview := truncateText(aggregated, 500)
				keywords := ""
				// If scan model is configured, generate keywords + overview via LLM.
				if base.ScanModel != "" && h.agent != nil {
					if idx, e := h.buildIndexEntry(ctx, base, node.Title, "", aggregated); e == nil && idx != "" {
						keywords, overview = parseKWAndOverview(idx)
					}
				}
				nodes = append(nodes, knowledge.IndexNode{
					Level:     3,
					Source:    rel,
					Kind:      kind,
					SortOrder: seq,
					Title:     title,
					Keywords:  keywords,
					Overview:  overview,
				})
				seq++
				// Content leaf.
				content := truncateText(aggregated, 3000)
				contents = append(contents, knowledge.ContentNode{
					Content:     content,
					ContentType: "text",
					SortOrder:   0,
				})
				walkNodes(node.Children)
			}
		}
		walkNodes(roots)

		// Fallback: no headings → whole file as one L3.
		if len(nodes) == 0 && text != "" {
			title := rel
			if idx := strings.LastIndex(rel, "/"); idx >= 0 {
				title = rel[idx+1:]
			}
			overview := truncateText(text, 500)
			nodes = append(nodes, knowledge.IndexNode{
				Level:     3,
				Source:    rel,
				Kind:      kind,
				SortOrder: 0,
				Title:     title,
				Keywords:  "",
				Overview:  overview,
			})
			contents = append(contents, knowledge.ContentNode{
				Content:     truncateText(text, 3000),
				ContentType: "text",
				SortOrder:   0,
			})
		}

		if len(nodes) > 0 {
			files = append(files, fileData{source: rel, kind: kind, nodes: nodes, contents: contents})
			totalL3 += len(nodes)
		}
		if progress != nil {
			progress(len(files))
		}
		return nil
	})
	if walkErr != nil {
		log.Printf("[index-scan %s] walk error: %v", baseName, walkErr)
	}

	// Phase 2: aggregate L2 per file.
	l2Nodes := make([]knowledge.IndexNode, 0, len(files))
	for fi, fd := range files {
		titles := make([]string, 0, len(fd.nodes))
		for _, n := range fd.nodes {
			titles = append(titles, n.Title)
		}
		// Use filename as L2 title.
		title := fd.source
		if idx := strings.LastIndex(fd.source, "/"); idx >= 0 {
			title = fd.source[idx+1:]
		}
		overview := fmt.Sprintf("%s (%d chapters)", title, len(fd.nodes))
		// Aggregate L3 info into L2 overview line.
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("· %s — 关键词: %s — %s — %d 章节",
			title, "", overview, len(fd.nodes)))
		for _, n := range fd.nodes {
			if len(n.Overview) > 0 {
				sb.WriteString(" | " + n.Title)
				break
			}
		}
		l2Node := knowledge.IndexNode{
			ParentID:  1, // Will be set to real L1 ID after insert.
			Level:     2,
			Source:    fd.source,
			Kind:      fd.kind,
			SortOrder: fi,
			Title:     title,
			Keywords:  "",
			Overview:  sb.String(),
		}
		l2Nodes = append(l2Nodes, l2Node)
	}

	// Phase 3: place L1 node.
	l1Overview := buildL1Overview(l2Nodes)
	l1Node := knowledge.IndexNode{
		ParentID:  0,
		Base:      baseName,
		Level:     1,
		Title:     baseName,
		Keywords:  "",
		Overview:  l1Overview,
		SortOrder: 0,
	}

	// Phase 4: assign IDs and write.
	nextID := 1
	l1Node.ID = nextID
	nextID++
	for i := range l2Nodes {
		l2Nodes[i].ID = nextID
		l2Nodes[i].ParentID = l1Node.ID
		l2Nodes[i].Base = baseName
		nextID++
	}
	var allContents []knowledge.ContentNode
	for fi, fd := range files {
		l2ID := l2Nodes[fi].ID
		for i := range fd.nodes {
			fd.nodes[i].ID = nextID
			fd.nodes[i].ParentID = l2ID
			fd.nodes[i].Base = baseName
			nextID++
		}
		for i := range fd.contents {
			fd.contents[i].NodeID = fd.nodes[i].ID
		}
		allContents = append(allContents, fd.contents...)
	}
	allL3s := make([]knowledge.IndexNode, 0, totalL3)
	for _, fd := range files {
		allL3s = append(allL3s, fd.nodes...)
	}

	allNodes := make([]knowledge.IndexNode, 0, 1+len(l2Nodes)+len(allL3s))
	allNodes = append(allNodes, l1Node)
	allNodes = append(allNodes, l2Nodes...)
	allNodes = append(allNodes, allL3s...)

	if err := store.ReplaceBaseNodes(ctx, baseName, allNodes, allContents); err != nil {
		return 0, 0, fmt.Errorf("write index: %w", err)
	}

	log.Printf("[index-scan %s] indexed %d files → L1 + %d L2 + %d L3 + %d contents",
		baseName, len(files), len(l2Nodes), len(allL3s), len(allContents))
	return len(l2Nodes), len(allL3s), nil
}

func buildL1Overview(l2Nodes []knowledge.IndexNode) string {
	if len(l2Nodes) == 0 {
		return "[Knowledge Base]\n(no files indexed)\n"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Knowledge Base] (%d files)\n", len(l2Nodes)))
	count := 0
	for _, l2 := range l2Nodes {
		if sb.Len() > 2000 {
			sb.WriteString(fmt.Sprintf("  ...(+%d files omitted)\n", len(l2Nodes)-count))
			break
		}
		sb.WriteString(fmt.Sprintf("· %s\n", l2.Overview))
		count++
	}
	return sb.String()
}

func parseKWAndOverview(indexed string) (keywords, overview string) {
	// Parse "关键词: a, b, c" and "摘要: ..." from LLM output.
	for _, line := range strings.Split(indexed, "\n") {
		line = strings.TrimSpace(line)
		if (strings.HasPrefix(line, "关键词：") || strings.HasPrefix(line, "关键词:")) && keywords == "" {
			keywords = strings.TrimSpace(line[strings.IndexRune(line, ':')+1:])
		}
		if (strings.HasPrefix(line, "摘要：") || strings.HasPrefix(line, "摘要:")) && overview == "" {
			overview = strings.TrimSpace(line[strings.IndexRune(line, ':')+1:])
		}
	}
	if overview == "" {
		overview = truncateText(indexed, 500)
	}
	return
}

// countIndexableFiles walks a directory and counts files eligible for indexing.
func countIndexableFiles(dir string, excludePatterns []string) int {
	count := 0
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor" || n == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > 5*1024*1024 {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if knowledge.IndexableExtensions[ext] {
			rel, _ := filepath.Rel(dir, p)
			for _, pat := range excludePatterns {
				if matched, _ := filepath.Match(pat, rel); matched {
					return nil
				}
			}
			count++
		}
		return nil
	})
	return count
}

type grepResult struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

func grepKB(cfg *config.Config, pattern string, maxResults int) []grepResult {
	if pattern == "" || maxResults <= 0 {
		return nil
	}
	patternLower := strings.ToLower(pattern)
	var out []grepResult
	kc := cfg.Knowledge
	for _, base := range kc.Bases {
		if !base.Enabled {
			continue
		}
		absPath, err := filepath.Abs(base.Path)
		if err != nil {
			continue
		}
		filepath.Walk(absPath, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info == nil || info.IsDir() {
				n := filepath.Base(path)
				if strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor" || n == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if !knowledge.IndexableExtensions[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			if info.Size() > 5*1024*1024 {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			// Close explicitly here, NOT via defer. The previous
			// code used `defer f.Close()` inside the Walk
			// callback — defer runs when the OUTER function
			// (grepKB) returns, not when the walk step
			// finishes. A knowledge base with 10,000 files
			// would exhaust file descriptors before grepKB
			// returned.
			scanner := bufio.NewScanner(f)
			lineNum := 0
			for scanner.Scan() && len(out) < maxResults {
				lineNum++
				if strings.Contains(strings.ToLower(scanner.Text()), patternLower) {
					rel, _ := filepath.Rel(absPath, path)
					out = append(out, grepResult{
						Path:    rel,
						Line:    lineNum,
						Content: scanner.Text(),
					})
				}
			}
			f.Close()
			return nil
		})
	}
	return out
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
