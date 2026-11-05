package memory

import (
	"database/sql"
	"fmt"
	"time"
)

// Bookmark is a user pin on one message inside a conversation.
type Bookmark struct {
	ID             int64
	ConversationID string
	MessageID      int64
	Note           string
	CreatedAt      time.Time
	Seq            int64
}

// EnsureMessageBookmarks creates the bookmark table. Idempotent so both
// fresh installs and the V21 upgrade can call it.
func EnsureMessageBookmarks(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS message_bookmarks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    UNIQUE(conversation_id, message_id)
)`)
	if err != nil {
		return fmt.Errorf("create message_bookmarks: %w", err)
	}
	return nil
}

// ListBookmarks returns pins for a conversation, oldest first.
func (s *Store) ListBookmarks(conversationID string) ([]Bookmark, error) {
	if s == nil || s.db == nil || conversationID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT b.id, b.conversation_id, b.message_id, b.note, b.created_at, COALESCE(m.seq, 0)
		FROM message_bookmarks b
		LEFT JOIN messages m ON m.id = b.message_id AND m.conversation_id = b.conversation_id
		WHERE b.conversation_id = ?
		ORDER BY b.created_at ASC, b.id ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bookmark
	for rows.Next() {
		var b Bookmark
		var created int64
		if err := rows.Scan(&b.ID, &b.ConversationID, &b.MessageID, &b.Note, &created, &b.Seq); err != nil {
			return nil, err
		}
		b.CreatedAt = time.Unix(created, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

// ToggleBookmark pins a message, or removes the pin if it already exists.
// The bool is true when the message is bookmarked after the call.
func (s *Store) ToggleBookmark(conversationID string, messageID int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("memory store not available")
	}
	var owner string
	err := s.db.QueryRow(`SELECT conversation_id FROM messages WHERE id = ?`, messageID).Scan(&owner)
	if err == sql.ErrNoRows || owner != conversationID {
		return false, fmt.Errorf("message not found")
	}
	if err != nil {
		return false, err
	}
	var existing int64
	err = s.db.QueryRow(
		`SELECT id FROM message_bookmarks WHERE conversation_id = ? AND message_id = ?`,
		conversationID, messageID,
	).Scan(&existing)
	if err == nil {
		_, err = s.db.Exec(`DELETE FROM message_bookmarks WHERE id = ?`, existing)
		return false, err
	}
	if err != sql.ErrNoRows {
		return false, err
	}
	_, err = s.db.Exec(
		`INSERT INTO message_bookmarks(conversation_id, message_id, note, created_at) VALUES (?, ?, '', ?)`,
		conversationID, messageID, time.Now().Unix(),
	)
	if err != nil {
		return false, err
	}
	return true, nil
}
