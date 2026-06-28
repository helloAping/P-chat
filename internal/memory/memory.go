// Package memory stores conversation history, message metadata, summaries
// and knowledge chunks in a single SQLite database at ~/.p-chat/memory/store.db.
//
// The previous JSON file is migrated automatically on first read.
package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	openai "github.com/sashabaranov/go-openai"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/paths"
	_ "modernc.org/sqlite"
)

// Conversation is a logical chat session.
type Conversation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Metadata  string    `json:"metadata,omitempty"`
	Archived  bool      `json:"archived"`
}

// Message is one entry in a conversation's history.
type Message struct {
	ID             int64     `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	Tokens         int       `json:"tokens,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	Metadata       string    `json:"metadata,omitempty"`
}

// Summary records an LLM-generated compression of a range of messages.
type Summary struct {
	ConversationID string    `json:"conversation_id"`
	RangeStart     int64     `json:"range_start"`
	RangeEnd       int64     `json:"range_end"`
	Summary        string    `json:"summary"`
	CreatedAt      time.Time `json:"created_at"`
}

// Store is the central accessor for the SQLite-backed memory database.
type Store struct {
	db         *sql.DB
	mu         sync.Mutex
	currentID  string
	maxHistory int

	// pendingWrites coalesces message additions so we don't fsync
	// after every token. Flushes happen on a timer or when the
	// buffer hits maxPending.
	pendingWrites []Message
	pendingMu     sync.Mutex
	maxPending    int
	flushInterval time.Duration
	stopCh        chan struct{}
	flushOnce     sync.Once
}

// Open opens (or creates) the SQLite database and runs migrations.
func Open(maxHistory int) (*Store, error) {
	return OpenAt(paths.MemoryDB(), maxHistory)
}

