package upgrade

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/p-chat/pchat/internal/paths"
)

//go:embed prompts/*.md
var builtinFS embed.FS

// builtinLabels maps built-in style IDs to their display labels.
var builtinLabels = map[string]string{
	"cute":    "小P (PiPi)",
	"guofeng": "墨言 (MoYan)",
	"tech":    "NEXUS (零号)",
}

// steps maps each start version to its upgrade function.
var steps = map[AppVersion]func(*sql.DB) error{
	V0:  stepV0toV1,
	V1:  stepV1toV2,
	V2:  stepV2toV3,
	V3:  stepV3toV4,
	V4:  stepV4toV5,
	V5:  stepV5toV6,
	V6:  stepV6toV7,
	V7:  stepV7toV8,
	V8:  stepV8toV9,
	V9:  stepV9toV10,
	V10: stepV10toV11,
	V11: stepV11toV12,
	V12: stepV12toV13,
	V13: stepV13toV14,
	V14: stepV14toV15,
}

// resolvePromptDir returns the best-guess prompts directory for legacy import.
func resolvePromptDir() string {
	cwd, _ := os.Getwd()
	projectPrompts := filepath.Join(cwd, "prompts")
	if _, err := os.Stat(projectPrompts); err == nil {
		return projectPrompts
	}
	return filepath.Join(os.Getenv("USERPROFILE"), ".p-chat", "prompts")
}

// ---- V0 → V1 ----

func stepV0toV1(_ *sql.DB) error {
	// V0 users have no version file. The V0→V1 step just writes the
	// version file to establish a baseline. No data migration is
	// performed because V0 installs predate structured prompts.
	log.Print("[upgrade] V0 → V1: establishing baseline version")
	return nil
}

// ---- V1 → V2 ----

func stepV1toV2(_ *sql.DB) error {
	// Merge identity/ + soul/ → style/ for user-defined styles.
	// Built-in styles are skipped (V1 had them on disk too, but
	// V2→V3 will seed them from the embedded FS).
	log.Print("[upgrade] V1 → V2: merging identity/ + soul/ → style/")

	dir := resolvePromptDir()
	idDir := filepath.Join(dir, "identity")
	soDir := filepath.Join(dir, "soul")

	entries, err := os.ReadDir(idDir)
	if err != nil {
		// No identity dir — nothing to merge.
		return nil
	}

	styleDir := filepath.Join(dir, "style")
	os.MkdirAll(styleDir, 0o755)

	merged := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		if id == "" {
			continue
		}
		// Skip built-ins — V2→V3 seeds them from the binary.
		if id == "cute" || id == "guofeng" || id == "tech" {
			continue
		}
		// Already merged.
		if _, err := os.Stat(filepath.Join(styleDir, id+".md")); err == nil {
			continue
		}

		var parts []string
		if data, err := os.ReadFile(filepath.Join(idDir, id+".md")); err == nil {
			parts = append(parts, string(data))
		}
		if data, err := os.ReadFile(filepath.Join(soDir, id+".md")); err == nil {
			parts = append(parts, string(data))
		}
		if len(parts) == 0 {
			continue
		}
		prompt := strings.Join(parts, "\n\n---\n\n")
		if err := os.WriteFile(filepath.Join(styleDir, id+".md"), []byte(prompt), 0o644); err != nil {
			log.Printf("[upgrade] V1→V2: write style/%s.md: %v", id, err)
			continue
		}
		merged++
		log.Printf("[upgrade] V1→V2: merged %s (identity+soul → style)", id)
	}
	if merged > 0 {
		log.Printf("[upgrade] V1→V2: merged %d styles", merged)
	}
	return nil
}

// ---- V2 → V3 ----

func stepV2toV3(db *sql.DB) error {
	// 1. Create styles table (if not already done by memory migration).
	// 2. Seed built-in styles from embedded FS.
	// 3. Import user-defined styles from legacy prompts/ directory.
	log.Print("[upgrade] V2 → V3: migrating styles to SQLite")

	if err := createStylesTable(db); err != nil {
		return fmt.Errorf("create styles table: %w", err)
	}
	if err := seedBuiltins(db); err != nil {
		return fmt.Errorf("seed builtins: %w", err)
	}
	if err := importLegacyStyles(db); err != nil {
		return fmt.Errorf("import legacy styles: %w", err)
	}
	return nil
}

// ---- V7 → V8 ----

