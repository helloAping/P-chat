package memory

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/p-chat/pchat/internal/llm"
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

// BookmarkHit is one pinned message with enough context to list it as
// project, conversation, and a short quote.
type BookmarkHit struct {
	MessageID      int64
	ConversationID string
	Title          string
	ProjectPath    string
	Snippet        string
	Note           string
	Seq            int64
	CreatedAt      time.Time
}

// UserTurn is one user message used to draw the conversation guide,
// independent of which history page the client has loaded.
type UserTurn struct {
	MessageID int64
	Seq       int64
	Snippet   string
}

// ListAllBookmarks returns every pin, newest first.
func (s *Store) ListAllBookmarks() ([]BookmarkHit, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT b.message_id, b.conversation_id, b.created_at, b.note,
		       COALESCE(c.title, ''),
		       COALESCE(json_extract(c.metadata, '$.project_path'), ''),
		       COALESCE(m.content, ''),
		       COALESCE(m.seq, 0)
		FROM message_bookmarks b
		JOIN conversations c ON c.id = b.conversation_id
		LEFT JOIN messages m ON m.id = b.message_id
		ORDER BY b.created_at DESC, b.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BookmarkHit
	for rows.Next() {
		var hit BookmarkHit
		var created int64
		if err := rows.Scan(&hit.MessageID, &hit.ConversationID, &created, &hit.Note, &hit.Title, &hit.ProjectPath, &hit.Snippet, &hit.Seq); err != nil {
			return nil, err
		}
		hit.CreatedAt = time.Unix(created, 0)
		hit.Snippet = clipRunes(strings.TrimSpace(hit.Snippet), 72)
		if hit.Title == "" {
			hit.Title = "未命名对话"
		}
		out = append(out, hit)
	}
	return out, rows.Err()
}

// ListUserTurns returns every visible user message in a conversation,
// oldest first. Pagination on the history API does not apply.
func (s *Store) ListUserTurns(conversationID string) ([]UserTurn, error) {
	if s == nil || s.db == nil || conversationID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT id, COALESCE(seq, 0), COALESCE(content, '')
		FROM messages
		WHERE conversation_id = ?
		  AND role = ?
		  AND COALESCE(is_archived, 0) = 0
		  AND COALESCE(msg_type, 0) = 0
		ORDER BY seq ASC, id ASC`, conversationID, llm.RoleUser)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserTurn
	for rows.Next() {
		var turn UserTurn
		var content string
		if err := rows.Scan(&turn.MessageID, &turn.Seq, &content); err != nil {
			return nil, err
		}
		content = strings.TrimSpace(content)
		if strings.HasPrefix(content, "⏱ 上一回合因") || strings.HasPrefix(content, "⚠ 系统检测：你刚才的回复没有调用任何工具") {
			continue
		}
		if content == "" {
			content = "附件"
		}
		turn.Snippet = clipRunes(content, 80)
		out = append(out, turn)
	}
	return out, rows.Err()
}

func clipRunes(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// DeleteBookmarks removes every pin that belongs to a conversation.
// Permanent session deletion and clearing a conversation's messages
// both call this so a removed turn cannot leave a dangling pin.
func (s *Store) DeleteBookmarks(conversationID string) error {
	if s == nil || s.db == nil || conversationID == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM message_bookmarks WHERE conversation_id = ?`, conversationID)
	if err != nil {
		return fmt.Errorf("delete bookmarks: %w", err)
	}
	return nil
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

// UpdateBookmarkNote replaces the note on an existing pin.
// A missing pin is an error so the caller can return 404.
func (s *Store) UpdateBookmarkNote(conversationID string, messageID int64, note string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("memory store not available")
	}
	res, err := s.db.Exec(
		`UPDATE message_bookmarks SET note = ? WHERE conversation_id = ? AND message_id = ?`,
		note, conversationID, messageID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("bookmark not found")
	}
	return nil
}
