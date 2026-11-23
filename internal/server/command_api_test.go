package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/paths"
)

func TestSessionInstructionObservationsUseSessionProjectRoot(t *testing.T) {
	s, _ := newTestServer(t)
	projectRoot := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(projectRoot, ".p-chat", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.GlobalRulesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.GlobalRulesDir(), "global.md"), []byte("global rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".p-chat", "rules", "project.md"), []byte("project rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.GlobalAgents(), []byte("global instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, ".p-chat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".p-chat", "AGENTS.md"), []byte("project fallback"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "AGENTS.md"), []byte("project root"), 0o644); err != nil {
		t.Fatal(err)
	}

	session, status := postSessionBody(t, s, map[string]any{"project_path": projectRoot})
	if status != http.StatusCreated {
		t.Fatalf("create session status = %d", status)
	}

	rulesReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+session.ID+"/rules", nil)
	rulesResp := httptest.NewRecorder()
	s.Engine().ServeHTTP(rulesResp, rulesReq)
	if rulesResp.Code != http.StatusOK {
		t.Fatalf("rules status = %d, body=%s", rulesResp.Code, rulesResp.Body.String())
	}
	var rulesBody struct {
		Rules []RuleObservation `json:"rules"`
	}
	if err := json.NewDecoder(rulesResp.Body).Decode(&rulesBody); err != nil {
		t.Fatal(err)
	}
	if len(rulesBody.Rules) != 2 || rulesBody.Rules[0].Scope != "global" || rulesBody.Rules[1].Scope != "project" {
		t.Fatalf("rules = %+v, want global + project", rulesBody.Rules)
	}

	agentsReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+session.ID+"/agents", nil)
	agentsResp := httptest.NewRecorder()
	s.Engine().ServeHTTP(agentsResp, agentsReq)
	if agentsResp.Code != http.StatusOK {
		t.Fatalf("agents status = %d, body=%s", agentsResp.Code, agentsResp.Body.String())
	}
	var agentsBody AgentsObservation
	if err := json.NewDecoder(agentsResp.Body).Decode(&agentsBody); err != nil {
		t.Fatal(err)
	}
	if len(agentsBody.Candidates) != 3 || !agentsBody.Candidates[0].Selected {
		t.Fatalf("agents candidates = %+v, want project root selected", agentsBody.Candidates)
	}
	if !strings.Contains(agentsBody.Prompt, "project root") || strings.Contains(agentsBody.Prompt, "project fallback") || strings.Contains(agentsBody.Prompt, "global instructions") {
		t.Fatalf("agents prompt = %q, want only project root", agentsBody.Prompt)
	}

	for _, tc := range []struct {
		name string
		want string
		not  string
	}{
		{name: "rules", want: "project", not: "other-project"},
		{name: "agents", want: "project root", not: "global instructions"},
	} {
		body, err := json.Marshal(RunCommandRequest{SessionID: session.ID})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/commands/"+tc.name, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		s.Engine().ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("/%s command status = %d, body=%s", tc.name, resp.Code, resp.Body.String())
		}
		var result CommandResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Output, tc.want) || strings.Contains(result.Output, tc.not) {
			t.Fatalf("/%s output = %q", tc.name, result.Output)
		}
	}

	noSession := httptest.NewRequest(http.MethodPost, "/api/v1/commands/agents", strings.NewReader(`{"args":""}`))
	noSession.Header.Set("Content-Type", "application/json")
	noSessionResp := httptest.NewRecorder()
	s.Engine().ServeHTTP(noSessionResp, noSession)
	if noSessionResp.Code != http.StatusBadRequest {
		t.Fatalf("agents command without session status = %d, want 400", noSessionResp.Code)
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/missing/agents", nil)
	missingResp := httptest.NewRecorder()
	s.Engine().ServeHTTP(missingResp, missingReq)
	if missingResp.Code != http.StatusNotFound {
		t.Fatalf("missing session status = %d, want 404", missingResp.Code)
	}
}
