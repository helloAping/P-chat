package upgrade

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRun_CurrentVersion(t *testing.T) {
	// Simulate a fresh install at Current version: V0→V3 all at once.
	orig := versionFilePath()

	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)

	db, err := sql.Open("sqlite", filepath.Join(dir, ".p-chat", "test.db")+
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Remove existing version file from temp dir.
	_ = os.Remove(versionFilePath())

	if err := Run(db); err != nil {
		t.Fatalf("Run from V0: %v", err)
	}

	// Verify version file was written.
	if v := readUserVersion(); v != Current {
		t.Errorf("expected version %d, got %d", Current, v)
	}

	// Verify styles table has built-in rows.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM styles WHERE is_builtin=1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 3 {
		t.Errorf("expected >=3 built-in styles, got %d", count)
	}

	// Re-run should be a no-op.
	if err := Run(db); err != nil {
		t.Fatalf("Run again (should be no-op): %v", err)
	}

	// Restore original env if test modified a real env var.
	_ = orig
}

func TestRun_Idempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)

	db, err := sql.Open("sqlite", filepath.Join(dir, ".p-chat", "test.db")+
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_ = os.Remove(versionFilePath())

	// First run.
	if err := Run(db); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// Second run: should be idempotent.
	if err := Run(db); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	// Third run.
	if err := Run(db); err != nil {
		t.Fatalf("third Run: %v", err)
	}
}

func TestRun_V7ToV8CreatesTurnQueue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)

	db, err := sql.Open("sqlite", filepath.Join(dir, ".p-chat", "test.db")+
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := writeUserVersion(V7); err != nil {
		t.Fatalf("write V7: %v", err)
	}
	if err := Run(db); err != nil {
		t.Fatalf("Run from V7: %v", err)
	}
	if v := readUserVersion(); v != Current {
		t.Fatalf("version = %d, want Current", v)
	}
	var tableName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='turn_queue'`).Scan(&tableName); err != nil {
		t.Fatalf("turn_queue table missing: %v", err)
	}
	var indexName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_turn_queue_session_status'`).Scan(&indexName); err != nil {
		t.Fatalf("turn_queue index missing: %v", err)
	}
}

