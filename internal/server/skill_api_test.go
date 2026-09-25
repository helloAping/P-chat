package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestListSkillsUsesSessionProjectRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	projectRoot := t.TempDir()
	writeTestSkill(t, filepath.Join(projectRoot, ".p-chat", "skills"), "project-only", "# project-only\n\nProject skill.\n")

	h := &Handler{
		meta: map[string]sessionMeta{
			"s1": {ProjectPath: projectRoot},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills?session_id=s1", nil)
	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	h.ListSkills(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Skills []skillResponse `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Skills) != 1 || body.Skills[0].Name != "project-only" {
		t.Fatalf("skills = %+v, want project-only", body.Skills)
	}
}

func TestInstallSkillRejectsAmbiguousSources(t *testing.T) {
	body := []byte(`{"name":"docs-tool","url":"https://github.com/example/skills","source_path":"C:/exports/skills"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/install", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	(&Handler{}).InstallSkill(c)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "exactly one") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestInstallSkillTrimsSourcesBeforeDispatch(t *testing.T) {
	body := []byte(`{"name":"docs-tool","url":"https://example.com/skill","source_path":"   "}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills/install", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	(&Handler{}).InstallSkill(c)

	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "GitHub") {
		t.Fatalf("status = %d, body = %s, want URL source dispatch", rec.Code, rec.Body.String())
	}
}

func writeTestSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
