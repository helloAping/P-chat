package upgrade

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	V0: stepV0toV1,
	V1: stepV1toV2,
	V2: stepV2toV3,
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
