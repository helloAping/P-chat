package memory

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"time"
)

// Migration 代表一次可逆的 schema 变更。
// Up 是升级 SQL，Down 是回滚 SQL。每条迁移在自身事务内执行。
type Migration struct {
	Version int
	Name    string
	Up      string
	Down    string
}

// 所有迁移按版本号升序排列。每条迁移的 Up 必须在空白数据库上
// 和已有数据库上都能安全执行（幂等）。
var allMigrations = []Migration{
	{
		Version: 1,
		Name:    "init",
		Up: `
CREATE TABLE IF NOT EXISTS conversations (
    id          TEXT PRIMARY KEY,
    title       TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    metadata    TEXT
);
CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role            TEXT NOT NULL,
    content         TEXT NOT NULL,
    tokens          INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    metadata        TEXT
);
CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id, id);
CREATE TABLE IF NOT EXISTS summaries (
    conversation_id TEXT NOT NULL,
    range_start     INTEGER NOT NULL,
    range_end       INTEGER NOT NULL,
    summary         TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (conversation_id, range_start)
);
CREATE TABLE IF NOT EXISTS chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source      TEXT NOT NULL,
    content     TEXT NOT NULL,
    metadata    TEXT,
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(source);
CREATE TABLE IF NOT EXISTS embeddings (
    chunk_id   INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
    model      TEXT NOT NULL,
    vector     BLOB NOT NULL,
    dim        INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS todo_items (
    session_id TEXT NOT NULL,
    item_id    TEXT NOT NULL,
    content    TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending',
    sort_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (session_id, item_id)
);
CREATE INDEX IF NOT EXISTS idx_todo_session ON todo_items(session_id);
`,
		Down: `
DROP INDEX IF EXISTS idx_todo_session;
DROP TABLE IF EXISTS todo_items;
DROP TABLE IF EXISTS embeddings;
DROP INDEX IF EXISTS idx_chunks_source;
DROP TABLE IF EXISTS chunks;
DROP TABLE IF EXISTS summaries;
DROP INDEX IF EXISTS idx_messages_conv;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS conversations;
`,
	},
	{
		Version: 2,
		Name:    "add_archived",
		Up:      `ALTER TABLE conversations ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`,
		Down:    `ALTER TABLE conversations DROP COLUMN archived`,
	},
	{
		Version: 3,
		Name:    "add_styles",
		Up: `
CREATE TABLE IF NOT EXISTS styles (
    id          TEXT PRIMARY KEY,
    label       TEXT NOT NULL DEFAULT '',
    prompt      TEXT NOT NULL DEFAULT '',
    memory      TEXT NOT NULL DEFAULT '',
    is_builtin  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);`,
		Down: `DROP TABLE IF EXISTS styles`,
	},
	{
		Version: 4,
		Name:    "add_vector_store",
		Up:      `ALTER TABLE conversations ADD COLUMN vector_store TEXT NOT NULL DEFAULT ''`,
		Down:    `ALTER TABLE conversations DROP COLUMN vector_store`,
	},
	{
		Version: 5,
		Name:    "add_msg_type_columns",
		Up: `
ALTER TABLE messages ADD COLUMN msg_type       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN submit_to_llm   INTEGER NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_messages_llm ON messages(conversation_id, submit_to_llm, id);`,
		Down: `
DROP INDEX IF EXISTS idx_messages_llm;
ALTER TABLE messages DROP COLUMN submit_to_llm;
ALTER TABLE messages DROP COLUMN msg_type;`,
	},
	{
		Version: 6,
		Name:    "backfill_msg_type",
		Up: `
-- text messages (user / assistant): msg_type=0, submit_to_llm=1
UPDATE messages SET msg_type=0, submit_to_llm=1
 WHERE (metadata LIKE '%"type":"text"%' OR metadata NOT LIKE '%"type":"%')
   AND (role='user' OR role='assistant');

-- system messages: msg_type=0, submit_to_llm=0
UPDATE messages SET msg_type=0, submit_to_llm=0
 WHERE (metadata LIKE '%"type":"text"%' OR metadata NOT LIKE '%"type":"%')
   AND role='system';

-- image: msg_type=1, submit_to_llm=1
UPDATE messages SET msg_type=1, submit_to_llm=1
 WHERE metadata LIKE '%"type":"image"%';

-- audio: msg_type=2, submit_to_llm=1
UPDATE messages SET msg_type=2, submit_to_llm=1
 WHERE metadata LIKE '%"type":"audio"%';

-- video: msg_type=3, submit_to_llm=1
UPDATE messages SET msg_type=3, submit_to_llm=1
 WHERE metadata LIKE '%"type":"video"%';

-- tool_call: msg_type=4, submit_to_llm=1
UPDATE messages SET msg_type=4, submit_to_llm=1
 WHERE metadata LIKE '%"type":"tool_call"%';

-- tool_result for exec_command: msg_type=5, submit_to_llm=0
UPDATE messages SET msg_type=5, submit_to_llm=0
 WHERE metadata LIKE '%"type":"tool_result"%' AND metadata LIKE '%"tool_name":"exec_command"%';

-- tool_result (other tools): msg_type=4, submit_to_llm=1
UPDATE messages SET msg_type=4, submit_to_llm=1
 WHERE metadata LIKE '%"type":"tool_result"%' AND metadata NOT LIKE '%"tool_name":"exec_command"%';

-- thinking: msg_type=0, submit_to_llm=0
UPDATE messages SET msg_type=0, submit_to_llm=0
 WHERE metadata LIKE '%"type":"thinking"%';`,
		Down: `UPDATE messages SET msg_type=0, submit_to_llm=1;`,
	},
	{
		// Migration 7: strip the `streaming` flag from
		// every persisted parts blob. The flag is a live-UI
		// signal only (drives the "思考中…" spinner /
		// caret while the LLM is still emitting) and must
		// not survive into reload — the persisted state
		// is always the *final* state of the round, so a
		// `streaming:true` on a persisted thinking block
		// would render the spinner on a thought the user
		// already saw the LLM finish (visible after a
		// rollback/undo too, because the rollback re-emits
		// whatever was in meta["parts"] verbatim).
		//
		// Going forward, `snapshotStructural` clears
		// `Streaming` on every part before serializing
		// (internal/agent/parts.go), so new rows won't
		// carry the field. This migration cleans up the
		// historical data — 2026-07-09 incident where
		// after-undo render was stuck on "思考中".
		//
		// `json_remove` is the right tool: it strips the
		// key by JSON path regardless of the surrounding
		// document shape. The `IS NOT NULL` guard skips
		// rows that don't have the key (e.g. legacy
		// `metadata=""` rows, tool_call rows) and rows
		// where metadata is malformed JSON (json_extract
		// returns NULL for both). No data loss either way.
		Version: 7,
		Name:    "strip_streaming_flag",
		Up: `
UPDATE messages
SET metadata = json_remove(metadata, '$.streaming')
WHERE json_extract(metadata, '$.streaming') IS NOT NULL;`,
		Down: `-- no down — Streaming=false is a no-op after
-- the fix, and flipping it back to true would re-introduce
-- the original bug. A re-run of the up is safe.`,
	},
}
const versionTableSchema = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at INTEGER NOT NULL
);`

// currentVersion 返回已应用的最新迁移版本号。无记录时返回 0。
func currentVersion(db *sql.DB) (int, error) {
	if _, err := db.Exec(versionTableSchema); err != nil {
		return 0, fmt.Errorf("create version table: %w", err)
	}
	var v int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("query current version: %w", err)
	}
	return v, nil
}

// bootstrapOnExisting 处理旧版数据库（有 conversations 表但没有 schema_migrations 记录）。
// 将所有已知迁移标记为已应用，防止重复执行。
// 注意：currentVersion() 已创建了空的 schema_migrations 表——判断条件用行数，不能用表是否存在。
func bootstrapOnExisting(db *sql.DB) error {
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&cnt); err != nil || cnt > 0 {
		return nil // 已有记录，正常流程
	}
	if !hasTable(db, "conversations") {
		return nil // 全新数据库，走正常迁移
	}
	// 旧版数据库 — 标记所有已知迁移为已应用
	for _, m := range allMigrations {
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.Version, m.Name, time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("bootstrap version %d: %w", m.Version, err)
		}
	}
	return nil
}

// hasTable 检查表名是否存在于 sqlite_master。
func hasTable(db *sql.DB, name string) bool {
	var n string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return err == nil && n == name
}

// Migrate 将数据库 schema 升级到最新版本。幂等 — 已应用的迁移自动跳过。
// 在执行任何待执行迁移前会自动备份数据库文件（如果 dbPath 非空）。
func (s *Store) Migrate() error {
	return s.migrateTo(0) // 0 = 所有待执行的迁移
}

// migrateTo 将数据库升级到目标版本。targetVersion=0 表示最新。
func (s *Store) migrateTo(targetVersion int) error {
	db := s.db
	cur, err := currentVersion(db)
	if err != nil {
		return err
	}
	// 旧版数据库无版本表 — 标记已有迁移为已应用
	if cur == 0 {
		if err := bootstrapOnExisting(db); err != nil {
			return err
		}
		cur, err = currentVersion(db)
		if err != nil {
			return err
		}
	}
	max := targetVersion
	if max <= 0 {
		max = len(allMigrations)
	}

	// 统计待执行的迁移数量
	pending := 0
	for _, m := range allMigrations {
		if m.Version <= cur {
			continue
		}
		if m.Version > max {
			break
		}
		pending++
	}
	if pending == 0 {
		return nil
	}

	// 执行备份
	if s.dbPath != "" {
		if err := backupDB(s.db, s.dbPath); err != nil {
			// 备份失败不阻塞迁移，但记录
			fmt.Printf("pchat: db backup failed: %v\n", err)
		}
	}

	for _, m := range allMigrations {
		if m.Version <= cur {
			continue
		}
		if m.Version > max {
			break
		}
		if err := applyMigration(db, m); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// backupDB copies the SQLite database file (and WAL/shm siblings) to
// <dbPath>.backup-<timestamp>. The existing connection is
// checkpointed first so the file-copy is self-contained.
// Errors are non-fatal.
//
// IMPORTANT: the caller must hold s.mu so no other goroutine is
// writing to the DB while we copy. Opening a separate
// connection (as the previous version did) is racy: a
// concurrent writer on the main connection may have a
// transaction in-flight that the second connection can't see,
// and closing the second connection can leave the WAL file
// un-truncated.
func backupDB(db *sql.DB, dbPath string) error {
	// Force WAL checkpoint on the SAME connection that the
	// application is using, so the checkpoint is consistent
	// with the caller's view of the database. The caller
	// holds s.mu so no concurrent writer can interleave.
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("wal_checkpoint: %w", err)
	}

	backupPath := dbPath + ".backup-" + time.Now().Format("20060102-150405")
	src, err := os.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(backupPath)
	if err != nil {
		return fmt.Errorf("create dst: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(backupPath)
		return fmt.Errorf("copy: %w", err)
	}
	fmt.Printf("pchat: db backed up to %s\n", backupPath)
	return nil
}

// Rollback 将数据库 schema 回滚到目标版本。即撤销所有 > targetVersion 的迁移。
func (s *Store) Rollback(targetVersion int) error {
	cur, err := currentVersion(s.db)
	if err != nil {
		return err
	}
	if targetVersion >= cur {
		return nil // 无需回滚
	}
	// 从后往前执行 Down
	for i := len(allMigrations) - 1; i >= 0; i-- {
		m := allMigrations[i]
		if m.Version <= targetVersion {
			break
		}
		if m.Version > cur {
			continue
		}
		if err := rollbackMigration(s.db, m); err != nil {
			return fmt.Errorf("rollback %d (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// AppliedMigrations 返回已应用和可用的迁移列表。
func (s *Store) AppliedMigrations() (current int, available int, err error) {
	cur, err := currentVersion(s.db)
	if err != nil {
		return 0, 0, err
	}
	return cur, len(allMigrations), nil
}

func applyMigration(db *sql.DB, m Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(m.Up); err != nil {
		return fmt.Errorf("exec up: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.Version, m.Name, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("record version: %w", err)
	}
	return tx.Commit()
}

func rollbackMigration(db *sql.DB, m Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(m.Down); err != nil {
		return fmt.Errorf("exec down: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM schema_migrations WHERE version = ?`, m.Version,
	); err != nil {
		return fmt.Errorf("remove version record: %w", err)
	}
	return tx.Commit()
}
