package knowledge

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDB creates a fresh SQLite database and applies the same
// schema as memory.Store. Uses WAL mode so a single *sql.DB can serve
// concurrent reads and writes without SQLITE_BUSY errors.
func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source TEXT NOT NULL,
		content TEXT NOT NULL,
		metadata TEXT,
		created_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS embeddings (
		chunk_id INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
		model TEXT NOT NULL,
		vector BLOB NOT NULL,
		dim INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	return db
}
