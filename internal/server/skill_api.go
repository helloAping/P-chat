package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/skill"
)

type skillResponse struct {
	Name           string              `json:"name"`
	Description    string              `json:"description"`
	Path           string              `json:"path"`
	Directory      string              `json:"directory,omitempty"`
	Scope          skill.Scope         `json:"scope,omitempty"`
	RequiredSkills []string            `json:"required_skills,omitempty"`
	RequiredBins   []string            `json:"required_bins,omitempty"`
	Resources      []string            `json:"resources,omitempty"`
	Shadowed       []skill.SkillSource `json:"shadowed,omitempty"`
	Content        string              `json:"content,omitempty"`
}

// ListSkills GET /api/v1/skills
func (h *Handler) ListSkills(c *gin.Context) {
	manager := skill.NewManager()
	catalog, err := manager.Catalog(c.Request.Context(), skill.CatalogQuery{ProjectRoot: h.skillsProjectRoot(c)})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]skillResponse, 0, len(catalog.Skills))
	for _, s := range catalog.Skills {
		out = append(out, skillResponse{
			Name:           s.Name,
			Description:    s.Description,
			Path:           s.Path,
			Directory:      s.Directory,
			Scope:          s.Scope,
			RequiredSkills: append([]string(nil), s.RequiredSkills...),
			RequiredBins:   append([]string(nil), s.RequiredBins...),
			Resources:      append([]string(nil), s.Resources...),
			Shadowed:       append([]skill.SkillSource(nil), s.Shadowed...),
		})
	}
	c.JSON(http.StatusOK, gin.H{"skills": out, "diagnostics": catalog.Diagnostics})
}

// GetSkill GET /api/v1/skills/:name
func (h *Handler) GetSkill(c *gin.Context) {
	name := c.Param("name")
	manager := skill.NewManager()
	loaded, err := manager.Load(c.Request.Context(), skill.LoadRequest{ProjectRoot: h.skillsProjectRoot(c), Name: name})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if len(loaded.Skills) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "skill not found"})
		return
	}
	primary := loaded.Skills[len(loaded.Skills)-1]
	c.JSON(http.StatusOK, gin.H{"skill": skillResponse{
		Name: primary.Name, Description: primary.Description, Path: primary.Path,
		Directory: primary.Directory, Scope: primary.Scope,
		RequiredSkills: append([]string(nil), primary.RequiredSkills...),
		RequiredBins:   append([]string(nil), primary.RequiredBins...),
		Resources:      append([]string(nil), primary.Resources...),
		Shadowed:       append([]skill.SkillSource(nil), primary.Shadowed...),
		Content:        primary.Content,
	}})
}

// DeleteSkill DELETE /api/v1/skills/:name
func (h *Handler) DeleteSkill(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "skill name is required"})
		return
	}
	if !validSkillName(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid skill name"})
		return
	}
	projectRoot := h.skillsProjectRoot(c)
	manager := skill.NewManager()
	catalog, err := manager.Catalog(c.Request.Context(), skill.CatalogQuery{ProjectRoot: projectRoot})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var selected *skill.SkillInfo
	for i := range catalog.Skills {
		if catalog.Skills[i].Name == name {
			selected = &catalog.Skills[i]
			break
		}
	}
	if selected == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "skill not found"})
		return
	}
	if selected.Scope != skill.ScopeGlobalManaged && selected.Scope != skill.ScopeProjectManaged {
		c.JSON(http.StatusConflict, gin.H{"error": "standard-directory Skills are read-only in P-Chat; import the package into a managed scope before removing it"})
		return
	}
	result, err := manager.Apply(c.Request.Context(), skill.ChangeRequest{
		Action: skill.ChangeRemove, Name: name, Scope: selected.Scope, ProjectRoot: projectRoot,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Reload agent skills so the system prompt updates.
	if h.agent != nil {
		h.agent.Reload()
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "result": result})
}