func stepV7toV8(db *sql.DB) error {
	// 创建持久化回合队列表。
	// Create the durable turn queue table.
	log.Print("[upgrade] V7 → V8: creating durable turn queue table")
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS turn_queue (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id       TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    status           TEXT NOT NULL DEFAULT 'queued',
    message          TEXT NOT NULL DEFAULT '',
    payload_json     TEXT NOT NULL DEFAULT '',
    client_msg_id    INTEGER NOT NULL DEFAULT 0,
    attachment_count INTEGER NOT NULL DEFAULT 0,
    error            TEXT NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    started_at       INTEGER,
    finished_at      INTEGER
);
CREATE INDEX IF NOT EXISTS idx_turn_queue_session_status ON turn_queue(session_id, status, id);`)
	if err != nil {
		return fmt.Errorf("create turn_queue: %w", err)
	}
	return nil
}

// ---- V8 → V9 ----

func stepV8toV9(db *sql.DB) error {
	log.Print("[upgrade] V8 → V9: migrating media capabilities")
	if err := migrateMediaCapabilityConfig(); err != nil {
		return fmt.Errorf("migrate media capability config: %w", err)
	}
	if err := migrateSessionRecognitionCapabilities(db); err != nil {
		return fmt.Errorf("migrate session recognition capabilities: %w", err)
	}
	return nil
}

// ---- V9 → V10 ----

func stepV9toV10(_ *sql.DB) error {
	log.Print("[upgrade] V9 → V10: typing models and initializing media generation config")
	if err := os.MkdirAll(paths.GeneratedDir(), 0o755); err != nil {
		return fmt.Errorf("create generated media directory: %w", err)
	}
	return migrateMediaGenerationConfig()
}

func migrateMediaGenerationConfig() error {
	configPath := paths.GlobalConfig()
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	changed := false
	if _, exists := root["generation"]; !exists {
		root["generation"] = map[string]any{"defaults": map[string]any{}}
		changed = true
	}
	if llmDoc, ok := root["llm"].(map[string]any); ok {
		if providers, ok := llmDoc["providers"].([]any); ok {
			for _, providerValue := range providers {
				provider, _ := providerValue.(map[string]any)
				if provider == nil {
					continue
				}
				if _, exists := provider["vendor"]; !exists {
					baseURL, _ := provider["base_url"].(string)
					name, _ := provider["name"].(string)
					probe := strings.ToLower(baseURL + " " + name)
					switch {
					case strings.Contains(probe, "volces.com"), strings.Contains(probe, "volcengine"), strings.Contains(probe, "doubao"):
						provider["vendor"] = "volcengine"
						changed = true
					case strings.Contains(probe, "minimax"):
						provider["vendor"] = "minimax"
						changed = true
					case strings.Contains(probe, "openai.com"):
						provider["vendor"] = "openai"
						changed = true
					}
				}
				models, _ := provider["models"].([]any)
				for _, modelValue := range models {
					model, _ := modelValue.(map[string]any)
					if model == nil {
						continue
					}
					if _, exists := model["type"]; !exists {
						model["type"] = "llm"
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeUpgradeFileAtomic(configPath, out, 0o644)
}

// ---- V10 → V11 ----

func stepV10toV11(_ *sql.DB) error {
	log.Print("[upgrade] V10 → V11: consolidating media model API configuration")
	return migrateSharedMediaGenerationAPIConfig()
}

func migrateSharedMediaGenerationAPIConfig() error {
	configPath := paths.GlobalConfig()
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	changed := false
	llmDoc, _ := root["llm"].(map[string]any)
	providers, _ := llmDoc["providers"].([]any)
	for _, providerValue := range providers {
		provider, _ := providerValue.(map[string]any)
		models, _ := provider["models"].([]any)
		for _, modelValue := range models {
			model, _ := modelValue.(map[string]any)
			generation, _ := model["generation"].(map[string]any)
			if generation == nil {
				continue
			}
			if _, exists := generation["api"]; exists {
				continue
			}
			operations, _ := generation["operations"].(map[string]any)
			if len(operations) == 0 {
				continue
			}

			// Only collapse legacy values when every capability used the same
			// API settings. Heterogeneous configs remain readable through the
			// compatibility path and are consolidated when the user edits them.
			var shared map[string]any
			compatible := true
			for _, value := range operations {
				operationConfig, _ := value.(map[string]any)
				if operationConfig == nil {
					operationConfig = map[string]any{}
				}
				if shared == nil {
					shared = make(map[string]any, len(operationConfig)+1)
					for key, item := range operationConfig {
						shared[key] = item
					}
					continue
				}
				if !generationAPIDocumentsEqual(shared, operationConfig) {
					compatible = false
					break
				}
			}
			if !compatible {
				continue
			}
			if shared == nil {
				shared = map[string]any{}
			}
			if _, exists := shared["timeout_seconds"]; !exists {
				shared["timeout_seconds"] = float64(600)
			}
			generation["api"] = shared
			for operation := range operations {
				operations[operation] = map[string]any{}
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeUpgradeFileAtomic(configPath, out, 0o644)
}

func generationAPIDocumentsEqual(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

// ---- V11 → V12 ----

func stepV11toV12(_ *sql.DB) error {
	log.Print("[upgrade] V11 → V12: migrating providers to Base URL plus per-model endpoint suffixes")
	return migrateModelAPIEndpoints()
}

func migrateModelAPIEndpoints() error {
	configPath := paths.GlobalConfig()
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	changed := false
	llmDoc, _ := root["llm"].(map[string]any)
	providers, _ := llmDoc["providers"].([]any)
	for _, providerValue := range providers {
		provider, _ := providerValue.(map[string]any)
		if provider == nil {
			continue
		}
		baseURL, _ := provider["base_url"].(string)
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		protocol, _ := provider["protocol"].(string)
		if protocol == "" {
			protocol, _ = provider["type"].(string)
		}
		if strings.TrimSpace(protocol) == "" {
			protocol = "openai"
		}
		if configuredProtocol, _ := provider["protocol"].(string); strings.TrimSpace(configuredProtocol) == "" {
			provider["protocol"] = protocol
			changed = true
		}
		apiURL, _ := provider["api_url"].(string)
		modelEndpoint := legacyDefaultModelEndpoint(protocol, baseURL)
		if strings.TrimSpace(apiURL) != "" {
			migratedBaseURL, migratedEndpoint := splitLegacyProviderAPIURL(protocol, apiURL)
			if baseURL == "" {
				baseURL = migratedBaseURL
			}
			if strings.HasPrefix(strings.TrimSpace(apiURL), baseURL) {
				migratedEndpoint = strings.TrimPrefix(strings.TrimSpace(apiURL), baseURL)
			}
			if migratedEndpoint != "" {
				modelEndpoint = normalizeEndpointSuffix(migratedEndpoint)
			}
			delete(provider, "api_url")
			changed = true
		}
		if baseURL != "" && provider["base_url"] != baseURL {
			provider["base_url"] = baseURL
			changed = true
		}

		models, _ := provider["models"].([]any)
		for _, modelValue := range models {
			model, _ := modelValue.(map[string]any)
			modelType, _ := model["type"].(string)
			if modelType == "" || modelType == "llm" {
				if endpoint, _ := model["api_endpoint"].(string); strings.TrimSpace(endpoint) == "" {
					model["api_endpoint"] = modelEndpoint
					changed = true
				} else if normalized := normalizeEndpointSuffix(endpoint); normalized != endpoint {
					model["api_endpoint"] = normalized
					changed = true
				}
			}
			generation, _ := model["generation"].(map[string]any)
			if generation == nil {
				continue
			}
			if apiConfig, ok := generation["api"].(map[string]any); ok {
				if makeGenerationEndpointsRelative(apiConfig, baseURL) {
					changed = true
				}
			}
			if operations, ok := generation["operations"].(map[string]any); ok {
				for _, operationValue := range operations {
					operationConfig, _ := operationValue.(map[string]any)
					if makeGenerationEndpointsRelative(operationConfig, baseURL) {
						changed = true
					}
				}
			}
		}
		// Vendor presets, legacy protocol aliases, and provider-level exact API
		// endpoints no longer participate in routing after V12.
		for _, legacyField := range []string{"vendor", "type", "api_url"} {
			if _, exists := provider[legacyField]; exists {
				delete(provider, legacyField)
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeUpgradeFileAtomic(configPath, out, 0o644)
}

// ---- V12 → V13 ----

func stepV12toV13(_ *sql.DB) error {
	log.Print("[upgrade] V12 → V13: initializing provider custom request headers")
	return migrateProviderCustomHeaders()
}

// ---- V13 → V14 ----

func stepV13toV14(db *sql.DB) error {
	log.Print("[upgrade] V13 → V14: creating reusable media context table")
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS media_contexts (
	    id                TEXT PRIMARY KEY,
	    session_id        TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
	    kind              TEXT NOT NULL,
	    tool_name         TEXT NOT NULL DEFAULT '',
	    input_refs_json   TEXT NOT NULL DEFAULT '[]',
	    output_refs_json  TEXT NOT NULL DEFAULT '[]',
	    prompt            TEXT NOT NULL DEFAULT '',
	    result_text       TEXT NOT NULL DEFAULT '',
	    summary           TEXT NOT NULL DEFAULT '',
	    structured_json   TEXT NOT NULL DEFAULT '',
	    context_refs_json TEXT NOT NULL DEFAULT '[]',
	    tool_call_id      TEXT NOT NULL DEFAULT '',
	    message_id        INTEGER NOT NULL DEFAULT 0,
	    regen_group_id    TEXT NOT NULL DEFAULT '',
	    archived          INTEGER NOT NULL DEFAULT 0,
	    archive_reason    TEXT NOT NULL DEFAULT '',
	    created_at        INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_media_contexts_session_created
	  ON media_contexts(session_id, archived, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_media_contexts_tool_call
	  ON media_contexts(session_id, tool_call_id);
	CREATE INDEX IF NOT EXISTS idx_media_contexts_regen_group
	  ON media_contexts(session_id, regen_group_id, archived);`)
	if err != nil {
		return fmt.Errorf("create media_contexts: %w", err)
	}
	return nil
}

