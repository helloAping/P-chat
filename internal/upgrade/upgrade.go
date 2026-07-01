package upgrade

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/p-chat/pchat/internal/paths"
)

// versionFilePath returns the path to the version tracking file.
func versionFilePath() string {
	return filepath.Join(paths.GlobalDir(), "version")
}

// readUserVersion returns the user's current AppVersion. If the version
// file doesn't exist, returns V0 (pre-upgrade-system).
func readUserVersion() AppVersion {
	data, err := os.ReadFile(versionFilePath())
	if err != nil {
		return V0
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || v < 0 {
		return V0
	}
	return AppVersion(v)
}

// writeUserVersion persists the given version to disk. Creates the
// .p-chat directory if it doesn't exist.
func writeUserVersion(v AppVersion) error {
	os.MkdirAll(paths.GlobalDir(), 0o755)
	return os.WriteFile(versionFilePath(), []byte(strconv.Itoa(int(v))), 0o644)
}

// Run executes any pending upgrade steps from the user's current
// version up to Current. Steps are applied sequentially; the version
// file is updated after each successful step so that a crash mid-way
// can resume from the last completed version on the next startup.
//
// Run MUST be called after the database is opened (so V2→V3 SQL
// migrations can execute) but before components that read from it
// (style manager, etc.).
func Run(db *sql.DB) error {
	from := readUserVersion()

	if from >= Current {
		return nil
	}

	if err := os.MkdirAll(paths.GlobalDir(), 0o755); err != nil {
		return fmt.Errorf("upgrade: create .p-chat dir: %w", err)
	}

	for v := from; v < Current; v++ {
		step, ok := steps[v]
		if !ok {
			return fmt.Errorf("upgrade: no step registered for V%d → V%d", v, v+1)
		}
		log.Printf("[upgrade] running V%d → V%d", v, v+1)
		if err := step(db); err != nil {
			return fmt.Errorf("upgrade V%d → V%d: %w", v, v+1, err)
		}
		if err := writeUserVersion(v + 1); err != nil {
			return fmt.Errorf("upgrade: write version V%d: %w", v+1, err)
		}
		log.Printf("[upgrade] V%d → V%d complete", v, v+1)
	}

	log.Printf("[upgrade] system at V%d", Current)
	return nil
}

// UserVersion returns the user's current AppVersion for diagnostics.
func UserVersion() AppVersion {
	return readUserVersion()
}

// SeedForTesting creates the styles table and inserts the built-in
// prompts. Use this in tests that need a ready-to-use styles table
// (e.g. agent tests with `:memory:` stores that don't go through
// the full upgrade.Run path).
func SeedForTesting(db *sql.DB) error {
	if err := createStylesTable(db); err != nil {
		return err
	}
	return seedBuiltins(db)
}