func TestRun_V8ToV9MigratesMediaCapabilities(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)

	configPath := filepath.Join(dir, ".p-chat", "config.json")
	legacyConfig := `{
  "llm": {"providers": [{"name": "p", "models": [
    {"name": "vision", "capabilities": {"supports_vision": true}},
    {"name": "audio", "capabilities": {"supports_audio": true}}
  ]}]},
  "vision_recognition": {"enabled": true, "provider": "p", "model": "vision", "timeout_seconds": 45, "max_image_bytes": 1234}
}`
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, ".p-chat", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, metadata TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations (id, metadata) VALUES ('s1', '{"use_image_recognition":true}')`); err != nil {
		t.Fatal(err)
	}
	if err := writeUserVersion(V8); err != nil {
		t.Fatal(err)
	}

	if err := Run(db); err != nil {
		t.Fatalf("Run from V8: %v", err)
	}
	if err := stepV8toV9(db); err != nil {
		t.Fatalf("second V9 migration should be idempotent: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var configDoc map[string]any
	if err := json.Unmarshal(data, &configDoc); err != nil {
		t.Fatal(err)
	}
	recognition := configDoc["recognition"].(map[string]any)
	routes := recognition["routes"].(map[string]any)
	imageRoute := routes["image"].(map[string]any)
	if imageRoute["provider"] != "p" || imageRoute["model"] != "vision" {
		t.Fatalf("image route = %#v", imageRoute)
	}

	providers := configDoc["llm"].(map[string]any)["providers"].([]any)
	models := providers[0].(map[string]any)["models"].([]any)
	visionCaps := models[0].(map[string]any)["capabilities"].(map[string]any)
	audioCaps := models[1].(map[string]any)["capabilities"].(map[string]any)
	if got := visionCaps["input_modalities"].([]any); len(got) != 1 || got[0] != "image" {
		t.Fatalf("vision modalities = %#v", got)
	}
	if got := audioCaps["input_modalities"].([]any); len(got) != 1 || got[0] != "audio" {
		t.Fatalf("audio modalities = %#v", got)
	}

	var metadata string
	if err := db.QueryRow(`SELECT metadata FROM conversations WHERE id='s1'`).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		t.Fatal(err)
	}
	if got := meta["enabled_recognition_capabilities"].([]any); len(got) != 1 || got[0] != "image" {
		t.Fatalf("session capabilities = %#v", got)
	}
	if v := readUserVersion(); v != Current {
		t.Fatalf("version = %d, want Current", v)
	}
}

func TestRun_V9ToV10TypesLegacyModelsAndInitializesGeneration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)

	configPath := filepath.Join(dir, ".p-chat", "config.json")
	legacyConfig := `{"llm":{"providers":[{"name":"minimax","base_url":"https://api.minimax.io","models":[{"name":"chat"}]}]}}`
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, ".p-chat", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := writeUserVersion(V9); err != nil {
		t.Fatal(err)
	}

	if err := Run(db); err != nil {
		t.Fatalf("Run from V9: %v", err)
	}
	if err := stepV9toV10(db); err != nil {
		t.Fatalf("second V10 migration should be idempotent: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	provider := doc["llm"].(map[string]any)["providers"].([]any)[0].(map[string]any)
	model := provider["models"].([]any)[0].(map[string]any)
	if model["type"] != "llm" || provider["vendor"] != "minimax" {
		t.Fatalf("migrated provider/model = %#v / %#v", provider, model)
	}
	if _, ok := doc["generation"].(map[string]any); !ok {
		t.Fatalf("generation config = %#v", doc["generation"])
	}
	if v := readUserVersion(); v != Current {
		t.Fatalf("version = %d, want Current", v)
	}
}

func TestRun_V10ToV11ConsolidatesIdenticalMediaModelAPIs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)

	configPath := filepath.Join(dir, ".p-chat", "config.json")
	legacyConfig := `{
  "llm": {"providers": [{"name":"media","models":[{
    "name":"video","type":"media_generation","generation":{"adapter":"minimax","operations":{
      "text_to_video":{"endpoint":"/v1/video_generation","query_endpoint":"/v1/query/video_generation?task_id={task_id}","timeout_seconds":600},
      "image_to_video":{"endpoint":"/v1/video_generation","query_endpoint":"/v1/query/video_generation?task_id={task_id}","timeout_seconds":600}
    }}
  }]}]}
}`
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, ".p-chat", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := writeUserVersion(V10); err != nil {
		t.Fatal(err)
	}

	if err := Run(db); err != nil {
		t.Fatalf("Run from V10: %v", err)
	}
	if err := stepV10toV11(db); err != nil {
		t.Fatalf("second V11 migration should be idempotent: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	provider := doc["llm"].(map[string]any)["providers"].([]any)[0].(map[string]any)
	model := provider["models"].([]any)[0].(map[string]any)
	generation := model["generation"].(map[string]any)
	apiConfig := generation["api"].(map[string]any)
	if apiConfig["endpoint"] != "/v1/video_generation" || apiConfig["timeout_seconds"] != float64(600) {
		t.Fatalf("shared generation api = %#v", apiConfig)
	}
	operations := generation["operations"].(map[string]any)
	for operation, value := range operations {
		if len(value.(map[string]any)) != 0 {
			t.Fatalf("operation %s still duplicates api settings: %#v", operation, value)
		}
	}
	if v := readUserVersion(); v != Current {
		t.Fatalf("version = %d, want Current", v)
	}
}

func TestV11MigrationPreservesHeterogeneousLegacyOperationAPIs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)
	configPath := filepath.Join(dir, ".p-chat", "config.json")
	legacyConfig := `{"llm":{"providers":[{"name":"media","models":[{"name":"omni","type":"media_generation","generation":{"operations":{"text_to_image":{"endpoint":"/images"},"text_to_video":{"endpoint":"/videos"}}}}]}]}}`
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := stepV10toV11(nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	provider := doc["llm"].(map[string]any)["providers"].([]any)[0].(map[string]any)
	model := provider["models"].([]any)[0].(map[string]any)
	generation := model["generation"].(map[string]any)
	if _, exists := generation["api"]; exists {
		t.Fatalf("heterogeneous operation APIs were collapsed: %#v", generation)
	}
	operations := generation["operations"].(map[string]any)
	if operations["text_to_image"].(map[string]any)["endpoint"] != "/images" || operations["text_to_video"].(map[string]any)["endpoint"] != "/videos" {
		t.Fatalf("legacy operation APIs changed: %#v", operations)
	}
}

func TestRun_V11ToV12MigratesModelAPIEndpointSuffixes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)
	configPath := filepath.Join(dir, ".p-chat", "config.json")
	legacyConfig := `{
  "llm": {"providers": [
    {"name":"ark","vendor":"volcengine","protocol":"openai","base_url":"https://ark.example/api/v3","models":[
      {"name":"chat","type":"llm"},
      {"name":"image","type":"media_generation","generation":{"api":{"endpoint":"/images/generations","query_endpoint":"/tasks/{task_id}"},"operations":{"text_to_image":{}}}}
    ]},
    {"name":"claude","protocol":"anthropic","base_url":"https://api.anthropic.com","models":[{"name":"sonnet"}]},
    {"name":"transition","protocol":"openai","api_url":"https://proxy.example/api/v3/responses","models":[{"name":"custom"}]}
  ]}
}`
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, ".p-chat", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := writeUserVersion(V11); err != nil {
		t.Fatal(err)
	}

	if err := Run(db); err != nil {
		t.Fatalf("Run from V11: %v", err)
	}
	if err := stepV11toV12(db); err != nil {
		t.Fatalf("second V12 migration should be idempotent: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	providers := doc["llm"].(map[string]any)["providers"].([]any)
	ark := providers[0].(map[string]any)
	if ark["base_url"] != "https://ark.example/api/v3" {
		t.Fatalf("ark base_url = %#v", ark["base_url"])
	}
	for _, legacyField := range []string{"vendor", "api_url", "type"} {
		if _, exists := ark[legacyField]; exists {
			t.Fatalf("legacy provider field %q was not removed: %#v", legacyField, ark)
		}
	}
	chat := ark["models"].([]any)[0].(map[string]any)
	if chat["api_endpoint"] != "/chat/completions" {
		t.Fatalf("chat api_endpoint = %#v", chat["api_endpoint"])
	}
	media := ark["models"].([]any)[1].(map[string]any)["generation"].(map[string]any)["api"].(map[string]any)
	if media["endpoint"] != "/images/generations" || media["query_endpoint"] != "/tasks/{task_id}" {
		t.Fatalf("media endpoints = %#v", media)
	}
	claude := providers[1].(map[string]any)
	if claude["base_url"] != "https://api.anthropic.com" {
		t.Fatalf("anthropic base_url = %#v", claude["base_url"])
	}
	sonnet := claude["models"].([]any)[0].(map[string]any)
	if sonnet["api_endpoint"] != "/v1/messages" {
		t.Fatalf("anthropic api_endpoint = %#v", sonnet["api_endpoint"])
	}
	transition := providers[2].(map[string]any)
	if transition["base_url"] != "https://proxy.example" {
		t.Fatalf("transition base_url = %#v", transition["base_url"])
	}
	custom := transition["models"].([]any)[0].(map[string]any)
	if custom["api_endpoint"] != "/api/v3/responses" {
		t.Fatalf("transition api_endpoint = %#v", custom["api_endpoint"])
	}
	if v := readUserVersion(); v != Current {
		t.Fatalf("version = %d, want Current", v)
	}
}

func TestUserVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755)
	_ = os.Remove(versionFilePath())

	// V0 when no file.
	if v := UserVersion(); v != V0 {
		t.Errorf("expected V0, got %d", v)
	}
}