// ---- V14 → V15 ----

func stepV14toV15(_ *sql.DB) error {
	log.Print("[upgrade] V14 → V15: initializing provider strategy ids")
	return migrateProviderStrategyIDs()
}

func migrateProviderCustomHeaders() error {
	configPath := paths.GlobalConfig()
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	llmDoc, _ := root["llm"].(map[string]any)
	providers, _ := llmDoc["providers"].([]any)
	changed := false
	for _, providerValue := range providers {
		provider, _ := providerValue.(map[string]any)
		if provider == nil {
			continue
		}
		if _, exists := provider["custom_headers"]; !exists {
			provider["custom_headers"] = map[string]any{}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeUpgradeFileAtomic(configPath, out, 0o644)
}

func migrateProviderStrategyIDs() error {
	configPath := paths.GlobalConfig()
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	llmDoc, _ := root["llm"].(map[string]any)
	providers, _ := llmDoc["providers"].([]any)
	changed := false
	for _, providerValue := range providers {
		provider, _ := providerValue.(map[string]any)
		if provider == nil {
			continue
		}
		if _, exists := provider["provider_id"]; !exists {
			provider["provider_id"] = "custom"
			changed = true
		}
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeUpgradeFileAtomic(configPath, out, 0o644)
}

func legacyDefaultModelEndpoint(protocol, baseURL string) string {
	if strings.EqualFold(strings.TrimSpace(protocol), "anthropic") {
		if strings.HasSuffix(strings.ToLower(strings.TrimRight(baseURL, "/")), "/v1") {
			return "/messages"
		}
		return "/v1/messages"
	}
	return "/chat/completions"
}

func splitLegacyProviderAPIURL(protocol, apiURL string) (string, string) {
	raw := strings.TrimSpace(apiURL)
	lower := strings.ToLower(strings.TrimRight(raw, "/"))
	candidates := []string{"/chat/completions"}
	if strings.EqualFold(strings.TrimSpace(protocol), "anthropic") {
		candidates = []string{"/v1/messages", "/messages"}
	}
	for _, suffix := range candidates {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimRight(raw[:len(strings.TrimRight(raw, "/"))-len(suffix)], "/"), suffix
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", legacyDefaultModelEndpoint(protocol, "")
	}
	baseURL := parsed.Scheme + "://" + parsed.Host
	endpoint := parsed.EscapedPath()
	if parsed.RawQuery != "" {
		endpoint += "?" + parsed.RawQuery
	}
	if endpoint == "" {
		endpoint = legacyDefaultModelEndpoint(protocol, baseURL)
	}
	return baseURL, endpoint
}

func normalizeEndpointSuffix(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	return "/" + strings.TrimLeft(endpoint, "/")
}

func makeGenerationEndpointsRelative(configDoc map[string]any, baseURL string) bool {
	if configDoc == nil {
		return false
	}
	changed := false
	for _, field := range []string{"endpoint", "query_endpoint"} {
		raw, _ := configDoc[field].(string)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		normalized := raw
		if strings.TrimSpace(baseURL) != "" && strings.HasPrefix(raw, strings.TrimRight(baseURL, "/")) {
			normalized = strings.TrimPrefix(raw, strings.TrimRight(baseURL, "/"))
		}
		normalized = normalizeEndpointSuffix(normalized)
		if normalized != raw {
			configDoc[field] = normalized
			changed = true
		}
	}
	return changed
}

func migrateMediaCapabilityConfig() error {
	configPath := paths.GlobalConfig()
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	changed := false
	recognition, _ := root["recognition"].(map[string]any)
	if recognition == nil {
		recognition = map[string]any{}
		root["recognition"] = recognition
		changed = true
	}
	routes, _ := recognition["routes"].(map[string]any)
	if routes == nil {
		routes = map[string]any{}
		recognition["routes"] = routes
		changed = true
	}
	if _, exists := routes["image"]; !exists {
		if legacy, ok := root["vision_recognition"].(map[string]any); ok {
			route := map[string]any{}
			for _, key := range []string{"enabled", "provider", "model", "timeout_seconds"} {
				if value, found := legacy[key]; found {
					route[key] = value
				}
			}
			if value, found := legacy["max_image_bytes"]; found {
				route["max_bytes"] = value
			}
			routes["image"] = route
			changed = true
		}
	}

	if llmDoc, ok := root["llm"].(map[string]any); ok {
		if providers, ok := llmDoc["providers"].([]any); ok {
			for _, providerValue := range providers {
				provider, _ := providerValue.(map[string]any)
				models, _ := provider["models"].([]any)
				for _, modelValue := range models {
					model, _ := modelValue.(map[string]any)
					caps, _ := model["capabilities"].(map[string]any)
					if caps == nil {
						continue
					}
					if _, exists := caps["input_modalities"]; exists {
						continue
					}
					modalities := make([]string, 0, 2)
					if enabled, _ := caps["supports_vision"].(bool); enabled {
						modalities = append(modalities, "image")
					}
					if enabled, _ := caps["supports_audio"].(bool); enabled {
						modalities = append(modalities, "audio")
					}
					if len(modalities) > 0 {
						caps["input_modalities"] = modalities
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeUpgradeFileAtomic(configPath, out, 0o644)
}

func writeUpgradeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".upgrade-config-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func migrateSessionRecognitionCapabilities(db *sql.DB) error {
	if db == nil {
		return nil
	}
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='conversations'`).Scan(&exists); err != nil || exists == 0 {
		return err
	}
	rows, err := db.Query(`SELECT id, metadata FROM conversations WHERE metadata <> ''`)
	if err != nil {
		return err
	}
	type update struct{ id, metadata string }
	updates := make([]update, 0)
	for rows.Next() {
		var id, metadata string
		if err := rows.Scan(&id, &metadata); err != nil {
			rows.Close()
			return err
		}
		var doc map[string]any
		if json.Unmarshal([]byte(metadata), &doc) != nil {
			continue
		}
		if _, exists := doc["enabled_recognition_capabilities"]; exists {
			continue
		}
		capabilities := []string{}
		if enabled, _ := doc["use_image_recognition"].(bool); enabled {
			capabilities = append(capabilities, "image")
		}
		doc["enabled_recognition_capabilities"] = capabilities
		encoded, err := json.Marshal(doc)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, update{id: id, metadata: string(encoded)})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := db.Exec(`UPDATE conversations SET metadata=? WHERE id=?`, item.metadata, item.id); err != nil {
			return err
		}
	}
	return nil
}

func createStylesTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS styles (
    id          TEXT PRIMARY KEY,
    label       TEXT NOT NULL DEFAULT '',
    prompt      TEXT NOT NULL DEFAULT '',
    memory      TEXT NOT NULL DEFAULT '',
    is_builtin  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')))`)
	return err
}

func seedBuiltins(db *sql.DB) error {
	entries, err := builtinFS.ReadDir("prompts")
	if err != nil {
		return fmt.Errorf("read embedded prompts: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".md")
		if id == "" {
			continue
		}
		data, err := builtinFS.ReadFile("prompts/" + e.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}
		isBuiltin := id == "cute" || id == "guofeng" || id == "tech"
		label := builtinLabels[id]
		if label == "" {
			label = id
		}
		_, err = db.Exec(
			`INSERT OR IGNORE INTO styles (id, label, prompt, memory, is_builtin, created_at, updated_at)
			 VALUES (?, ?, ?, '', ?, ?, ?)`,
			id, label, string(data), boolToInt(isBuiltin), now, now,
		)
		if err != nil {
			return fmt.Errorf("insert %s: %w", id, err)
		}
	}
	return nil
}

func importLegacyStyles(db *sql.DB) error {
	dir := resolvePromptDir()

	// V2 style/ directory
	styleDir := filepath.Join(dir, "style")
	if entries, err := os.ReadDir(styleDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".md")
			if id == "" || id == "cute" || id == "guofeng" || id == "tech" {
				continue
			}
			if styleExists(db, id) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(styleDir, e.Name()))
			if err != nil {
				log.Printf("[upgrade] V2→V3: read style/%s: %v", id, err)
				continue
			}
			if err := insertStyle(db, id, id, string(data), false); err != nil {
				log.Printf("[upgrade] V2→V3: insert %s: %v", id, err)
				continue
			}
			log.Printf("[upgrade] V2→V3: imported %s from style/", id)
		}
	}

	// V1 identity/ + soul/ (fallback)
	idDir := filepath.Join(dir, "identity")
	soDir := filepath.Join(dir, "soul")
	if entries, err := os.ReadDir(idDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".md")
			if id == "" || id == "cute" || id == "guofeng" || id == "tech" {
				continue
			}
			if styleExists(db, id) {
				continue
			}
			var parts []string
			if data, err := os.ReadFile(filepath.Join(idDir, id+".md")); err == nil {
				parts = append(parts, string(data))
			}
			if data, err := os.ReadFile(filepath.Join(soDir, id+".md")); err == nil {
				parts = append(parts, string(data))
			}
			if len(parts) == 0 {
				continue
			}
			prompt := strings.Join(parts, "\n\n---\n\n")
			if err := insertStyle(db, id, id, prompt, false); err != nil {
				log.Printf("[upgrade] V2→V3: insert %s: %v", id, err)
				continue
			}
			log.Printf("[upgrade] V2→V3: imported %s from identity+soul", id)
		}
	}
	return nil
}

func styleExists(db *sql.DB, id string) bool {
	var dummy int
	err := db.QueryRow(`SELECT 1 FROM styles WHERE id=?`, id).Scan(&dummy)
	return err == nil
}

func insertStyle(db *sql.DB, id, label, prompt string, builtin bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT OR IGNORE INTO styles (id, label, prompt, memory, is_builtin, created_at, updated_at)
		 VALUES (?, ?, ?, '', ?, ?, ?)`,
		id, label, prompt, boolToInt(builtin), now, now,
	)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- V3 → V4 ----
//
// Background. Before V4, internal/paths/devhome.go treated
// PCHAT_HOME as the data dir. install.ps1 -AddToPath writes
// PCHAT_HOME = <install dir> as a user env var. Result: any
// install with -AddToPath had memory + config written under
// the install directory, not under the user's $HOME/.p-chat.
//
// V4 fixes the resolution (PCHAT_HOME is now exclusively the
// install root; data dir override is PCHAT_DATA_HOME). But
// existing installs have data stranded at
// <PCHAT_HOME>/memory/. This step rescues it.
//
// Behaviour:
//
//   - No PCHAT_HOME set → nothing to do (the bug never bit
//     this user; their data is already at ~/.p-chat/).
//   - PCHAT_HOME set, <PCHAT_HOME>/memory/ empty / missing
//     → nothing to do (clean install, or they wiped it).
//   - PCHAT_HOME set, <PCHAT_HOME>/memory/ has data,
//     ~/.p-chat/memory/ empty → move the directory across.
//     Idempotent: on a re-run, source will be gone so the
//     step is a no-op.
//   - PCHAT_HOME set, BOTH have data → CONFLICT. Don't touch
//     either side. Log a warning with the exact paths so the
//     user can manually consolidate (typically: keep the
//     larger / more-recent one, delete the other).
//
// The step is intentionally best-effort: it logs and
// returns nil on the conflict path, so a botched install
// doesn't prevent the rest of the upgrade from running.
func stepV3toV4(_ *sql.DB) error {
	log.Print("[upgrade] V3 → V4: rescue install-dir data into ~/.p-chat/")

	installDir := strings.TrimSpace(os.Getenv("PCHAT_HOME"))
	if installDir == "" {
		log.Print("[upgrade] V3→V4: PCHAT_HOME not set, nothing to migrate")
		return nil
	}

	// Defensive: some Windows shells leave the env var with
	// trailing whitespace or a stray quote. Resolve to an
	// absolute, cleaned-up path before using.
	installDir = filepath.Clean(installDir)
	if !filepath.IsAbs(installDir) {
		// install.ps1 always writes an absolute path; if we
		// got a relative one something's off, but don't
		// refuse to start over it — just skip the migration
		// and log so the user knows.
		log.Printf("[upgrade] V3→V4: PCHAT_HOME=%q is not absolute, skipping migration", installDir)
		return nil
	}

	src := filepath.Join(installDir, "memory")
	dst := paths.MemoryDir() // = ~/.p-chat/memory/ under the new resolution

	// Idempotency: if the source is already gone, we either
	// already ran this step or the install was always clean.
	// Either way, nothing to do.
	if _, err := os.Stat(src); os.IsNotExist(err) {
		log.Printf("[upgrade] V3→V4: source %s does not exist, nothing to migrate", src)
		return nil
	}

	// Conflict: both locations have data. Don't clobber.
	// Use store.db as the existence probe — it's the
	// canonical "the user has data here" signal. (WAL/SHM
	// may exist as zero-byte stragglers from a previous
	// open connection; not interesting on their own.)
	dstDB := filepath.Join(dst, "store.db")
	if _, err := os.Stat(dstDB); err == nil {
		log.Printf("[upgrade] V3→V4: CONFLICT — both %s and %s have a SQLite store.db. "+
			"Keeping both; please consolidate manually (e.g. inspect with sqlite3 and "+
			"delete the smaller / older one).", src, dst)
		return nil
	}

	// Move the entire memory/ directory: store.db + .wal +
	// .shm + any .backup-* snapshots. os.Rename on the
	// directory itself is atomic on the same volume (the
	// usual case — both dirs are on the system drive) and
	// fast for large stores.
	//
	// Cross-volume fallback: when the install dir lives on
	// a different drive than HOME (e.g. install on D:\,
	// user home on C:\), os.Rename returns
	// ERROR_NOT_SAME_DEVICE / EXDEV. We then copy the
	// tree and remove the source.
	if err := os.Rename(src, dst); err != nil {
		log.Printf("[upgrade] V3→V4: rename %s → %s failed (%v), "+
			"trying copy+delete (cross-volume fallback)", src, dst, err)

		if err := copyDir(src, dst); err != nil {
			log.Printf("[upgrade] V3→V4: copy %s → %s also failed: %v. "+
				"Data is still at the install dir; you can copy it manually to %s.",
				src, dst, err, dst)
			// Clean up any partial dest so next run doesn't
			// hit the CONFLICT probe with a broken store.db.
			os.RemoveAll(dst)
			return nil
		}
		if err := os.RemoveAll(src); err != nil {
			log.Printf("[upgrade] V3→V4: copied to %s but could not remove source %s: %v (non-fatal)",
				dst, src, err)
		}
		log.Printf("[upgrade] V3→V4: copied %s → %s (cross-volume)", src, dst)
		return nil
	}

	log.Printf("[upgrade] V3→V4: moved %s → %s (install-dir memory rescued into user home)", src, dst)
	return nil
}

// copyDir recursively copies src into dst. Used as the
// cross-volume fallback for stepV3toV4 when os.Rename
// fails with EXDEV / ERROR_NOT_SAME_DEVICE.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("readdir %s: %w", src, err)
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", srcPath, err)
		}
		fi, err := os.Stat(srcPath)
		mode := os.FileMode(0o644)
		if err == nil {
			mode = fi.Mode()
		}
		if err := os.WriteFile(dstPath, data, mode); err != nil {
			return fmt.Errorf("write %s: %w", dstPath, err)
		}
	}
	return nil
}

// ---- V4 → V5 ----

func stepV4toV5(_ *sql.DB) error {
	log.Print("[upgrade] V4 → V5: add work_mode config metadata (noop)")
	return nil
}

// ---- V5 → V6 ----

func stepV5toV6(_ *sql.DB) error {
	log.Print("[upgrade] V5 → V6: add vision_recognition config metadata (noop)")
	return nil
}

// ---- V6 → V7 ----

func stepV6toV7(_ *sql.DB) error {
	log.Print("[upgrade] V6 → V7: add per-session sub-agent model metadata (noop)")
	return nil
}