// OpenAt opens a SQLite database at the given path. Useful for tests
// that need an isolated store. The parent directory of dbPath is
// created if it doesn't exist.
func OpenAt(dbPath string, maxHistory int) (*Store, error) {
	// Make sure the parent directory exists. Skip this for paths that
	// don't have a parent (e.g. just a filename in cwd).
	parent := filepath.Dir(dbPath)
	if parent != "" && parent != "." {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return nil, fmt.Errorf("create db parent: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // sqlite is single-writer; serialize to avoid lock errors

	s := &Store{
		db:            db,
		maxHistory:    maxHistory,
		maxPending:    20,
		flushInterval: 2 * time.Second,
		stopCh:        make(chan struct{}),
	}

	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	// Migrate legacy JSON file if present.
	if err := s.migrateLegacy(); err != nil {
		// non-fatal: log only
		fmt.Printf("warn: legacy migration failed: %v\n", err)
	}

	// Pick the most recent conversation as current if none set.
	if cur, err := s.mostRecentConversation(); err == nil && cur != "" {
		s.currentID = cur
	} else {
		id, err := s.NewConversation()
		if err != nil {
			return nil, err
		}
		_ = id
	}

	go s.flushLoop()
	return s, nil
}

// Close flushes pending writes and closes the database.
func (s *Store) Close() error {
	s.flushOnce.Do(func() { close(s.stopCh) })
	if err := s.Flush(); err != nil {
		return err
	}
	return s.db.Close()
}

func ensureDir() error {
	// Use MkdirAll on the DB path's parent.
	dir := paths.MemoryDir()
	if err := paths.EnsureGlobal(); err != nil {
		return err
	}
	_ = dir
	return nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS conversations (
			id          TEXT PRIMARY KEY,
			title       TEXT,
			created_at  INTEGER NOT NULL,
			updated_at  INTEGER NOT NULL,
			metadata    TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
			role            TEXT NOT NULL,
			content         TEXT NOT NULL,
			tokens          INTEGER NOT NULL DEFAULT 0,
			created_at      INTEGER NOT NULL,
			metadata        TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id, id)`,
		`CREATE TABLE IF NOT EXISTS summaries (
			conversation_id TEXT NOT NULL,
			range_start      INTEGER NOT NULL,
			range_end        INTEGER NOT NULL,
			summary          TEXT NOT NULL,
			created_at       INTEGER NOT NULL,
			PRIMARY KEY (conversation_id, range_start)
		)`,
		`CREATE TABLE IF NOT EXISTS chunks (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			source      TEXT NOT NULL,
			content     TEXT NOT NULL,
			metadata    TEXT,
			created_at  INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(source)`,
		`CREATE TABLE IF NOT EXISTS embeddings (
			chunk_id   INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
			model      TEXT NOT NULL,
			vector     BLOB NOT NULL,
			dim        INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("exec %q: %w", q, err)
		}
	}
	// Migration: add archived column to conversations (v2 schema).
	s.db.Exec(`ALTER TABLE conversations ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`)
	return nil
}

// migrateLegacy imports the old JSON file and renames it on success.
func (s *Store) migrateLegacy() error {
	jsonPath := paths.MemoryFile()
	data, err := readFile(jsonPath)
	if err != nil {
		return nil // no legacy file
	}
	var legacy map[string]struct {
		ID       string        `json:"id"`
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("parse legacy: %w", err)
	}
	if len(legacy) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for id, conv := range legacy {
		now := time.Now().Unix()
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO conversations(id, title, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			id, "", now, now,
		); err != nil {
			return err
		}
		for _, m := range conv.Messages {
			if _, err := tx.Exec(
				`INSERT INTO messages(conversation_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
				id, m.Role, m.Content, now,
			); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Rename the legacy file so we don't import twice.
	_ = renameFile(jsonPath, jsonPath+".migrated")
	return nil
}

func (s *Store) mostRecentConversation() (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM conversations ORDER BY updated_at DESC, id DESC LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// NewConversation creates a new conversation, sets it as current, and
// returns its id.
func (s *Store) NewConversation() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newConvID()
	now := time.Now().Unix()
	if _, err := s.db.Exec(
		`INSERT INTO conversations(id, created_at, updated_at) VALUES (?, ?, ?)`,
		id, now, now,
	); err != nil {
		return "", err
	}
	s.currentID = id
	return id, nil
}

// AddMessage records a message in the current conversation. Writes are
// buffered and flushed asynchronously; call Flush() to force.
func (s *Store) AddMessage(msg llm.Message) {
	s.pendingMu.Lock()
	s.pendingWrites = append(s.pendingWrites, Message{
		ConversationID: s.currentID,
		Role:           msg.Role,
		Content:        msg.Content,
		CreatedAt:      time.Now(),
	})
	full := len(s.pendingWrites) >= s.maxPending
	s.pendingMu.Unlock()
	if full {
		_ = s.Flush()
	}
}

// AddMessageWithMeta is like AddMessage but stores extra metadata
// (tool_call_id, tool name, etc.) as JSON.
func (s *Store) AddMessageWithMeta(msg llm.Message, meta map[string]string) {
	if len(meta) == 0 {
		s.AddMessage(msg)
		return
	}
	b, _ := json.Marshal(meta)
	s.pendingMu.Lock()
	s.pendingWrites = append(s.pendingWrites, Message{
		ConversationID: s.currentID,
		Role:           msg.Role,
		Content:        msg.Content,
		CreatedAt:      time.Now(),
		Metadata:       string(b),
	})
	full := len(s.pendingWrites) >= s.maxPending
	s.pendingMu.Unlock()
	if full {
		_ = s.Flush()
	}
}

// AddChatMessage records a ChatMessage (protocol-agnostic format).
// It automatically encodes metadata based on the message type
// so that GetChatMessages() can restore the full message shape.
func (s *Store) AddChatMessage(msg llm.ChatMessage) {
	meta := encodeChatMeta(msg)
	b, _ := json.Marshal(meta)
	s.pendingMu.Lock()
	s.pendingWrites = append(s.pendingWrites, Message{
		ConversationID: s.currentID,
		Role:           msg.Role,
		Content:        msg.Content,
		CreatedAt:      time.Now(),
		Metadata:       string(b),
	})
	full := len(s.pendingWrites) >= s.maxPending
	s.pendingMu.Unlock()
	if full {
		_ = s.Flush()
	}
}

// AddChatMessageWithMeta records a ChatMessage with additional
// metadata. extraMeta is merged into the auto-generated metadata
// from encodeChatMeta (auto-generated keys take precedence so
// the canonical fields are preserved unless extraMeta explicitly
// overwrites them).
func (s *Store) AddChatMessageWithMeta(msg llm.ChatMessage, extraMeta map[string]string) {
	m := encodeChatMeta(msg)
	for k, v := range extraMeta {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	s.pendingMu.Lock()
	s.pendingWrites = append(s.pendingWrites, Message{
		ConversationID: s.currentID,
		Role:           msg.Role,
		Content:        msg.Content,
		CreatedAt:      time.Now(),
		Metadata:       string(b),
	})
	full := len(s.pendingWrites) >= s.maxPending
	s.pendingMu.Unlock()
	if full {
		_ = s.Flush()
	}
}

// GetChatMessages returns the current conversation's history as
// protocol-agnostic ChatMessage values. It handles both the new
// metadata format and the legacy format.
func (s *Store) GetChatMessages() []llm.ChatMessage {
	return s.GetChatMessagesN(s.maxHistory)
}

// GetChatMessagesN is like GetChatMessages with an explicit limit.
func (s *Store) GetChatMessagesN(limit int) []llm.ChatMessage {
	msgs, _, _ := s.GetChatMessagesWithMetaN(limit)
	return msgs
}

// GetChatMessagesWithMeta returns ChatMessage history alongside
// raw metadata strings and creation timestamps.
func (s *Store) GetChatMessagesWithMeta() ([]llm.ChatMessage, []string, []int64) {
	return s.GetChatMessagesWithMetaN(s.maxHistory)
}

// GetChatMessagesWithMetaN is like GetChatMessagesWithMeta but
// allows overriding the fetch limit. Use 0 for unlimited.
func (s *Store) GetChatMessagesWithMetaN(limit int) ([]llm.ChatMessage, []string, []int64) {
	_ = s.Flush()
	convID := s.currentID
	if convID == "" {
		return nil, nil, nil
	}

	rows, err := s.db.Query(
		`SELECT id, role, content, metadata, created_at FROM messages
		 WHERE conversation_id = ?
		 ORDER BY id DESC LIMIT ?`,
		convID, limitOrHuge(limit),
	)
	if err != nil {
		return nil, nil, nil
	}
	defer rows.Close()

	type row struct {
		msg     llm.ChatMessage
		meta    string
		created int64
	}
	var rev []row
	for rows.Next() {
		var (
			id                    int64
			role, content         string
			metaStr               sql.NullString
			created               int64
		)
		if err := rows.Scan(&id, &role, &content, &metaStr, &created); err != nil {
			break
		}
		meta := ""
		if metaStr.Valid {
			meta = metaStr.String
		}
		msgs := decodeChatMessages(role, content, meta)
		for _, m := range msgs {
			rev = append(rev, row{msg: m, meta: meta, created: created})
		}
	}
	n := len(rev)
	msgs := make([]llm.ChatMessage, n)
	metas := make([]string, n)
	createds := make([]int64, n)
	for i := 0; i < n; i++ {
		msgs[i] = rev[n-1-i].msg
		metas[i] = rev[n-1-i].meta
		createds[i] = rev[n-1-i].created
	}
	return msgs, metas, createds
}

// encodeChatMeta builds the canonical metadata map for a
// ChatMessage.
func encodeChatMeta(msg llm.ChatMessage) map[string]string {
	m := make(map[string]string)
	if msg.Type != "" {
		m["type"] = msg.Type
	}
	if msg.Name != "" {
		m["name"] = msg.Name
	}
	if msg.MimeType != "" {
		m["mime_type"] = msg.MimeType
	}
	if msg.ToolID != "" {
		m["tool_id"] = msg.ToolID
	}
	if msg.ToolName != "" {
		m["tool_name"] = msg.ToolName
	}
	if msg.ToolInput != "" {
		m["tool_input"] = msg.ToolInput
	}
	if msg.ToolError {
		m["tool_error"] = "true"
	}
	return m
}

// decodeChatMessages decodes one row from the messages table into
// one or more ChatMessage values. Handles both new format (type key)
// and legacy format (multi_content / tool_calls / tool_call_id).
func decodeChatMessages(role, content string, metaStr string) []llm.ChatMessage {
	if metaStr == "" || metaStr == "{}" {
		if content != "" {
			return []llm.ChatMessage{{Role: role, Type: llm.TypeText, Content: content}}
		}
		return nil
	}

	var meta map[string]string
	if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
		if content != "" {
			return []llm.ChatMessage{{Role: role, Type: llm.TypeText, Content: content}}
		}
		return nil
	}

	// New format: metadata has a "type" key.
	if t, ok := meta["type"]; ok && t != "" {
		return []llm.ChatMessage{{
			Role:      role,
			Type:      t,
			Content:   content,
			Name:      meta["name"],
			MimeType:  meta["mime_type"],
			ToolID:    meta["tool_id"],
			ToolName:  meta["tool_name"],
			ToolInput: meta["tool_input"],
			ToolError: meta["tool_error"] == "true",
		}}
	}

	// Legacy format: multi_content → split into separate messages.
	if mcJSON, ok := meta["multi_content"]; ok && mcJSON != "" {
		var parts []openai.ChatMessagePart
		if err := json.Unmarshal([]byte(mcJSON), &parts); err == nil {
			var msgs []llm.ChatMessage
			for _, p := range parts {
				switch p.Type {
				case "text":
					if p.Text != "" {
						msgs = append(msgs, llm.ChatMessage{Role: role, Type: llm.TypeText, Content: p.Text})
					}
				case "image_url":
					if p.ImageURL != nil {
						data := extractBase64FromDataURL(p.ImageURL.URL)
						msgs = append(msgs, llm.ChatMessage{
							Role:    role,
							Type:    llm.TypeImage,
							Content: data,
						})
					}
				}
			}
			if len(msgs) == 0 && content != "" {
				msgs = append(msgs, llm.ChatMessage{Role: role, Type: llm.TypeText, Content: content})
			}
			return msgs
		}
	}

	// Legacy format: tool_calls → assistant message + tool_call parts.
	if tcJSON, ok := meta["tool_calls"]; ok && tcJSON != "" {
		var tcs []openai.ToolCall
		if err := json.Unmarshal([]byte(tcJSON), &tcs); err == nil {
			var msgs []llm.ChatMessage
			if content != "" {
				msgs = append(msgs, llm.ChatMessage{Role: role, Type: llm.TypeText, Content: content})
			}
			for _, tc := range tcs {
				msgs = append(msgs, llm.ChatMessage{
					Role:      role,
					Type:      llm.TypeToolCall,
					ToolID:    tc.ID,
					ToolName:  tc.Function.Name,
					ToolInput: tc.Function.Arguments,
				})
			}
			return msgs
		}
	}

	// Legacy format: tool_call_id → tool_result message.
	if tcID, ok := meta["tool_call_id"]; ok && tcID != "" {
		return []llm.ChatMessage{{
			Role:     llm.RoleTool,
			Type:     llm.TypeToolResult,
			Content:  content,
			ToolID:   tcID,
			ToolName: meta["tool_name"],
		}}
	}

	// Fallback.
	if content != "" {
		return []llm.ChatMessage{{Role: role, Type: llm.TypeText, Content: content}}
	}
	return nil
}
func (s *Store) Flush() error {
	s.pendingMu.Lock()
	pending := s.pendingWrites
	s.pendingWrites = nil
	s.pendingMu.Unlock()
	if len(pending) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(
		`INSERT INTO messages(conversation_id, role, content, created_at, metadata) VALUES (?, ?, ?, ?, ?)`,
	)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, m := range pending {
		if _, err := stmt.Exec(m.ConversationID, m.Role, m.Content, m.CreatedAt.Unix(), m.Metadata); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.Exec(
		`UPDATE conversations SET updated_at = ? WHERE id = ?`,
		time.Now().Unix(), pending[0].ConversationID,
	); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) flushLoop() {
	t := time.NewTicker(s.flushInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			_ = s.Flush()
		}
	}
}

// GetMessages returns up to maxHistory messages from the current
// conversation, oldest first. If maxHistory <= 0 all messages are
// returned.
func (s *Store) GetMessages() []llm.Message {
	_ = s.Flush()
	convID := s.currentID
	if convID == "" {
		return nil
	}

	limit := s.maxHistory
	rows, err := s.db.Query(
		`SELECT id, role, content, metadata FROM messages
		 WHERE conversation_id = ?
		 ORDER BY id DESC LIMIT ?`,
		convID, limitOrHuge(limit),
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []llm.Message
	for rows.Next() {
		var (
			id              int64
			role, content   string
			metadata        sql.NullString
		)
		if err := rows.Scan(&id, &role, &content, &metadata); err != nil {
			return out
		}
		msg := llm.Message{Role: role, Content: content}
		// Re-attach metadata (e.g. tool_call_id, name, tool_calls,
		// multi_content) for tool messages, assistant messages with
		// native tool calls, and user messages with attachments.
		if metadata.Valid && metadata.String != "" {
			var meta map[string]string
			if json.Unmarshal([]byte(metadata.String), &meta) == nil {
				if v, ok := meta["tool_call_id"]; ok {
					msg.ToolCallID = v
				}
				if v, ok := meta["name"]; ok {
					msg.Name = v
				}
				if v, ok := meta["tool_calls"]; ok && v != "" {
					// Restore the native tool_calls array so the
					// next turn's history replay matches the tool
					// result messages' tool_call_id references.
					var tcs []openai.ToolCall
					if json.Unmarshal([]byte(v), &tcs) == nil {
						msg.ToolCalls = tcs
					}
				}
				if v, ok := meta["multi_content"]; ok && v != "" {
					// Restore the multi-part content array (text +
					// image_url) so uploaded images survive across
					// turns. Without this the LLM would lose the
					// image on the next turn and try to re-read it
					// via read_file.
					var parts []openai.ChatMessagePart
					if json.Unmarshal([]byte(v), &parts) == nil {
						msg.MultiContent = parts
					}
				}
			}
		}
		out = append([]llm.Message{msg}, out...) // prepend for ASC order
	}
	return out
}

// CurrentConversationID returns the active conversation id.
func (s *Store) CurrentConversationID() string {
	return s.currentID
}

// MessageWithMeta is a row from the messages table paired with
// its raw metadata blob. The store's GetMessages helper only
// re-hydrates a small set of well-known fields (tool_call_id,
// tool_calls, multi_content) into the standard llm.Message.
// Anything else (notably the assistant message's `parts`
// rendering) is still in the raw JSON and must be read by
// callers that need it. GET /sessions/:id/messages uses this
// to forward the parts blob verbatim to the web client so
// thinking blocks / tool cards / sub-agent cards survive a
// session reload.
type MessageWithMeta struct {
	Msg      llm.Message
	Metadata string
	// CreatedAt is the row's creation timestamp (unix seconds).
	// Not part of llm.Message because the LLM client doesn't
	// need it, but the UI uses it to order messages within a
	// session.
	CreatedAt int64
}

// GetMessagesWithMeta is like GetMessages but returns the raw
// metadata string alongside each message. The two slices are
// parallel: out[i] and metaRaw[i] describe the same row. Used
// by the server's history endpoint so it can pass through
// fields the store doesn't know about (e.g. the assistant
// message's `parts` rendering, which is encoded as JSON in
// metadata under the "parts" key by the agent).
func (s *Store) GetMessagesWithMeta() ([]llm.Message, []string, []int64) {
	_ = s.Flush()
	convID := s.currentID
	if convID == "" {
		return nil, nil, nil
	}

	limit := s.maxHistory
	rows, err := s.db.Query(
		`SELECT id, role, content, metadata, created_at FROM messages
		 WHERE conversation_id = ?
		 ORDER BY id DESC LIMIT ?`,
		convID, limitOrHuge(limit),
	)
	if err != nil {
		return nil, nil, nil
	}
	defer rows.Close()

	type row struct {
		msg      llm.Message
		meta     string
		created  int64
	}
	var rev []row
	for rows.Next() {
		var (
			id        int64
			role, content string
			metadata  sql.NullString
			created   int64
		)
		if err := rows.Scan(&id, &role, &content, &metadata, &created); err != nil {
			break
		}
		msg := llm.Message{Role: role, Content: content}
		metaStr := ""
		if metadata.Valid {
			metaStr = metadata.String
		}
		// Re-attach metadata (same logic as GetMessages).
		if metaStr != "" {
			var meta map[string]string
			if json.Unmarshal([]byte(metaStr), &meta) == nil {
				if v, ok := meta["tool_call_id"]; ok {
					msg.ToolCallID = v
				}
				if v, ok := meta["name"]; ok {
					msg.Name = v
				}
				if v, ok := meta["tool_calls"]; ok && v != "" {
					var tcs []openai.ToolCall
					if json.Unmarshal([]byte(v), &tcs) == nil {
						msg.ToolCalls = tcs
					}
				}
				if v, ok := meta["multi_content"]; ok && v != "" {
					var parts []openai.ChatMessagePart
					if json.Unmarshal([]byte(v), &parts) == nil {
						msg.MultiContent = parts
					}
				}
			}
		}
		rev = append(rev, row{msg: msg, meta: metaStr, created: created})
	}

	// Reverse so output is oldest-first, matching GetMessages.
	n := len(rev)
	msgs := make([]llm.Message, n)
	metas := make([]string, n)
	createds := make([]int64, n)
	for i := 0; i < n; i++ {
		msgs[i] = rev[n-1-i].msg
		metas[i] = rev[n-1-i].meta
		createds[i] = rev[n-1-i].created
	}
	return msgs, metas, createds
}

// SetCurrent switches the active conversation.
func (s *Store) SetCurrent(id string) error {
	_ = s.Flush()
	var exists bool
	if err := s.db.QueryRow(`SELECT 1 FROM conversations WHERE id = ?`, id).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("conversation %q not found", id)
		}
		return err
	}
	s.mu.Lock()
	s.currentID = id
	s.mu.Unlock()
	return nil
}

// ListConversations returns all conversations ordered by updated_at desc.
func (s *Store) ListConversations() []Conversation {
	_ = s.Flush()
	rows, err := s.db.Query(`SELECT id, COALESCE(title,''), created_at, updated_at, COALESCE(metadata,''), archived FROM conversations WHERE archived = 0 ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		var c Conversation
		var created, updated int64
		var archived int
		if err := rows.Scan(&c.ID, &c.Title, &created, &updated, &c.Metadata, &archived); err != nil {
			return out
		}
		c.CreatedAt = time.Unix(created, 0)
		c.UpdatedAt = time.Unix(updated, 0)
		c.Archived = archived != 0
		out = append(out, c)
	}
	return out
}

// RenameConversation sets a human-readable title on a conversation.
func (s *Store) RenameConversation(id, title string) error {
	res, err := s.db.Exec(`UPDATE conversations SET title = ? WHERE id = ?`, title, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("conversation %q not found", id)
	}
	return nil
}

// UpdateConversationMeta overwrites the JSON metadata blob for a
// conversation. Pass an empty string to clear it. The caller is
// responsible for serialising whatever payload it wants to keep —
// typically {provider, model, style}. Used by the HTTP layer to
// persist per-session model/style overrides so they survive a
// pchat-server restart.
func (s *Store) UpdateConversationMeta(id, meta string) error {
	_ = s.Flush()
	var v any
	if meta == "" {
		v = nil
	} else {
		v = meta
	}
	res, err := s.db.Exec(`UPDATE conversations SET metadata = ? WHERE id = ?`, v, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("conversation %q not found", id)
	}
	return nil
}

// GetConversation fetches a single conversation by id. Returns
// sql.ErrNoRows if the id is unknown — callers usually translate
// that into a 404.
func (s *Store) GetConversation(id string) (Conversation, error) {
	_ = s.Flush()
	var c Conversation
	var created, updated int64
	var archived int
	err := s.db.QueryRow(
		`SELECT id, COALESCE(title,''), created_at, updated_at, COALESCE(metadata,''), archived FROM conversations WHERE id = ?`,
		id,
	).Scan(&c.ID, &c.Title, &created, &updated, &c.Metadata, &archived)
	if err != nil {
		return Conversation{}, err
	}
	c.CreatedAt = time.Unix(created, 0)
	c.UpdatedAt = time.Unix(updated, 0)
	c.Archived = archived != 0
	return c, nil
}

// ArchiveConversation marks a conversation as archived.
func (s *Store) ArchiveConversation(id string) error {
	_, err := s.db.Exec(`UPDATE conversations SET archived = 1, updated_at = ? WHERE id = ?`, time.Now().Unix(), id)
	return err
}

// UnarchiveConversation restores an archived conversation.
func (s *Store) UnarchiveConversation(id string) error {
	_, err := s.db.Exec(`UPDATE conversations SET archived = 0, updated_at = ? WHERE id = ?`, time.Now().Unix(), id)
	return err
}

// ArchiveByProjectPath archives all conversations whose metadata
// contains the given project_path.
func (s *Store) ArchiveByProjectPath(projectPath string) (int, error) {
	now := time.Now().Unix()
	res, err := s.db.Exec(
		`UPDATE conversations SET archived = 1, updated_at = ? WHERE metadata LIKE ?`,
		now, `%"project_path":"`+projectPath+`"%`,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ListArchivedConversations returns all archived conversations.
func (s *Store) ListArchivedConversations() []Conversation {
	_ = s.Flush()
	rows, err := s.db.Query(`SELECT id, COALESCE(title,''), created_at, updated_at, COALESCE(metadata,''), archived FROM conversations WHERE archived = 1 ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		var c Conversation
		var created, updated int64
		var archived int
		if err := rows.Scan(&c.ID, &c.Title, &created, &updated, &c.Metadata, &archived); err != nil {
			return out
		}
		c.CreatedAt = time.Unix(created, 0)
		c.UpdatedAt = time.Unix(updated, 0)
		c.Archived = archived != 0
		out = append(out, c)
	}
	return out
}

// DeleteConversation removes a conversation and all its messages.
func (s *Store) DeleteConversation(id string) error {
	_, err := s.db.Exec(`DELETE FROM conversations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if s.CurrentConversationID() == id {
		// Pick another conversation as current. If none exists,
		// create a fresh one.
		s.mu.Lock()
		s.currentID = ""
		s.mu.Unlock()
		if cur, _ := s.mostRecentConversation(); cur != "" {
			_ = s.SetCurrent(cur)
		} else {
			if _, err := s.NewConversation(); err != nil {
				return fmt.Errorf("create replacement conv: %w", err)
			}
		}
	}
	return nil
}

// ClearMessages deletes all messages and summaries for a conversation
// without removing the conversation record itself (preserves session ID).
func (s *Store) ClearMessages(conversationID string) error {
	_ = s.Flush()
	if _, err := s.db.Exec(`DELETE FROM messages WHERE conversation_id = ?`, conversationID); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM summaries WHERE conversation_id = ?`, conversationID); err != nil {
		return err
	}
	return nil
}

// SaveSummary records a compressed summary for a range of messages in a
// conversation. Used by the auto-summarize feature.
func (s *Store) SaveSummary(conversationID string, startID, endID int64, summary string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO summaries(conversation_id, range_start, range_end, summary, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		conversationID, startID, endID, summary, time.Now().Unix(),
	)
	return err
}

// GetSummaries returns all summaries for a conversation, oldest first.
func (s *Store) GetSummaries(conversationID string) []Summary {
	rows, err := s.db.Query(
		`SELECT range_start, range_end, summary, created_at
		 FROM summaries WHERE conversation_id = ? ORDER BY range_start ASC`,
		conversationID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s Summary
		var created int64
		if err := rows.Scan(&s.RangeStart, &s.RangeEnd, &s.Summary, &created); err != nil {
			return out
		}
		s.ConversationID = conversationID
		s.CreatedAt = time.Unix(created, 0)
		out = append(out, s)
	}
	return out
}

// LastCompressedID returns the highest range_end across all summaries
// for the current conversation, or 0 if nothing has been compressed.
func (s *Store) LastCompressedID() int64 {
	var maxEnd sql.NullInt64
	_ = s.db.QueryRow(
		`SELECT MAX(range_end) FROM summaries WHERE conversation_id = ?`,
		s.currentID,
	).Scan(&maxEnd)
	if maxEnd.Valid {
		return maxEnd.Int64
	}
	return 0
}

// CompressedSummary returns the concatenated text of all summaries
// for the current conversation, or empty string if none.
func (s *Store) CompressedSummary() string {
	rows, err := s.db.Query(
		`SELECT summary FROM summaries WHERE conversation_id = ? ORDER BY range_start ASC`,
		s.currentID,
	)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var txt string
		if err := rows.Scan(&txt); err != nil {
			continue
		}
		parts = append(parts, txt)
	}
	return strings.Join(parts, "\n\n")
}

// GetChatMessagesAfterID returns up to limit ChatMessage rows with
// id > afterID from the current conversation, oldest first. Use 0
// for afterID to get all messages (no filter).
func (s *Store) GetChatMessagesAfterID(limit int, afterID int64) ([]llm.ChatMessage, []string, []int64) {
	_ = s.Flush()
	convID := s.currentID
	if convID == "" {
		return nil, nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, role, content, metadata, created_at FROM messages
		 WHERE conversation_id = ? AND id > ?
		 ORDER BY id DESC LIMIT ?`,
		convID, afterID, limitOrHuge(limit),
	)
	if err != nil {
		return nil, nil, nil
	}
	defer rows.Close()

	type row struct {
		msg     llm.ChatMessage
		meta    string
		created int64
	}
	var rev []row
	for rows.Next() {
		var (
			id                    int64
			role, content         string
			metaStr               sql.NullString
			created               int64
		)
		if err := rows.Scan(&id, &role, &content, &metaStr, &created); err != nil {
			break
		}
		meta := ""
		if metaStr.Valid {
			meta = metaStr.String
		}
		msgs := decodeChatMessages(role, content, meta)
		for _, m := range msgs {
			rev = append(rev, row{msg: m, meta: meta, created: created})
		}
	}
	// Reverse to ASC order.
	n := len(rev)
	out := make([]llm.ChatMessage, n)
	metas := make([]string, n)
	createds := make([]int64, n)
	for i, r := range rev {
		out[n-1-i] = r.msg
		metas[n-1-i] = r.meta
		createds[n-1-i] = r.created
	}
	return out, metas, createds
}

// ConversationMessageCount returns the number of messages in the current
// conversation.
func (s *Store) ConversationMessageCount() int {
	_ = s.Flush()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id = ?`, s.currentID).Scan(&n); err != nil {
		return 0
	}
	return n
}

// DB returns the underlying *sql.DB for advanced callers (knowledge
// package). Not part of the stable API.
func (s *Store) DB() *sql.DB { return s.db }

// newConvID generates a sortable, unique conversation id.
// Uses nanosecond precision + a small atomic counter to guarantee
// uniqueness even when multiple NewConversation() calls happen in
// the same nanosecond (e.g. during tests or fast startup).
var convCounter atomic.Int64

func newConvID() string {
	n := convCounter.Add(1)
	return fmt.Sprintf("conv_%d_%d", time.Now().UnixNano(), n)
}

func limitOrHuge(n int) int {
	if n <= 0 {
		return 1 << 30
	}
	return n
}

// extractBase64FromDataURL strips the "data:<mime>;base64," prefix
// from a data: URL and returns the raw base64 payload.
func extractBase64FromDataURL(s string) string {
	const prefix = "data:"
	if !strings.HasPrefix(s, prefix) {
		return s
	}
	rest := s[len(prefix):]
	idx := strings.Index(rest, ";base64,")
	if idx < 0 {
		return rest
	}
	return rest[idx+len(";base64,"):]
}

// ----- file helpers (used only during legacy migration) -----

func readFile(path string) ([]byte, error) {
	// Inline to avoid extra import path; callers check error.
	return readFileOS(path)
}
