package memory

import (
	"database/sql"
	"fmt"
	"time"
)

// TurnChange is one persisted file or command touch from an agent turn.
// It is a summary only; byte snapshots stay in the in-memory journal.
type TurnChange struct {
	ID             int64
	ConversationID string
	MessageID      int64
	Tool           string
	Path           string
	Preview        string
	CreatedAt      time.Time
}

// EnsureTurnChanges creates the summary table. Idempotent.
func EnsureTurnChanges(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS turn_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    tool TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    preview TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_turn_changes_conv ON turn_changes(conversation_id, message_id);`)
	if err != nil {
		return fmt.Errorf("create turn_changes: %w", err)
	}
	return nil
}

// ReplaceTurnChanges rewrites the summary rows for one assistant message.
func (s *Store) ReplaceTurnChanges(conversationID string, messageID int64, rows []TurnChange) error {
	if s == nil || s.db == nil || conversationID == "" || messageID <= 0 || len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM turn_changes WHERE conversation_id = ? AND message_id = ?`, conversationID, messageID); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, row := range rows {
		if _, err := tx.Exec(`INSERT INTO turn_changes(conversation_id, message_id, tool, path, preview, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			conversationID, messageID, row.Tool, row.Path, row.Preview, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListTurnChanges returns summaries for a conversation, oldest first.
func (s *Store) ListTurnChanges(conversationID string) ([]TurnChange, error) {
	if s == nil || s.db == nil || conversationID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT id, conversation_id, message_id, tool, path, preview, created_at
		FROM turn_changes WHERE conversation_id = ? ORDER BY message_id ASC, id ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TurnChange
	for rows.Next() {
		var row TurnChange
		var created int64
		if err := rows.Scan(&row.ID, &row.ConversationID, &row.MessageID, &row.Tool, &row.Path, &row.Preview, &created); err != nil {
			return nil, err
		}
		row.CreatedAt = time.Unix(created, 0)
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeleteTurnChanges removes summaries for a conversation.
func (s *Store) DeleteTurnChanges(conversationID string) error {
	if s == nil || s.db == nil || conversationID == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM turn_changes WHERE conversation_id = ?`, conversationID)
	return err
}

// LastVisibleUserText returns the newest user message body, or empty.
func (s *Store) LastVisibleUserText(conversationID string) (string, error) {
	if s == nil || s.db == nil || conversationID == "" {
		return "", nil
	}
	_ = s.Flush()
	var content string
	err := s.db.QueryRow(`SELECT content FROM messages WHERE conversation_id = ? AND role = 'user' ORDER BY id DESC LIMIT 1`, conversationID).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return content, err
}

// LatestAssistantMessageID returns the newest assistant row id, or 0.
func (s *Store) LatestAssistantMessageID(conversationID string) int64 {
	if s == nil || s.db == nil || conversationID == "" {
		return 0
	}
	_ = s.Flush()
	var id int64
	err := s.db.QueryRow(`SELECT id FROM messages WHERE conversation_id = ? AND role = 'assistant' ORDER BY id DESC LIMIT 1`, conversationID).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}
