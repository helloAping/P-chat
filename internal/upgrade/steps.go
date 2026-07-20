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
	V0: stepV0toV1,
	V1: stepV1toV2,
	V2: stepV2toV3,
	V3: stepV3toV4,
	V4: stepV4toV5,
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
