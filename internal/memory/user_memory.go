package memory

import (
	"database/sql"
	"fmt"
)

// EnsureUserMemories creates the retired global card table.
// Upgrade V22 still calls it. V25 drops the table.
func EnsureUserMemories(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS user_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    text TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'manual',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
)`)
	if err != nil {
		return fmt.Errorf("create user_memories: %w", err)
	}
	return nil
}

// EnsureMorningDigests creates the retired digest schedule table.
// Upgrade V23 still calls it. V25 drops the table.
func EnsureMorningDigests(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS morning_digests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT NOT NULL UNIQUE,
    weekdays TEXT NOT NULL DEFAULT '1,2,3,4,5',
    time_of_day TEXT NOT NULL DEFAULT '09:00',
    style TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 0,
    last_fired TEXT NOT NULL DEFAULT '',
    last_missed TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
)`)
	if err != nil {
		return fmt.Errorf("create morning_digests: %w", err)
	}
	return nil
}

// DropRetiredDelightTables removes the global memory cards and morning digest
// schedule. Facts now live on speaking styles.
func DropRetiredDelightTables(db *sql.DB) error {
	if db == nil {
		return nil
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS user_memories`); err != nil {
		return fmt.Errorf("drop user_memories: %w", err)
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS morning_digests`); err != nil {
		return fmt.Errorf("drop morning_digests: %w", err)
	}
	return nil
}