type installSkillRequest struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Scope       string `json:"scope,omitempty"` // "global" (default) | "project"
	ProjectPath string `json:"project_path,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	SourcePath  string `json:"source_path,omitempty"`
}

// InstallSkill 处理远程包或本地包/集合目录的 Skill 安装请求。
// InstallSkill handles remote packages or local package/collection directories.
func (h *Handler) InstallSkill(c *gin.Context) {
	var req installSkillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	req.SourcePath = strings.TrimSpace(req.SourcePath)
	sourceCount := 0
	for _, source := range []string{req.URL, req.SourcePath} {
		if source != "" {
			sourceCount++
		}
	}
	if sourceCount != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "exactly one of url or source_path is required"})
		return
	}
	name := req.Name
	if name == "" && req.URL != "" {
		name = inferSkillName(req.URL)
	}
	if name != "" && !validSkillName(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid skill name"})
		return
	}

	scope := skill.ScopeGlobalManaged
	projectRoot := req.ProjectPath
	if projectRoot == "" && req.SessionID != "" {
		projectRoot = h.sessionProjectPath(req.SessionID)
	}
	if req.Scope == "project" {
		scope = skill.ScopeProjectManaged
		if projectRoot == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "project_path or session_id is required for project skill install"})
			return
		}
	} else if req.Scope != "" && req.Scope != "global" {
		c.JSON(http.StatusBadRequest, gin.H{"error": `scope must be "global" or "project"`})
		return
	}
	change := skill.ChangeRequest{Name: name, Scope: scope, ProjectRoot: projectRoot}
	if req.SourcePath != "" {
		change.Action = skill.ChangeImport
		change.SourcePath = req.SourcePath
		change.Name = req.Name
	} else {
		change.Action = skill.ChangeInstall
		change.SourceURL = req.URL
	}
	result, err := skill.NewManager().Apply(c.Request.Context(), change)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if h.agent != nil {
		h.agent.Reload()
	}
	c.JSON(http.StatusCreated, gin.H{
		"ok": result.Ready, "ready": result.Ready, "name": result.Name,
		"path": result.Directory, "installed": result.Installed, "rolled_back": result.RolledBack,
		"diagnostics": result.Diagnostics,
	})
}

func (h *Handler) skillsProjectRoot(c *gin.Context) string {
	if sessionID := c.Query("session_id"); sessionID != "" {
		return h.sessionProjectPath(sessionID)
	}
	return c.Query("project_path")
}

func validSkillName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	return filepath.Base(name) == name
}

// inferSkillName extracts a skill name from a URL.
func inferSkillName(url string) string {
	// Try raw GitHub path: .../skills/name/SKILL.md
	if idx := strings.Index(url, "/skills/"); idx >= 0 {
		rest := url[idx+len("/skills/"):]
		if i := strings.Index(rest, "/"); i >= 0 {
			return rest[:i]
		}
	}
	// Try GitHub repo: github.com/user/repo
	if strings.Contains(url, "github.com") {
		parts := strings.Split(strings.TrimRight(url, "/"), "/")
		if len(parts) >= 2 {
			repo := parts[len(parts)-1]
			repo = strings.TrimSuffix(repo, ".git")
			return repo
		}
	}
	return ""
}

// fetchSkillContent downloads the SKILL.md (or README.md) from a
// remote URL. Tries SKILL.md first, then README.md, and both main
// and master branches.
func fetchSkillContent(url string) (string, error) {
	rawURL := toRawURL(url)
	// Try multiple branch + filename combinations.
	trials := []string{rawURL}
	if strings.Contains(rawURL, "/main/") {
		trials = append(trials, strings.Replace(rawURL, "/main/", "/master/", 1))
	}
	// Try README.md as fallback for each branch.
	for _, t := range append([]string{}, trials...) {
		if strings.HasSuffix(t, "SKILL.md") {
			trials = append(trials, strings.Replace(t, "SKILL.md", "README.md", 1))
		}
	}
	var lastErr error
	for _, t := range trials {
		resp, err := httpGet(t)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("read response: %w", err)
			continue
		}
		return string(body), nil
	}
	return "", fmt.Errorf("skill not found (tried %d URLs, last: %w)", len(trials), lastErr)
}

func httpGet(url string) (*http.Response, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// toRawURL converts a GitHub URL to a raw content URL.
func toRawURL(url string) string {
	if strings.Contains(url, "raw.githubusercontent.com") {
		return url
	}
	if !strings.Contains(url, "github.com") {
		return url
	}
	// github.com/user/repo → raw.githubusercontent.com/user/repo/main/SKILL.md
	rawURL := strings.Replace(url, "github.com", "raw.githubusercontent.com", 1)
	rawURL = strings.TrimRight(rawURL, "/")
	rawURL = strings.Replace(rawURL, "/blob/", "/", 1)
	// If the URL already points to a file, use it as-is after conversion.
	// Otherwise append /main/SKILL.md for repo-level searches.
	if !strings.HasSuffix(rawURL, ".md") && !strings.Contains(rawURL, "/raw/") {
		rawURL += "/main/SKILL.md"
	}
	return rawURL
}

// GET /api/v1/skills/search searches for skills in a GitHub repo.
// Expects ?q= query parameter (repo URL or search term).
func (h *Handler) SearchSkills(c *gin.Context) {
	q := c.Query("q")
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "q is required"})
		return
	}
	results, err := searchSkills(q)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

type searchResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

func searchSkills(q string) ([]searchResult, error) {
	// Search a GitHub repository for skill directories.
	// Uses GitHub API to list directories under the skills/ path.
	repoURL := q
	if !strings.Contains(repoURL, "github.com") {
		return nil, fmt.Errorf("only GitHub repositories are supported, e.g. https://github.com/user/repo")
	}
	repoURL = strings.TrimRight(repoURL, "/")
	repoURL = strings.TrimSuffix(repoURL, ".git")

	// Extract owner/repo from URL
	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid GitHub repository URL")
	}
	owner, repo := parts[0], parts[1]

	// Use GitHub API to list skills directory
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/skills", owner, repo)
	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d — 请确认仓库存在且包含 skills/ 目录", resp.StatusCode)
	}
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("parse API response: %w", err)
	}

	var results []searchResult
	for _, e := range entries {
		if e.Type != "dir" {
			continue
		}
		// Try to fetch the skill's description from its SKILL.md
		desc := ""
		rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/skills/%s/SKILL.md", owner, repo, e.Name)
		if d, err := fetchSkillContent(rawURL); err == nil {
			desc = extractFirstLine(d)
		}
		results = append(results, searchResult{
			Name:        e.Name,
			Description: desc,
			URL:         fmt.Sprintf("https://github.com/%s/%s/tree/main/skills/%s", owner, repo, e.Name),
		})
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no skill directories found in %s/%s/skills/", owner, repo)
	}
	return results, nil
}

func extractFirstLine(content string) string {
	lines := strings.Split(content, "\n")
	var para []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, trimmed)
	}
	if len(para) == 0 {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			return line
		}
		return ""
	}
	result := strings.Join(para, " ")
	if len(result) > 200 {
		result = result[:200] + "..."
	}
	return result
}

// --- Skill repos ---

type skillRepo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func skillReposPath() string {
	return filepath.Join(paths.GlobalDir(), "skill-repos.json")
}

func loadSkillRepos() ([]skillRepo, error) {
	data, err := os.ReadFile(skillReposPath())
	if err != nil {
		if os.IsNotExist(err) {
			// Seed with the official P-Chat skill repository.
			defaults := []skillRepo{
				{Name: "P-Chat 官方技能", URL: "https://github.com/p-chat-community/skills"},
			}
			_ = saveSkillRepos(defaults)
			return defaults, nil
		}
		return nil, err
	}
	var repos []skillRepo
	if err := json.Unmarshal(data, &repos); err != nil {
		return nil, err
	}
	return repos, nil
}

func saveSkillRepos(repos []skillRepo) error {
	if err := os.MkdirAll(paths.GlobalDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(repos, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(skillReposPath(), data, 0o644)
}

// ListSkillRepos GET /api/v1/skills/repos
func (h *Handler) ListSkillRepos(c *gin.Context) {
	repos, err := loadSkillRepos()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if repos == nil {
		repos = []skillRepo{}
	}
	c.JSON(http.StatusOK, gin.H{"repos": repos})
}

// AddSkillRepo POST /api/v1/skills/repos
func (h *Handler) AddSkillRepo(c *gin.Context) {
	var req skillRepo
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if req.Name == "" || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and url are required"})
		return
	}
	repos, err := loadSkillRepos()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, r := range repos {
		if r.URL == req.URL {
			c.JSON(http.StatusOK, gin.H{"repos": repos})
			return
		}
	}
	repos = append(repos, req)
	if err := saveSkillRepos(repos); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"repos": repos})
}

// RemoveSkillRepo DELETE /api/v1/skills/repos
func (h *Handler) RemoveSkillRepo(c *gin.Context) {
	var req struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	repos, err := loadSkillRepos()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	filtered := make([]skillRepo, 0, len(repos))
	for _, r := range repos {
		if r.URL != req.URL {
			filtered = append(filtered, r)
		}
	}
	if err := saveSkillRepos(filtered); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"repos": filtered})
}
