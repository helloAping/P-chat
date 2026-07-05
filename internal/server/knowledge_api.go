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
	var patch config.KnowledgeConfig
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
		c.JSON(http.StatusOK, scanProgressResp{Done: false})
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
		msg := "鎵弿涓?.."
		if j.status == "counting" {
			msg = "姝ｅ湪缁熻鏂囦欢..."
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
		c.JSON(http.StatusOK, gin.H{"ok": true, "message": "濞屸剝婀佹潻娑滎攽娑擃厾娈戦幍顐ｅ伎"})
		return
	}
	j := v.(*scanJob)
	if j.cancel != nil {
		j.cancel()
	}
	scanJobs.Delete(name)
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "scan cancelled"})
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
	if !kc.Enabled || !base.Enabled {
		kc.Enabled = true
		base.Enabled = true
		h.cfg.Knowledge = kc
		if err := config.NewManager().SaveGlobal(h.cfg); err != nil {
			log.Printf("[scan %s] auto-enable save: %v", name, err)
		} else {
			config.Load("")
			h.reloadAfterConfigChange()
			log.Printf("[scan %s] auto-enabled", name)
		}
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

		fileCount, sectionCount, err := h.wikiScan(ctx, store, base, basePath, name, func(current, total int) {
			job.current = current
			job.total = total
			job.status = "running"
		})
		if err != nil {
			job.status = fmt.Sprintf("error: %v", err)
			log.Printf("[scan %s] wiki scan: %v", name, err)
			return
		}

		// Media scan: only if ScanModel is configured and scan has media types.
		if base.ScanModel != "" && len(base.ScanMediaTypes) > 0 && h.agent != nil {
			log.Printf("[scan %s] media scanning with model %s (types: %v)", name, base.ScanModel, base.ScanMediaTypes)
			mediaChunks, mediaErr := h.mediaScan(ctx, store, base, basePath, name, func(current, total int) {
				job.current = fileCount + current
				job.total = fileCount + total
				job.status = "media"
			})
			if mediaErr != nil {
				log.Printf("[scan %s] media scan: %v", name, mediaErr)
				// Don't fail the whole scan; text is already indexed.
			}
			sectionCount += mediaChunks
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

// summarizeText sends raw text content through the configured LLM for
// structured summarization. Uses SHA256 caching to avoid re-processing.
func (h *Handler) summarizeText(ctx context.Context, base *config.KnowledgeBase, source, content string) (string, error) {
	if base.ScanModel == "" || h.agent == nil {
		return content, nil // no model configured, pass through raw content
	}
	parts := strings.SplitN(base.ScanModel, "/", 2)
	if len(parts) != 2 {
		return content, nil
	}
	provider, model := parts[0], parts[1]

	hsh := sha256.New()
	hsh.Write([]byte(content))
	sum := fmt.Sprintf("%x", hsh.Sum(nil))

	store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
	if err == nil {
		if cached, err := store.GetCachedMediaDescription(ctx, sum); err == nil && cached != "" {
			return cached, nil
		}
	}

	prompt := fmt.Sprintf(
		"You are a knowledge archivist. Summarize the following document section for a searchable knowledge base. Extract key facts, definitions, and concepts. Keep the summary concise but complete. Write in the original language. Source: %s\n\n%s",
		source, content,
	)
	msgs := []llm.ChatMessage{
		{Role: "system", Type: "text", Content: "You are a precise knowledge archivist. Output only the summary, no prefixes or explanations."},
		{Role: "user", Type: "text", Content: prompt},
	}

	ch := h.agent.LLM().ChatStreamCM(ctx, provider, model, msgs, nil, llm.ChatOptions{})
	var sb strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return content, nil // fallback to raw content on error
		}
		if chunk.Done {
			break
		}
		sb.WriteString(chunk.Content)
	}
	desc := strings.TrimSpace(sb.String())
	if desc == "" {
		return content, nil
	}

	if store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path); err == nil {
		_ = store.CacheMediaDescription(ctx, sum, desc)
	}
	return desc, nil
}

// wikiScan walks a directory and parses all indexable files into wiki
// sections, storing them via the WikiStore. Returns (fileCount, sectionCount).
// When base.ScanModel is set, each section is summarized by LLM before storage.
func (h *Handler) wikiScan(ctx context.Context, store *knowledge.WikiStore, base *config.KnowledgeBase, dir, baseName string, progress func(current, total int)) (int, int, error) {
	if _, err := os.Stat(dir); err != nil {
		return 0, 0, fmt.Errorf("stat %s: %w", dir, err)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return 0, 0, err
	}

	// Phase 1: count files.
	var totalFiles int
	filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
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
			return nil
		}
		totalFiles++
		return nil
	})

	log.Printf("[scan %s] found %d indexable files", baseName, totalFiles)
	var processed, totalSections, skipped int
	currentSources := make(map[string]bool)

	// Phase 2: parse each file into wiki sections (skip unchanged via mtime).
	filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
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

		sections, parseErr := knowledge.ParseWikiFile(path, rel, 3)
		if parseErr != nil {
			log.Printf("[scan] skip %s: parse: %v", rel, parseErr)
			processed++
			if progress != nil {
				progress(processed, totalFiles)
			}
			return nil
		}

		for i := range sections {
			sections[i].Base = baseName
		}
		// Summarize via LLM if scan model configured.
		if base.ScanModel != "" {
			for i := range sections {
				summary, err := h.summarizeText(ctx, base, rel, sections[i].Content)
				if err != nil {
					log.Printf("[scan] summarize %s: %v", rel, err)
				} else {
					sections[i].Content = summary
				}
			}
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

	if skipped > 0 {
		log.Printf("[scan %s] skipped %d unchanged files", baseName, skipped)
	}

	// Clean up stale sources (files deleted since last scan).
	if err := store.RemoveStaleSources(ctx, baseName, currentSources); err != nil {
		log.Printf("[scan %s] stale cleanup: %v", baseName, err)
	}

	log.Printf("[scan %s] indexed %d sections (+%d skipped) in %d files", baseName, totalSections, skipped, processed)
	return processed, totalSections, nil
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
			defer f.Close()

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
						return filepath.SkipAll
					}
				}
			}
			return nil
		})
		if len(out) >= maxResults {
			break
		}
	}
	return out
}
