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
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	kc := h.cfg.Knowledge
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
	if h.cfg == nil {
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
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	type modelItem struct {
		Provider       string `json:"provider"`
		Model          string `json:"model"`
		SupportsVision bool   `json:"supports_vision"`
	}
	var out []modelItem
	for _, p := range h.cfg.LLM.Providers {
		for _, m := range p.AllModels() {
			out = append(out, modelItem{
				Provider:       p.Name,
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
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	out := make([]knowledgeBaseResponse, 0, len(h.cfg.Knowledge.Bases))
	for _, b := range h.cfg.Knowledge.Bases {
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
	if h.cfg == nil {
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
	if h.cfg == nil {
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
	if h.cfg == nil {
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
		for _, b := range h.cfg.Knowledge.Bases {
			if b.Name == name {
				store, err := knowledge.GetOrOpenWikiStore(b.Name, b.Path)
				if err == nil {
					sections, _ := store.ListBase(context.Background(), b.Name)
					resp.Chunks = len(sections)
				}
				break
			}
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	j := v.(*scanJob)
	if strings.HasPrefix(j.status, "ok: ") {
		var chunks int
		fmt.Sscanf(j.status, "ok: %d chunks", &chunks)
		c.JSON(http.StatusOK, scanProgressResp{Chunks: chunks, Current: j.current, Total: j.total, Done: true})
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

// ListSections GET /api/v1/knowledge/bases/:name/sections
func (h *Handler) ListSections(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var base config.KnowledgeBase
	found := false
	for _, b := range h.cfg.Knowledge.Bases {
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
	sections, err := store.ListBase(c.Request.Context(), base.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sections": sections})
}

// GetSection GET /api/v1/knowledge/bases/:name/sections/:id
func (h *Handler) GetSection(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	name := c.Param("name")
	var base config.KnowledgeBase
	found := false
	for _, b := range h.cfg.Knowledge.Bases {
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
	section, err := store.GetSection(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "section not found"})
		return
	}
	c.JSON(http.StatusOK, section)
}

// AddSection POST /api/v1/knowledge/bases/:name/sections
func (h *Handler) AddSection(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var base config.KnowledgeBase
	found := false
	for _, b := range h.cfg.Knowledge.Bases {
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
	var req struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Source  string `json:"source"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	if req.Source == "" {
		req.Source = "_manual_"
	}

	store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	id, err := store.InsertSection(c.Request.Context(), knowledge.WikiSection{
		Title:   req.Title,
		Content: req.Content,
		Source:  req.Source,
		Base:    name,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "ok": true})
}

// UpdateSection PUT /api/v1/knowledge/bases/:name/sections/:id
func (h *Handler) UpdateSection(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	name := c.Param("name")
	var base config.KnowledgeBase
	found := false
	for _, b := range h.cfg.Knowledge.Bases {
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
	var req struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	if err := store.UpdateSection(c.Request.Context(), id, req.Title, req.Content); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DeleteSection DELETE /api/v1/knowledge/bases/:name/sections/:id
func (h *Handler) DeleteSection(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	name := c.Param("name")
	var base config.KnowledgeBase
	found := false
	for _, b := range h.cfg.Knowledge.Bases {
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
	if err := store.DeleteSection(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SearchKnowledge POST /api/v1/knowledge/search
func (h *Handler) SearchKnowledge(c *gin.Context) {
	if h.cfg == nil {
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

	kc := h.cfg.Knowledge
	if !kc.Enabled || len(kc.Bases) == 0 {
		c.JSON(http.StatusOK, gin.H{"query": req.Query, "results": []interface{}{}})
		return
	}

	base := kc.Bases[0]
	store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("open wiki store: %v", err)})
		return
	}

	ctx := c.Request.Context()
	type resultItem struct {
		Source     string  `json:"source"`
		Content    string  `json:"content"`
		Similarity float64 `json:"similarity"`
		Rank       int     `json:"rank"`
	}
	seen := map[string]bool{}
	var out []resultItem

	// Wiki FTS5 search.
	sections, err := store.SearchFTS(ctx, req.Query, req.TopK)
	if err == nil {
		for _, s := range sections {
			key := s.Source
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, resultItem{
				Source:     s.Source,
				Content:    s.Content,
				Similarity: 1.0,
				Rank:       len(out) + 1,
			})
		}
	}

	// Grep actual files.
	if req.Grep != "" {
		for _, gr := range grepKB(h.cfg, req.Grep, req.TopK) {
			key := gr.Path
			if seen[key] {
				continue
			}
			seen[key] = true
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
	if h.cfg == nil || !h.cfg.Knowledge.AutoIndex || !h.cfg.Knowledge.Enabled {
		return
	}
	kc := h.cfg.Knowledge
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
	for i := range h.cfg.Knowledge.Bases {
		if h.cfg.Knowledge.Bases[i].Name == name {
			base = &h.cfg.Knowledge.Bases[i]
			break
		}
	}
	if base == nil {
		return fmt.Errorf("knowledge base %q not found", name)
	}

	kc := h.cfg.Knowledge
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
		h.cfg.Knowledge = kc
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

		fileCount, sectionCount, mediaFiles, err := h.wikiScan(ctx, store, base, basePath, name, func(current, total int) {
			job.current = current
			job.total = total
			job.status = "running"
		})
		if err != nil {
			job.status = fmt.Sprintf("error: %v", err)
			log.Printf("[scan %s] wiki scan: %v", name, err)
			return
		}

		// Process media files collected during the wikiScan walk.
		if len(mediaFiles) > 0 && h.agent != nil {
			log.Printf("[scan %s] processing %d media files with model %s", name, len(mediaFiles), base.ScanModel)
			var sections []knowledge.WikiSection
			for i, path := range mediaFiles {
				select {
				case <-ctx.Done():
					break
				default:
				}
				ext := strings.ToLower(filepath.Ext(path))
				mt := knowledge.IsMediaFile(ext, base.ScanMediaTypes)
				if mt == "" {
					continue
				}
				desc, err := h.describeMediaFile(ctx, base, path, mt)
				if err != nil {
					log.Printf("[scan %s] media %s: %v", name, path, err)
					continue
				}
				rel, _ := filepath.Rel(basePath, path)
				sections = append(sections, knowledge.WikiSection{
					Title:   filepath.ToSlash(rel),
					Content: desc,
					Source:  filepath.ToSlash(rel),
					Base:    name,
				})
				if len(sections) >= 50 {
					if err := store.AppendSections(ctx, sections); err != nil {
						log.Printf("[scan %s] append media: %v", name, err)
					}
					sectionCount += len(sections)
					sections = nil
				}
				job.current = fileCount + i + 1
				job.total = fileCount + len(mediaFiles)
				job.status = "media"
			}
			if len(sections) > 0 {
				if err := store.AppendSections(ctx, sections); err != nil {
					log.Printf("[scan %s] append media: %v", name, err)
				}
				sectionCount += len(sections)
			}
		}

		job.status = fmt.Sprintf("ok: %d sections", sectionCount)
		job.total = fileCount
		job.current = fileCount
		job.chunks = sectionCount
		log.Printf("[scan %s] done: %d sections in %d files", name, sectionCount, fileCount)
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

// indexerSystemPrompt is loaded once from prompts/knowledge_indexer.md.
var indexerSystemPrompt = loadIndexerPrompt()

func loadIndexerPrompt() string {
	data, err := os.ReadFile("prompts/knowledge_indexer.md")
	if err != nil {
		log.Printf("[kb] load indexer prompt: %v", err)
		return "You are a knowledge-base indexing assistant. Output format: 内容概览：...\\n关键词：...\\n搜索匹配：..."
	}
	return string(data)
}

// wikiScan walks a directory, parses all indexable files into wiki
// sections and collects media file paths for downstream processing.
// Returns (fileCount, sectionCount, mediaFiles, error).
// When base.ScanModel is set, each section is summarized by LLM before storage.
func (h *Handler) wikiScan(ctx context.Context, store *knowledge.WikiStore, base *config.KnowledgeBase, dir, baseName string, progress func(current, total int)) (int, int, []string, error) {
	if _, err := os.Stat(dir); err != nil {
		return 0, 0, nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return 0, 0, nil, err
	}

	wantMedia := base.ScanModel != "" && len(base.ScanMediaTypes) > 0

	// Phase 1: count files (text + media).
	var totalFiles, mediaCount int
	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if knowledge.IndexableExtensions[ext] {
			totalFiles++
		} else if wantMedia && knowledge.IsMediaFile(ext, base.ScanMediaTypes) != "" {
			mediaCount++
		}
		return nil
	})
	if walkErr != nil {
		log.Printf("[scan %s] counting phase walk error: %v", baseName, walkErr)
	}

	log.Printf("[scan %s] found %d indexable files + %d media files", baseName, totalFiles, mediaCount)
	var processed, totalSections, skipped int
	currentSources := make(map[string]bool)
	var mediaFiles []string

	mediaMaxSize := base.MaxFileSize
	if mediaMaxSize <= 0 {
		mediaMaxSize = 5 * 1024 * 1024
	}

	// Phase 2: parse text + collect media (single walk).
	walkPhase2Err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		select {
		case <-ctx.Done():
			return fmt.Errorf("scan cancelled after %d/%d files", processed, totalFiles)
		default:
		}
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !knowledge.IndexableExtensions[ext] {
			// Collect media files during this walk.
			if wantMedia && knowledge.IsMediaFile(ext, base.ScanMediaTypes) != "" && info.Size() <= mediaMaxSize {
				mediaFiles = append(mediaFiles, path)
			}
			return nil
		}
		sizeLimit := int64(5 * 1024 * 1024)
		if info.Size() > sizeLimit {
			return nil
		}

		rel, rErr := filepath.Rel(dir, path)
		if rErr != nil {
			log.Printf("[scan] skip %s: %v", path, rErr)
			return nil
		}
		rel = filepath.ToSlash(rel)

		// Check base-level exclude patterns.
		if len(base.ExcludePatterns) > 0 {
			skip := false
			for _, pat := range base.ExcludePatterns {
				if matched, _ := filepath.Match(pat, rel); matched {
					skip = true
					break
				}
			}
			if skip {
				return nil
			}
		}

		currentSources[rel] = true

		// Check mtime for incremental skip.
		storedMtime, _ := store.GetFileMtime(ctx, baseName, rel)
		if storedMtime > 0 && storedMtime == info.ModTime().Unix() {
			skipped++
			processed++
			if progress != nil {
				progress(processed, totalFiles)
			}
			return nil
		}

		text, readErr := knowledge.ReadFileText(path)
		if readErr != nil {
			log.Printf("[scan] skip %s: read: %v", rel, readErr)
			processed++
			if progress != nil {
				progress(processed, totalFiles)
			}
			return nil
		}

		// Build heading tree from file text.
		roots := knowledge.BuildHeadingTree(text, 3)
		var sections []knowledge.WikiSection

		// Generate LLM-indexed entries for every node with content.
		knowledge.WalkHeadingTree(roots, func(node *knowledge.HeadingNode) {
			if !node.HasContent() {
				return
			}
			heading := ""
			if node.Parent != nil {
				heading = node.Parent.Title
			}

			aggregated := node.AggregatedContent()
			content := aggregated // fallback if LLM fails
			if base.ScanModel != "" && h.agent != nil {
				if indexed, err := h.buildIndexEntry(ctx, base, node.Title, heading, aggregated); err == nil && indexed != "" {
					content = indexed
				} else if err != nil {
					log.Printf("[scan] index %s/%s: %v", rel, node.Title, err)
				}
			}

			sections = append(sections, knowledge.WikiSection{
				Title:   node.Title,
				Content: content,
				Source:  rel,
				Base:    baseName,
				Heading: heading,
			})
		})

		if len(sections) == 0 {
			// Fallback: no headings found → treat whole file as single entry.
			content := text
			if base.ScanModel != "" && h.agent != nil {
				if indexed, err := h.buildIndexEntry(ctx, base, rel, "", text); err == nil && indexed != "" {
					content = indexed
				}
			}
			title := rel
			if idx := strings.LastIndex(rel, "/"); idx >= 0 {
				title = rel[idx+1:]
			}
			sections = []knowledge.WikiSection{{
				Title:   title,
				Content: content,
				Source:  rel,
				Base:    baseName,
			}}
		}
		if err := store.ReplaceSource(ctx, baseName, rel, sections); err != nil {
			log.Printf("[scan] replace %s: %v", rel, err)
		}
		_ = store.SetFileMtime(ctx, baseName, rel, info.ModTime().Unix())
		totalSections += len(sections)
		processed++
		if progress != nil {
			progress(processed, totalFiles)
		}
		log.Printf("[scan %d/%d] %s -> %d sections", processed, totalFiles, rel, len(sections))
		return nil
	})
	if walkPhase2Err != nil {
		log.Printf("[scan %s] phase 2 walk error: %v", baseName, walkPhase2Err)
	}

	if skipped > 0 {
		log.Printf("[scan %s] skipped %d unchanged files", baseName, skipped)
	}

	// Clean up stale sources (files deleted since last scan).
	if err := store.RemoveStaleSources(ctx, baseName, currentSources); err != nil {
		log.Printf("[scan %s] stale cleanup: %v", baseName, err)
	}

	log.Printf("[scan %s] indexed %d sections (+%d skipped) in %d files", baseName, totalSections, skipped, processed)
	return processed, totalSections, mediaFiles, nil
}

// mediaScan walks the directory for media files (images/video/audio/pdf) and
// uses the configured LLM to describe each file. Results are added to the wiki
// store as sections keyed by relative path. Returns number of media sections indexed.
func (h *Handler) mediaScan(ctx context.Context, store *knowledge.WikiStore, base *config.KnowledgeBase, dir, baseName string, progress func(current, total int)) (int, error) {
	var mediaFiles []string
	filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if mt := knowledge.IsMediaFile(ext, base.ScanMediaTypes); mt != "" {
			maxSize := base.MaxFileSize
			if maxSize <= 0 {
				maxSize = 5 * 1024 * 1024
			}
			if info.Size() <= maxSize {
				mediaFiles = append(mediaFiles, path)
			}
		}
		return nil
	})

	if len(mediaFiles) == 0 {
		return 0, nil
	}
	log.Printf("[scan %s] found %d media files", baseName, len(mediaFiles))

	var sections []knowledge.WikiSection
	for i, path := range mediaFiles {
		select {
		case <-ctx.Done():
			return len(sections), ctx.Err()
		default:
		}

		ext := strings.ToLower(filepath.Ext(path))
		mt := knowledge.IsMediaFile(ext, base.ScanMediaTypes)
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)

		desc, err := h.describeMediaFile(ctx, base, path, mt)
		if err != nil {
			log.Printf("[scan media %d/%d] %s: %v", i+1, len(mediaFiles), rel, err)
			if progress != nil {
				progress(i+1, len(mediaFiles))
			}
			continue
		}

		section := knowledge.WikiSection{
			Title:   filepath.Base(path),
			Content: desc,
			Source:  rel,
			Base:    baseName,
		}
		sections = append(sections, section)
		if progress != nil {
			progress(i+1, len(mediaFiles))
		}
		log.Printf("[scan media %d/%d] %s → %d chars", i+1, len(mediaFiles), rel, len(desc))
	}

	if len(sections) > 0 {
		if err := store.AppendSections(ctx, sections); err != nil {
			return len(sections), fmt.Errorf("append media sections: %w", err)
		}
	}
	return len(sections), nil
}

// grepResult is a single match from a knowledge-base file grep.
type grepResult struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// grepKB searches the configured knowledge-base directories for lines
// containing pattern (case-insensitive substring match). Returns up to
// maxResults matches, newest files first.
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
		_ = filepath.Walk(absPath, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info == nil || info.IsDir() {
				name := filepath.Base(path)
				if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == ".git" {
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

			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
			lineNo := 0
			for scanner.Scan() {
				lineNo++
				if strings.Contains(strings.ToLower(scanner.Text()), patternLower) {
					out = append(out, grepResult{
						Path:    path,
						Line:    lineNo,
						Content: scanner.Text(),
					})
					if len(out) >= maxResults {
						f.Close()
						return filepath.SkipAll
					}
				}
			}
			f.Close()
			return nil
		})
		if len(out) >= maxResults {
			break
		}
	}
	return out
}
