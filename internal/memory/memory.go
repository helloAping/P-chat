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
	"sort"
	"strconv"
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
	ID          string    `json:"id"`
	Title       string    `json:"title,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Metadata    string    `json:"metadata,omitempty"`
	Archived    bool      `json:"archived"`
	VectorStore string    `json:"vector_store,omitempty"`
}

// Message is one entry in a conversation's history.
type Message struct {
	ID             int64     `json:"id"`
	// Seq is the per-conversation logical position
	// (1..N within a session). Unlike `id` (a global
	// AUTOINCREMENT that's never reused), seq survives
	// rollback/undo: the undo path INSERTs restored rows
	// with their caller-supplied seq, so a restored
	// message has the same identity it had before the
	// rollback. The pagination cursor and the rollback
	// anchor both use seq in preference to id. See
	// migration 8 (add_message_seq) for the schema
	// addition and backfill strategy.
	Seq            int64     `json:"seq,omitempty"`
	ConversationID string    `json:"conversation_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	Tokens         int       `json:"tokens,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	Metadata       string    `json:"metadata,omitempty"`
	MsgType        int       `json:"msg_type,omitempty"`
	SubmitToLLM    int       `json:"submit_to_llm,omitempty"`
	// RegenGroupID is the SQLite row id of the user message
	// that triggered this assistant reply, stringified to
	// keep the field optional. Same value across every
	// sibling in a regenerate group (one user prompt can
	// produce many assistant replies). Nil/empty for
	// non-assistant rows and for pre-migration-9 assistant
	// rows that have never been regen'd.
	//
	// See migration 9 (regen_history) for the schema
	// addition and backfill, and store.go's
	// ArchiveSiblings / ListSiblings / ActivateSibling
	// for the group-management API.
	RegenGroupID *string `json:"regen_group_id,omitempty"`
	// IsArchived is the visibility flag the UI uses to
	// pick which sibling to show. 0 = the active reply
	// (default, what ListMessages returns); 1 = an older
	// regenerated sibling that lives in the same group
	// but is hidden from the main timeline. The user
	// can paginate to archived siblings via the
	// bubble's ◀ N/M ▶ pager or the new
	// GET .../replies endpoint.
	IsArchived bool `json:"is_archived"`
}

// Summary records an LLM-generated compression of a range of messages.
type Summary struct {
	ConversationID string    `json:"conversation_id"`
	RangeStart     int64     `json:"range_start"`
	RangeEnd       int64     `json:"range_end"`
	Summary        string    `json:"summary"`
	CreatedAt      time.Time `json:"created_at"`
}

// SearchResult is one hit from a full-text search across messages.
type SearchResult struct {
	ConversationID    string `json:"conversation_id"`
	ConversationTitle string `json:"conversation_title"`
	MessageID         int64  `json:"message_id"`
	Role              string `json:"role"`
	Snippet           string `json:"snippet"`
	CreatedAt         int64  `json:"created_at"`
}

// Store is the central accessor for the SQLite-backed memory database.
type Store struct {
	db         *sql.DB
	dbPath     string // filesystem path, set by OpenAt (empty for in-memory)
	closed     atomic.Bool // set by Close; readers reject when true
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

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// WAL mode supports concurrent readers + single writer.
	// A pool size of 4 allows concurrent reads while avoiding
	// contention on the single-writer lock.
	db.SetMaxOpenConns(4)

	s := &Store{
		db:            db,
		dbPath:        dbPath,
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
	// Idempotent: double-Close should be safe (the underlying
	// sql.DB returns an error on double-Close which we swallow).
	if s.closed.Swap(true) {
		return nil
	}
	s.flushOnce.Do(func() { close(s.stopCh) })
	// Flush after the closed flag is set so any in-flight
	// AddMessage that races with Close writes its pending
	// message into the closed store — at that point the DB is
	// still open, so the write succeeds. Future AddMessage
	// calls would be after Close and would observe the closed
	// flag if we add such a check.
	if err := s.Flush(); err != nil {
		return err
	}
	return s.db.Close()
}

// Ping verifies the underlying SQLite connection is alive.
// Used by the /health endpoint to surface "DB wedged" as a
// 503 to load balancers rather than serving traffic that
// will fail at the next query. Cheap (a single SELECT 1).
func (s *Store) Ping() error {
	if s.closed.Load() {
		return fmt.Errorf("store closed")
	}
	return s.db.Ping()
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
	return s.Migrate()
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
	s.AddMessageTo(s.currentID, msg)
}

// AddMessageTo is like AddMessage but writes to an explicit
// conversation. This is the multi-session-safe variant —
// AddMessage reads the shared s.currentID, which is a race
// hazard when several goroutines stream into different
// conversations at once.
func (s *Store) AddMessageTo(convID string, msg llm.Message) {
	s.pendingMu.Lock()
	s.pendingWrites = append(s.pendingWrites, Message{
		ConversationID: convID,
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
	s.AddMessageWithMetaTo(s.currentID, msg, meta)
}

// AddMessageWithMetaTo is the multi-session-safe variant of
// AddMessageWithMeta (see AddMessageTo for the rationale).
func (s *Store) AddMessageWithMetaTo(convID string, msg llm.Message, meta map[string]string) {
	if len(meta) == 0 {
		s.AddMessageTo(convID, msg)
		return
	}
	b, _ := json.Marshal(meta)
	s.pendingMu.Lock()
	s.pendingWrites = append(s.pendingWrites, Message{
		ConversationID: convID,
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
	s.AddChatMessageTo(s.currentID, msg)
}

// AddChatMessageTo is the multi-session-safe variant of
// AddChatMessage. Use this from goroutines that may overlap
// (e.g. concurrent SendMessage on different sessions).
func (s *Store) AddChatMessageTo(convID string, msg llm.ChatMessage) {
	meta := encodeChatMeta(msg)
	b, _ := json.Marshal(meta)
	s.pendingMu.Lock()
	s.pendingWrites = append(s.pendingWrites, Message{
		ConversationID: convID,
		Role:           msg.Role,
		Content:        msg.Content,
		CreatedAt:      time.Now(),
		Metadata:       string(b),
		MsgType:        msg.MsgType,
		SubmitToLLM:    msg.SubmitToLLM,
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
	s.AddChatMessageWithMetaTo(s.currentID, msg, extraMeta)
}

// AddChatMessageWithMetaTo is the multi-session-safe variant.
// Internally delegates to AddChatMessageWithMetaToRegen with
// empty regenGroupID and isArchived=false — the P1-4 regen-
// history fields are NULL/0 for any non-regen write, so
// legacy callers (agent's SendMessage path, sub-agents,
// test fixtures) get the same single-shot behaviour
// without a signature change.
func (s *Store) AddChatMessageWithMetaTo(convID string, msg llm.ChatMessage, extraMeta map[string]string) {
	s.AddChatMessageWithMetaToRegen(convID, msg, extraMeta, "", false)
}

// AddChatMessageWithMetaToRegen is the P1-4 regen-aware
// variant of AddChatMessageWithMetaTo. regenGroupID is the
// string form of the user message id that this assistant
// row is replying to (empty for non-regen writes; the new
// row's regen_group_id column will be NULL). isArchived
// controls the new row's visibility flag — false for the
// active regen sibling, true for the archived path
// (ArchiveSiblings uses a direct UPDATE, not this method,
// so the false branch is the only one used in practice).
//
// P1-4 callers:
//   - agent.persistAssistant uses
//     AddChatMessageWithMetaToRegen(req.SessionID, msg, meta,
//     req.RegenGroupID, false) when persisting the agent
//     loop's final assistant row. Non-empty RegenGroupID
//     marks the row as a new sibling in that regen group.
func (s *Store) AddChatMessageWithMetaToRegen(convID string, msg llm.ChatMessage, extraMeta map[string]string, regenGroupID string, isArchived bool) {
	m := encodeChatMeta(msg)
	for k, v := range extraMeta {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	var rgPtr *string
	if regenGroupID != "" {
		rg := regenGroupID
		rgPtr = &rg
	}
	s.pendingMu.Lock()
	s.pendingWrites = append(s.pendingWrites, Message{
		ConversationID: convID,
		Role:           msg.Role,
		Content:        msg.Content,
		CreatedAt:      time.Now(),
		Metadata:       string(b),
		MsgType:        msg.MsgType,
		SubmitToLLM:    msg.SubmitToLLM,
		RegenGroupID:   rgPtr,
		IsArchived:     isArchived,
	})
	full := len(s.pendingWrites) >= s.maxPending
	s.pendingMu.Unlock()
	if full {
		_ = s.Flush()
	}
}

// AddChatMessageToWithID is like AddChatMessageTo but persists the
// row with an explicit rowID instead of letting SQLite AUTOINCREMENT
// pick one. Used by the agent when the frontend has pre-minted a
// client_msg_id at send time (see SendMessageRequest.ClientMsgID) —
// the row id on disk and the msg.id on the client end up identical,
// so rollback and regenerate have a valid target from the moment
// the user clicks send, without having to wait for the SSE `done`
// event to broadcast the server-assigned id back.
//
// rowID == 0 falls back to the autoincrement path (the same
// behaviour as AddChatMessageTo) so callers can pass through
// the request's optional field without an extra branch.
func (s *Store) AddChatMessageToWithID(convID string, msg llm.ChatMessage, rowID int64) {
	meta := encodeChatMeta(msg)
	b, _ := json.Marshal(meta)
	s.pendingMu.Lock()
	s.pendingWrites = append(s.pendingWrites, Message{
		// 0 here means "let AUTOINCREMENT pick" in Flush;
		// any positive value is inserted verbatim.
		ID:             rowID,
		ConversationID: convID,
		Role:           msg.Role,
		Content:        msg.Content,
		CreatedAt:      time.Now(),
		Metadata:       string(b),
		MsgType:        msg.MsgType,
		SubmitToLLM:    msg.SubmitToLLM,
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

// GetChatMessagesFor is the multi-session-safe variant — pass
// the conversation id explicitly instead of relying on the
// shared currentID. Empty convID returns no messages.
func (s *Store) GetChatMessagesFor(convID string, limit int) []llm.ChatMessage {
	msgs, _, _ := s.GetChatMessagesWithMetaFor(convID, limit)
	return msgs
}

// CountChatMessages returns the total number of *active*
// (is_archived = 0) messages in convID. Used by the
// history-paging endpoint to decide whether there are
// older messages to load. The P1-4 visibility filter is
// applied so the count matches what the main timeline
// actually shows — archived regen siblings don't count.
func (s *Store) CountChatMessages(convID string) int {
	if convID == "" {
		return 0
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND is_archived = 0`, convID).Scan(&n)
	return n
}

// HasOlderMessages reports whether convID has at least one
// active (is_archived = 0) message with id < oldestID.
// Used by the paged ListMessages handler to set `has_more`
// without loading the whole history. Cheap: a single
// indexed EXISTS query.
func (s *Store) HasOlderMessages(convID string, oldestID int64) bool {
	if convID == "" || oldestID <= 0 {
		return false
	}
	var exists bool
	_ = s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM messages WHERE conversation_id = ? AND id < ? AND is_archived = 0)`,
		convID, oldestID,
	).Scan(&exists)
	return exists
}

// HasOlderMessagesBySeq is the seq-based counterpart of
// HasOlderMessages. The seq cursor is stable across
// rollback+undo (per-conversation, never reused), so the
// seq-based check is the right one for the new cursor.
// Backed by the idx_messages_conv_seq index, so it's the
// same cost as the id-based check.
func (s *Store) HasOlderMessagesBySeq(convID string, oldestSeq int64) bool {
	if convID == "" || oldestSeq <= 0 {
		return false
	}
	var exists bool
	_ = s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM messages WHERE conversation_id = ? AND seq < ? AND is_archived = 0)`,
		convID, oldestSeq,
	).Scan(&exists)
	return exists
}

// GetChatMessagesWithMeta returns ChatMessage history alongside
// raw metadata strings and creation timestamps.
func (s *Store) GetChatMessagesWithMeta() ([]llm.ChatMessage, []string, []int64) {
	return s.GetChatMessagesWithMetaN(s.maxHistory)
}

// GetChatMessagesWithMetaN is like GetChatMessagesWithMeta but
// allows overriding the fetch limit. Use 0 for unlimited.
func (s *Store) GetChatMessagesWithMetaN(limit int) ([]llm.ChatMessage, []string, []int64) {
	return s.GetChatMessagesWithMetaFor(s.currentID, limit)
}

// GetChatMessagesWithMetaFor is the multi-session-safe variant.
// Pass the conversation id explicitly; empty id returns nil.
func (s *Store) GetChatMessagesWithMetaFor(convID string, limit int) ([]llm.ChatMessage, []string, []int64) {
	msgs, metas, createds, _, _, _, _ := s.GetChatMessagesWithMetaPage(convID, 0, limit)
	return msgs, metas, createds
}

// GetChatMessagesWithMetaPage is the paged variant of
// GetChatMessagesWithMetaFor. Pass beforeID > 0 to fetch only
// messages with id < beforeID (for infinite-scroll history
// loading). The result is always returned oldest-first; the
// caller does no further ordering.
//
// `limit` follows the same convention as elsewhere: 0 means
// "no limit". For paging you almost always want a positive
// limit (e.g. 50) so the response is bounded.
//
// The fourth return value is the list of SQLite row ids
// (parallel to msgs / metas / createds / seqs). The fifth is
// the per-conversation seq — the API exposes both for
// backwards compat: id is kept for the legacy `before_id`
// cursor while seq is the new stable cursor (see migration
// 8 add_message_seq for the rationale).
//
// The sixth and seventh returns carry the P1-4 regen
// history fields: regen_group_id (string form of the user
// message id, empty when the row has no group) and
// is_archived (the visibility flag — 1 = hidden from the
// main timeline). The main SELECT applies `is_archived = 0`
// so archived siblings never appear here; the caller still
// gets the flag value (always false on this path) for
// downstream consistency with the seq-paged counterpart.
func (s *Store) GetChatMessagesWithMetaPage(convID string, beforeID int64, limit int) ([]llm.ChatMessage, []string, []int64, []int64, []int64, []string, []bool) {
	_ = s.Flush()
	if convID == "" {
		return nil, nil, nil, nil, nil, nil, nil
	}

	// Two query shapes: with and without the beforeID filter.
	// We could use a single query with "id < ? OR ? = 0" but
	// keeping the predicates narrow helps the SQLite planner
	// and keeps the EXPLAIN QUERY PLAN output readable.
	//
	// `is_archived = 0` is the P1-4 visibility filter: the
	// main timeline only shows the active reply per regen
	// group. Archived siblings live in the messages table
	// for the ◀ N/M ▶ pager but are hidden from this
	// SELECT. The idx_messages_conv_seq index already covers
	// (conversation_id, id) ordering; the is_archived=0
	// check is a per-row filter, not a table scan.
	var (
		rows *sql.Rows
		err  error
	)
	if beforeID > 0 {
		rows, err = s.db.Query(
			`SELECT id, role, content, metadata, created_at, msg_type, submit_to_llm, seq, regen_group_id, is_archived FROM messages
			 WHERE conversation_id = ? AND id < ? AND is_archived = 0
			 ORDER BY id DESC LIMIT ?`,
			convID, beforeID, limitOrHuge(limit),
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, role, content, metadata, created_at, msg_type, submit_to_llm, seq, regen_group_id, is_archived FROM messages
			 WHERE conversation_id = ? AND is_archived = 0
			 ORDER BY id DESC LIMIT ?`,
			convID, limitOrHuge(limit),
		)
	}
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil
	}
	defer rows.Close()

	type row struct {
		id           int64
		msg          llm.ChatMessage
		meta         string
		created      int64
		seq          int64
		regenGroupID string
		isArchived   bool
	}
	var rev []row
	for rows.Next() {
		var (
			id                    int64
			role, content         string
			metaStr               sql.NullString
			created               int64
			msgType, submitToLLM  int
			seq                   sql.NullInt64
			regenGroup            sql.NullString
			isArchived            int
		)
		if err := rows.Scan(&id, &role, &content, &metaStr, &created, &msgType, &submitToLLM, &seq, &regenGroup, &isArchived); err != nil {
			break
		}
		meta := ""
		if metaStr.Valid {
			meta = metaStr.String
		}
		seqVal := int64(0)
		if seq.Valid {
			seqVal = seq.Int64
		}
		rg := ""
		if regenGroup.Valid {
			rg = regenGroup.String
		}
		msgs := decodeChatMessages(role, content, meta, msgType, submitToLLM)
		for _, m := range msgs {
			rev = append(rev, row{id: id, msg: m, meta: meta, created: created, seq: seqVal, regenGroupID: rg, isArchived: isArchived == 1})
		}
	}
	n := len(rev)
	out := make([]llm.ChatMessage, n)
	metas := make([]string, n)
	createds := make([]int64, n)
	ids := make([]int64, n)
	seqs := make([]int64, n)
	regenGroupIDs := make([]string, n)
	isArchiveds := make([]bool, n)
	for i := 0; i < n; i++ {
		out[i] = rev[n-1-i].msg
		metas[i] = rev[n-1-i].meta
		createds[i] = rev[n-1-i].created
		ids[i] = rev[n-1-i].id
		seqs[i] = rev[n-1-i].seq
		regenGroupIDs[i] = rev[n-1-i].regenGroupID
		isArchiveds[i] = rev[n-1-i].isArchived
	}
	return out, metas, createds, ids, seqs, regenGroupIDs, isArchiveds
}

// GetChatMessagesWithMetaPageBySeq is the seq-based
// counterpart of GetChatMessagesWithMetaPage. It returns
// rows with `seq < beforeSeq` in the same conversation.
//
// Why a separate method: the id-based cursor becomes stale
// after a rollback+undo (restored rows have new ids). The
// seq-based cursor is stable (seq is per-conversation and
// never reused), so the API exposes it as the preferred
// cursor going forward. The id-based method stays for
// legacy clients.
//
// Both methods share the same in-memory layout (parallel
// slices, oldest-first order). The SQL is just `seq < ?`
// instead of `id < ?`, indexed by idx_messages_conv_seq
// (migration 8). P1-4 adds the is_archived=0 filter and
// the regen_group_id / is_archived return slices (see
// GetChatMessagesWithMetaPage for the rationale).
func (s *Store) GetChatMessagesWithMetaPageBySeq(convID string, beforeSeq int64, limit int) ([]llm.ChatMessage, []string, []int64, []int64, []int64, []string, []bool) {
	_ = s.Flush()
	if convID == "" {
		return nil, nil, nil, nil, nil, nil, nil
	}
	var (
		rows *sql.Rows
		err  error
	)
	if beforeSeq > 0 {
		rows, err = s.db.Query(
			`SELECT id, role, content, metadata, created_at, msg_type, submit_to_llm, seq, regen_group_id, is_archived FROM messages
			 WHERE conversation_id = ? AND seq < ? AND is_archived = 0
			 ORDER BY seq DESC LIMIT ?`,
			convID, beforeSeq, limitOrHuge(limit),
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, role, content, metadata, created_at, msg_type, submit_to_llm, seq, regen_group_id, is_archived FROM messages
			 WHERE conversation_id = ? AND is_archived = 0
			 ORDER BY seq DESC LIMIT ?`,
			convID, limitOrHuge(limit),
		)
	}
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil
	}
	defer rows.Close()

	type row struct {
		id           int64
		msg          llm.ChatMessage
		meta         string
		created      int64
		seq          int64
		regenGroupID string
		isArchived   bool
	}
	var rev []row
	for rows.Next() {
		var (
			id                    int64
			role, content         string
			metaStr               sql.NullString
			created               int64
			msgType, submitToLLM  int
			seq                   sql.NullInt64
			regenGroup            sql.NullString
			isArchived            int
		)
		if err := rows.Scan(&id, &role, &content, &metaStr, &created, &msgType, &submitToLLM, &seq, &regenGroup, &isArchived); err != nil {
			break
		}
		meta := ""
		if metaStr.Valid {
			meta = metaStr.String
		}
		seqVal := int64(0)
		if seq.Valid {
			seqVal = seq.Int64
		}
		rg := ""
		if regenGroup.Valid {
			rg = regenGroup.String
		}
		msgs := decodeChatMessages(role, content, meta, msgType, submitToLLM)
		for _, m := range msgs {
			rev = append(rev, row{id: id, msg: m, meta: meta, created: created, seq: seqVal, regenGroupID: rg, isArchived: isArchived == 1})
		}
	}
	n := len(rev)
	out := make([]llm.ChatMessage, n)
	metas := make([]string, n)
	createds := make([]int64, n)
	ids := make([]int64, n)
	seqs := make([]int64, n)
	regenGroupIDs := make([]string, n)
	isArchiveds := make([]bool, n)
	for i := 0; i < n; i++ {
		out[i] = rev[n-1-i].msg
		metas[i] = rev[n-1-i].meta
		createds[i] = rev[n-1-i].created
		ids[i] = rev[n-1-i].id
		seqs[i] = rev[n-1-i].seq
		regenGroupIDs[i] = rev[n-1-i].regenGroupID
		isArchiveds[i] = rev[n-1-i].isArchived
	}
	return out, metas, createds, ids, seqs, regenGroupIDs, isArchiveds
}

// GetAssistantMessagesAfterSeq is the P0-1 recovery query.
// Returns assistant messages with seq > afterSeq, in
// oldest-first order, with the full metadata blob
// (containing the persisted parts[]). It's deliberately
// separate from the paging methods because the use case
// is fundamentally different: P0-1 wants the *delta* since
// a known cursor, not "the N oldest before a position".
//
// The role filter is hard-coded to "assistant" because
// the only thing the recovery flow needs is the streaming
// assistant content (parts); user / system / tool rows are
// committed synchronously on send and either landed or
// didn't — they don't need a re-pull.
//
// Returns parallel slices: msgs (decoded chat message
// scalar fields), metas (raw metadata JSON), createds
// (unix seconds), seqs. id is omitted because the
// recovery flow doesn't need it; add it back if a
// future caller does.
func (s *Store) GetAssistantMessagesAfterSeq(convID string, afterSeq int64) ([]llm.ChatMessage, []string, []int64, []int64) {
	_ = s.Flush()
	if convID == "" {
		return nil, nil, nil, nil
	}
	rows, err := s.db.Query(
		`SELECT role, content, metadata, created_at, seq FROM messages
		 WHERE conversation_id = ? AND seq > ? AND role = 'assistant'
		 ORDER BY seq ASC`,
		convID, afterSeq,
	)
	if err != nil {
		return nil, nil, nil, nil
	}
	defer rows.Close()

	var out []llm.ChatMessage
	var metas []string
	var createds []int64
	var seqsOut []int64
	for rows.Next() {
		var (
			role, content string
			metaStr       sql.NullString
			created       int64
			seq           sql.NullInt64
		)
		if err := rows.Scan(&role, &content, &metaStr, &created, &seq); err != nil {
			break
		}
		meta := ""
		if metaStr.Valid {
			meta = metaStr.String
		}
		seqVal := int64(0)
		if seq.Valid {
			seqVal = seq.Int64
		}
		msgs := decodeChatMessages(role, content, meta, 0, 1)
		for _, m := range msgs {
			out = append(out, m)
			metas = append(metas, meta)
			createds = append(createds, created)
			seqsOut = append(seqsOut, seqVal)
		}
	}
	return out, metas, createds, seqsOut
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
// dbMsgType and dbSubmitToLLM come from the dedicated columns (0 when
// not yet backfilled); the function falls back to metadata inference
// when they are zero.
//
// The v2 (current) agent write path stores assistant messages as
// `meta["parts"] = "<json string of []MessagePart>"` with an empty
// `content` column — the canonical snapshot format written by
// partsAcc.snapshotStructural. Earlier code only handled the
// `meta["type"]` (new-format-with-type-key) and the legacy
// `multi_content` / `tool_calls` shapes, so v2 rows were silently
// dropped on read. That made `GetChatMessagesWithMeta*` lose
// every assistant message in the LLM context the moment the agent
// started persisting parts, which is exactly the point at which
// the LLM context starts mattering (multi-round tool flows,
// question cards, sub-agents). The v2 branch added below
// reconstructs a single ChatMessage with Type=TypeText and the
// `content` carried over from the row; the full parts array is
// not needed for LLM context (the LLM only sees text + tool calls
// + tool results), and the wire UI re-derives the parts via
// decodePartsFromMeta on the ListMessages path. This keeps the
// LLM context complete without changing the wire shape.
func decodeChatMessages(role, content string, metaStr string, dbMsgType int, dbSubmitToLLM int) []llm.ChatMessage {
	if metaStr == "" || metaStr == "{}" {
		if content != "" {
			return []llm.ChatMessage{{Role: role, Type: llm.TypeText, Content: content, MsgType: llm.MsgTypeText, SubmitToLLM: 1}}
		}
		// Empty content + empty metadata: there's literally
		// no message here (shouldn't happen in practice, but
		// guard rather than panic). Returning nil makes the
		// row invisible to the LLM — same as before this
		// fix, but now scoped to the truly-empty case
		// instead of swallowing the v2 format.
		return nil
	}

	var meta map[string]string
	if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
		if content != "" {
			return []llm.ChatMessage{{Role: role, Type: llm.TypeText, Content: content, MsgType: llm.MsgTypeText, SubmitToLLM: 1}}
		}
		return nil
	}

	// v2 (current) format: the agent's snapshotStructural
	// writes `meta["parts"] = "<json of []MessagePart>"` with
	// the content column holding the denormalized text body.
	// The content column might be empty for a question-only
	// assistant turn; the parts blob is the source of truth.
	// Reconstruct a single text message so the LLM still sees
	// this turn in its history.
	if raw, ok := meta["parts"]; ok && raw != "" {
		// Prefer the row's content column (denormalized
		// cache) when it has text. Fall back to extracting
		// the text part from the parts blob. Both are fine
		// for the LLM.
		text := content
		if text == "" {
			text = extractTextFromPartsBlob(raw)
		}
		// Even when the text is empty (a turn whose only
		// payload is a question card with no prose), the LLM
		// still needs to see *something* so it remembers
		// the question. Emit a single text-typed message
		// with empty content rather than dropping the row.
		// The downstream tool_result will carry the user's
		// answer.
		//
		// submit_to_llm: trust the column value directly.
		// The column is NOT NULL DEFAULT 1 (migration 5 +
		// backfill), so every row has an explicit value
		// and we can read it verbatim. The pre-fix code
		// had `sl := 1; if dbSubmitToLLM != 0 { sl = dbSubmitToLLM }`
		// which silently overrode a deliberate 0 (e.g.
		// thinking rows, exec_command tool_result rows)
		// with the default 1, leaking display-only content
		// into the LLM context on the next read. The C5
		// commit's rollback round-trip relies on this
		// (an undone thinking row must round-trip with
		// submit_to_llm=0, not 1).
		mt := llm.MsgTypeText
		if dbMsgType != 0 {
			mt = dbMsgType
		}
		return []llm.ChatMessage{{
			Role:        role,
			Type:        llm.TypeText,
			Content:     text,
			MsgType:     mt,
			SubmitToLLM: dbSubmitToLLM,
		}}
	}

	// New format with explicit type key (rare — the v2
	// snapshotStructural path is the dominant one; this
	// branch is for messages written via the explicit
	// `type` key, e.g. the question card's standalone
	// roundtrip if any future writer uses that key).
	if t, ok := meta["type"]; ok && t != "" {
		mt, sl := resolveMsgType(t, meta["tool_name"], dbMsgType, dbSubmitToLLM)
		return []llm.ChatMessage{{
			Role:        role,
			Type:        t,
			Content:     content,
			Name:        meta["name"],
			MimeType:    meta["mime_type"],
			ToolID:      meta["tool_id"],
			ToolName:    meta["tool_name"],
			ToolInput:   meta["tool_input"],
			ToolError:   meta["tool_error"] == "true",
			MsgType:     mt,
			SubmitToLLM: sl,
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
						msgs = append(msgs, llm.ChatMessage{Role: role, Type: llm.TypeText, Content: p.Text, MsgType: llm.MsgTypeText, SubmitToLLM: 1})
					}
				case "image_url":
					if p.ImageURL != nil {
						data := extractBase64FromDataURL(p.ImageURL.URL)
						msgs = append(msgs, llm.ChatMessage{
							Role:        role,
							Type:        llm.TypeImage,
							Content:     data,
							MsgType:     llm.MsgTypeImage,
							SubmitToLLM: 1,
						})
					}
				}
			}
			if len(msgs) == 0 && content != "" {
				msgs = append(msgs, llm.ChatMessage{Role: role, Type: llm.TypeText, Content: content, MsgType: llm.MsgTypeText, SubmitToLLM: 1})
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
				msgs = append(msgs, llm.ChatMessage{Role: role, Type: llm.TypeText, Content: content, MsgType: llm.MsgTypeText, SubmitToLLM: 1})
			}
			for _, tc := range tcs {
				msgs = append(msgs, llm.ChatMessage{
					Role:        role,
					Type:        llm.TypeToolCall,
					ToolID:      tc.ID,
					ToolName:    tc.Function.Name,
					ToolInput:   tc.Function.Arguments,
					MsgType:     llm.MsgTypeTool,
					SubmitToLLM: 1,
				})
			}
			return msgs
		}
	}

	// Legacy format: tool_call_id → tool_result message.
	if tcID, ok := meta["tool_call_id"]; ok && tcID != "" {
		mt, sl := resolveMsgType("tool_result", meta["tool_name"], dbMsgType, dbSubmitToLLM)
		return []llm.ChatMessage{{
			Role:        llm.RoleTool,
			Type:        llm.TypeToolResult,
			Content:     content,
			ToolID:      tcID,
			ToolName:    meta["tool_name"],
			MsgType:     mt,
			SubmitToLLM: sl,
		}}
	}

	// Fallback.
	if content != "" {
		return []llm.ChatMessage{{Role: role, Type: llm.TypeText, Content: content, MsgType: llm.MsgTypeText, SubmitToLLM: 1}}
	}
	return nil
}

// resolveMsgType picks the correct MsgType / SubmitToLLM for a decoded
// message. The dedicated DB columns (msg_type, submit_to_llm) are
// the source of truth: every row has explicit values because
// migration 5 added the columns as NOT NULL DEFAULT 1 and the
// backfill (migration 6) re-stamped every existing row. The
// legacy Type-string + tool_name inference is the fallback for
// pre-migration-5 rows that somehow have empty columns (none
// should exist, but the safety net stays).
//
// Pre-fix the "prefer columns" branch was gated on either
// column being non-zero, which meant a thinking row
// (msg_type=0, submit_to_llm=0) fell through to the legacy
// inference and decoded as a normal text row, leaking the
// thinking text into the LLM context. The C5 commit's
// rollback round-trip relies on this NOT happening.
func resolveMsgType(legacyType, toolName string, dbMsgType, dbSubmitToLLM int) (int, int) {
	// If the row's columns look uninitialized (both zero AND
	// no legacy type key), fall back to the legacy inference
	// — this only happens for hand-crafted test fixtures or
	// pre-migration DBs, not real production data.
	if dbMsgType == 0 && dbSubmitToLLM == 0 && legacyType == "" {
		return llm.MsgTypeForLegacy(legacyType, toolName)
	}
	// Trust the columns. The msg_type column may be 0 for
	// text rows (which is the enum's zero value); map that
	// to MsgTypeText explicitly.
	mt := dbMsgType
	if mt == 0 {
		mt = llm.MsgTypeText
	}
	return mt, dbSubmitToLLM
}

// extractTextFromPartsBlob parses a v2 `meta["parts"]` JSON
// string and concatenates the `text` fields of every part
// with kind="text" in their original order. Returns "" if
// the blob is malformed or contains no text parts. Used as
// a fallback when the row's `content` column is empty (a
// turn whose only payload is a question card or a tool
// call with no prose). The LLM doesn't strictly need the
// text — the tool_result carries the user's answer — but
// it does need a placeholder so the message survives the
// read path and the LLM context stays complete.
func extractTextFromPartsBlob(raw string) string {
	var parts []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return ""
	}
	var out string
	for _, p := range parts {
		if p.Kind == "text" && p.Text != "" {
			if out != "" {
				out += "\n"
			}
			out += p.Text
		}
	}
	return out
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
		// Put the pending messages back so a transient
		// error doesn't lose user data. The next Flush
		// attempt will retry.
		s.pendingMu.Lock()
		s.pendingWrites = append(pending, s.pendingWrites...)
		s.pendingMu.Unlock()
		return err
	}
	// Note the explicit `id` column. `INSERT OR IGNORE` lets
	// us mix two write modes in the same batch:
	//   - m.ID == 0  → pass NULL → SQLite AUTOINCREMENT picks
	//                  the next id (legacy path, used for
	//                  assistant / tool_result / question rows).
	//   - m.ID > 0   → pass m.ID verbatim → the row lands at
	//                  the caller-minted id (used when the
	//                  frontend has pre-minted a client_msg_id
	//                  at send time; see AddChatMessageToWithID).
	// The OR IGNORE clause means a duplicate explicit id is
	// silently dropped instead of aborting the whole batch —
	// vanishingly unlikely in practice (Date.now() × 1000 +
	// random gives 13-16 digits of entropy) but cheap to be
	// defensive about.
	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO messages(id, conversation_id, role, content, created_at, metadata, msg_type, submit_to_llm, seq, regen_group_id, is_archived)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		_ = tx.Rollback()
		s.pendingMu.Lock()
		s.pendingWrites = append(pending, s.pendingWrites...)
		s.pendingMu.Unlock()
		return err
	}
	defer stmt.Close()

	// Assign per-conversation seqs in a single pass.
	// For each conversation in the batch, look up the
	// current MAX(seq) once, then stamp pending rows
	// with seq+1, seq+2, ... in the order they were
	// queued. The lookup uses the same index
	// (idx_messages_conv_seq) that the new cursor
	// pagination uses.
	//
	// The pending slice may contain multiple
	// conversations (different SendMessage goroutines
	// can interleave on s.pendingWrites). Grouping by
	// convID first means each MAX(seq) is queried once
	// per conversation rather than once per row.
	convMax := map[string]int64{}
	for _, m := range pending {
		var maxSeq sql.NullInt64
		if err := tx.QueryRow(
			`SELECT MAX(seq) FROM messages WHERE conversation_id = ?`,
			m.ConversationID,
		).Scan(&maxSeq); err != nil {
			_ = tx.Rollback()
			s.pendingMu.Lock()
			s.pendingWrites = append(pending, s.pendingWrites...)
			s.pendingMu.Unlock()
			return err
		}
		base := int64(0)
		if maxSeq.Valid {
			base = maxSeq.Int64
		}
		convMax[m.ConversationID] = base
	}
	// Second pass: write rows, stamping the per-conv counter.
	convCur := map[string]int64{}
	for _, m := range pending {
		base := convMax[m.ConversationID]
		cur := convCur[m.ConversationID]
		next := base + cur + 1
		convCur[m.ConversationID] = cur + 1
		// idArg: NULL for autoincrement (m.ID == 0), explicit
		// m.ID otherwise. The INSERT OR IGNORE above handles
		// both shapes and the rare duplicate-id case.
		var idArg sql.NullInt64
		if m.ID > 0 {
			idArg = sql.NullInt64{Int64: m.ID, Valid: true}
		}
		// regenGroupArg: NULL for legacy / non-regen writes;
		// the explicit regen_group_id string for regen siblings.
		// isArchived: 0 for the active write, 1 for siblings
		// archived by ArchiveSiblings (rare in this path — the
		// archive step happens in a separate UPDATE, see
		// ArchiveSiblings in store.go).
		var regenGroupArg sql.NullString
		if m.RegenGroupID != nil && *m.RegenGroupID != "" {
			regenGroupArg = sql.NullString{String: *m.RegenGroupID, Valid: true}
		}
		isArchived := 0
		if m.IsArchived {
			isArchived = 1
		}
		if _, err := stmt.Exec(
			idArg, m.ConversationID, m.Role, m.Content, m.CreatedAt.Unix(),
			m.Metadata, m.MsgType, m.SubmitToLLM, next,
			regenGroupArg, isArchived,
		); err != nil {
			_ = tx.Rollback()
			// Put pending back so a per-message insert error
			// (e.g. constraint violation on bad data) doesn't
			// silently drop the rest of the batch.
			s.pendingMu.Lock()
			s.pendingWrites = append(pending, s.pendingWrites...)
			s.pendingMu.Unlock()
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
	// The previous line only updated pending[0]'s conversation.
	// If a batch contains messages from multiple sessions (each
	// AddMessageTo may append to the same pending slice), the
	// other sessions' updated_at would be stale, causing the
	// session picker to show them in the wrong order. Update
	// every distinct session in the batch.
	seen := map[string]struct{}{pending[0].ConversationID: {}}
	for _, m := range pending {
		if _, ok := seen[m.ConversationID]; ok {
			continue
		}
		seen[m.ConversationID] = struct{}{}
		if _, err := tx.Exec(
			`UPDATE conversations SET updated_at = ? WHERE id = ?`,
			time.Now().Unix(), m.ConversationID,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
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

// Attachment is one uploaded file (image / audio / video /
// text) attached to a message. Sourced from the
// `multi_content` field in the messages table's metadata
// for user messages, pre-parsed for the CLI /export path
// so the markdown writer can inline data: URLs without
// re-walking the raw OpenAI ChatMessagePart JSON.
//
// Type mirrors openai.ChatMessagePart.Type (the OpenAI
// wire format, kept verbatim for back-compat with any
// downstream consumer that already knows it):
//   - "image_url" / "audio_url" / "video_url": URL carries
//     the data: URL (or https:// URL when the attachment
//     is remote)
//   - "text":      URL carries the file body (text / csv /
//     json / etc. decoded as a UTF-8 string)
//
// Kind is the human-readable category derived from Type.
// It's the same value space the frontend's
// InlineAttachment.kind uses (so a JS consumer
// rendering the JSON doesn't have to translate the
// OpenAI wire format itself):
//   - "image" / "audio" / "video" / "text" / "file"
//
// The Name and Mime fields preserve the original filename
// and MIME for the export's display path.
type Attachment struct {
	Type string `json:"type"`
	Kind string `json:"kind,omitempty"`
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
}

// MessageFull is the rich message shape used by the CLI
// /export path. It adds two fields on top of llm.Message:
//   - Parts: the raw JSON of the assistant message's
//     structured parts (thinking / tool / sub_agent /
//     question). Stored as json.RawMessage so the caller
//     can re-marshal it (JSON export) or decode it
//     (markdown export) without losing any fields the
//     typed llm.Message doesn't carry.
//   - Attachments: the typed list of user-uploaded files
//     extracted from multi_content. Empty for rows that
//     don't carry uploads (e.g. assistant messages).
//   - Thinking: the assistant's chain-of-thought, when
//     persisted. Lives outside parts[] in the frontend
//     message model but is stored under the same row.
//   - CreatedAt: the row's unix-seconds creation time.
type MessageFull struct {
	Msg         llm.Message
	Parts       json.RawMessage `json:"parts,omitempty"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	Thinking    string          `json:"thinking,omitempty"`
	CreatedAt   int64           `json:"created_at"`
}

// GetMessagesFull is like GetMessages but returns the
// rich shape the CLI /export path needs. It re-hydrates
// the assistant message's `parts` (thinking / tool cards /
// sub-agent runs) and the user message's attachments in
// one pass, so the caller doesn't have to thread the
// raw metadata through to the export pipeline.
//
// Behaviour:
//   - Same row selection as GetMessages: current
//     conversation, ordered by id DESC, capped by
//     maxHistory. The slice is returned oldest-first.
//   - Per-row error tolerance: a row that fails to parse
//     is dropped silently (matching the GetMessages
//     convention). The caller sees a shorter slice, not
//     a Go error.
//   - Parts is nil (not "null") for rows that don't have
//     a `parts` key in their metadata — the difference
//     matters for the markdown writer, which uses the
//     nil check to decide whether to render the parts
//     array or the legacy denormalised content field.
func (s *Store) GetMessagesFull() []MessageFull {
	_ = s.Flush()
	convID := s.currentID
	if convID == "" {
		return nil
	}

	limit := s.maxHistory
	rows, err := s.db.Query(
		`SELECT id, role, content, metadata, created_at FROM messages
		 WHERE conversation_id = ?
		 ORDER BY id DESC LIMIT ?`,
		convID, limitOrHuge(limit),
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	type row struct {
		msg  llm.Message
		meta string
		cre  int64
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
		m := llm.Message{Role: role, Content: content}
		metaStr := ""
		if metadata.Valid {
			metaStr = metadata.String
		}
		if metaStr != "" {
			var meta map[string]string
			if json.Unmarshal([]byte(metaStr), &meta) == nil {
				if v, ok := meta["tool_call_id"]; ok {
					m.ToolCallID = v
				}
				if v, ok := meta["name"]; ok {
					m.Name = v
				}
				if v, ok := meta["tool_calls"]; ok && v != "" {
					var tcs []openai.ToolCall
					if json.Unmarshal([]byte(v), &tcs) == nil {
						m.ToolCalls = tcs
					}
				}
				if v, ok := meta["multi_content"]; ok && v != "" {
					var parts []openai.ChatMessagePart
					if json.Unmarshal([]byte(v), &parts) == nil {
						m.MultiContent = parts
					}
				}
			}
		}
		rev = append(rev, row{msg: m, meta: metaStr, cre: created})
	}

	// Reverse so output is oldest-first, matching
	// GetMessages / GetMessagesWithMeta.
	n := len(rev)
	out := make([]MessageFull, n)
	for i := 0; i < n; i++ {
		r := rev[n-1-i]
		out[i] = MessageFull{
			Msg:       r.msg,
			CreatedAt: r.cre,
		}
		// Re-hydrate the assistant message's structured
		// parts (thinking / tool / sub_agent) and any
		// user-uploaded attachments from the raw
		// metadata JSON. Stored under different keys:
		//   - "parts"          → assistant message's
		//                        MessagePart array
		//   - "thinking"       → assistant's thinking text
		//   - "multi_content"  → user uploads (already
		//                        restored on Msg above;
		//                        re-parsed here into a
		//                        typed []Attachment for
		//                        the export)
		if r.meta == "" {
			continue
		}
		// The metadata JSON is heterogeneous across
		// schemas (tool_call_id is a string, parts is an
		// array, multi_content is a string-encoded
		// array). We unmarshal into a generic shape
		// once and pull out the keys we care about,
		// rather than maintaining a hand-rolled typed
		// struct that would need to track every
		// future field the agent adds.
		var meta map[string]json.RawMessage
		if err := json.Unmarshal([]byte(r.meta), &meta); err != nil {
			continue
		}
		if v, ok := meta["parts"]; ok && len(v) > 0 && string(v) != "null" {
			out[i].Parts = v
		}
		if v, ok := meta["thinking"]; ok {
			// Thinking is stored as a JSON string
			// (quoted); unmarshal to drop the
			// surrounding quotes.
			var s string
			if json.Unmarshal(v, &s) == nil {
				out[i].Thinking = s
			}
		}
		// Always re-derive Attachments from
		// multi_content, even when it's empty — the
		// caller can rely on the field being a
		// (possibly empty) slice rather than nil for
		// the rows we know how to decode.
		out[i].Attachments = AttachmentsFromMultiContent(out[i].Msg.MultiContent)
	}
	return out
}

// AttachmentsFromMultiContent turns the OpenAI
// ChatMessagePart list (the wire format for
// multi_content) into a flat []Attachment the export
// can iterate without knowing the OpenAI SDK shapes.
//
// The translation is deliberately lossy on `type`:
//   - "image_url" / "audio_url" / "video_url" pass
//     through, with the data URL preserved verbatim
//   - "text" is collapsed to a single Attachment whose
//     URL field holds the joined text body. The export
//     writer treats this as a text file (rendered as a
//     code block in markdown).
//
// Each entry also gets a `Kind` (the human-readable
// category: image / audio / video / text / file),
// which the export's JSON envelope surfaces at the
// message level as a deduped `attachment_kinds` array.
// Without this, downstream JSON consumers had to look
// inside every `attachments[].type` to learn what kind
// of attachment a message carries — fine for one
// message, painful for a thousand.
//
// Returns nil (not an empty slice) when the input is
// empty — the export writers use a nil check to skip
// the "attachments" section entirely.
//
// Exported so the CLI's test suite (and any other
// package that wants to render multi_content without
// importing the OpenAI SDK) can reuse the same
// translation.
func AttachmentsFromMultiContent(parts []openai.ChatMessagePart) []Attachment {
	if len(parts) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case openai.ChatMessagePartTypeImageURL:
			url := ""
			if p.ImageURL != nil {
				url = p.ImageURL.URL
			}
			out = append(out, Attachment{
				Type: "image_url",
				Kind: "image",
				URL:  url,
				Mime: "image/png", // best-effort; the wire format doesn't carry the MIME separately for image_url
			})
		case "audio_url":
			url := ""
			if p.ImageURL != nil {
				url = p.ImageURL.URL
			}
			out = append(out, Attachment{
				Type: "audio_url",
				Kind: "audio",
				URL:  url,
				Mime: "audio/mpeg",
			})
		case "video_url":
			url := ""
			if p.ImageURL != nil {
				url = p.ImageURL.URL
			}
			out = append(out, Attachment{
				Type: "video_url",
				Kind: "video",
				URL:  url,
				Mime: "video/mp4",
			})
		case openai.ChatMessagePartTypeText:
			out = append(out, Attachment{
				Type: "text",
				Kind: "text",
				URL:  p.Text,
				Mime: "text/plain",
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// GetMessagesFullByID is the per-session equivalent of
// GetMessagesFull: it reads the same rich row shape
// (parts + attachments + thinking) for a specific
// conversation id, without touching the store's
// currentID. The HTTP /export endpoint uses this so
// concurrent exports of different sessions don't fight
// over the global current, and so a single export can't
// silently switch the user's active session out from
// under them.
//
// Returns nil when the session id is empty or unknown —
// matching GetMessagesFull's nil-on-missing convention so
// callers can branch on len() rather than an error.
//
// The implementation is a near-copy of GetMessagesFull
// with the WHERE-clause parameter pulled from the
// argument instead of s.currentID. The two are kept
// separate on purpose: GetMessagesFull is the hot path
// the REPL hits per keystroke, GetMessagesFullByID is
// the cold path hit once per export. Forcing them
// through one method would either add a branch on
// every REPL call or thread a "skip-current" flag
// through call sites that don't need it.
func (s *Store) GetMessagesFullByID(sessionID string) []MessageFull {
	if sessionID == "" {
		return nil
	}
	_ = s.Flush()

	limit := s.maxHistory
	rows, err := s.db.Query(
		`SELECT id, role, content, metadata, created_at FROM messages
		 WHERE conversation_id = ?
		 ORDER BY id ASC`,
		sessionID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	type row struct {
		msg  llm.Message
		meta string
		cre  int64
	}
	var rowsList []row
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
		m := llm.Message{Role: role, Content: content}
		metaStr := ""
		if metadata.Valid {
			metaStr = metadata.String
		}
		if metaStr != "" {
			var meta map[string]string
			if json.Unmarshal([]byte(metaStr), &meta) == nil {
				if v, ok := meta["tool_call_id"]; ok {
					m.ToolCallID = v
				}
				if v, ok := meta["name"]; ok {
					m.Name = v
				}
				if v, ok := meta["tool_calls"]; ok && v != "" {
					var tcs []openai.ToolCall
					if json.Unmarshal([]byte(v), &tcs) == nil {
						m.ToolCalls = tcs
					}
				}
				if v, ok := meta["multi_content"]; ok && v != "" {
					var parts []openai.ChatMessagePart
					if json.Unmarshal([]byte(v), &parts) == nil {
						m.MultiContent = parts
					}
				}
			}
		}
		rowsList = append(rowsList, row{msg: m, meta: metaStr, cre: created})
	}
	if rows.Err() != nil {
		return nil
	}

	// Apply maxHistory cap from the *end* of the
	// ASC-ordered result, matching the REPL path's
	// semantics: a very long session exports the most
	// recent N messages, not a random middle slice.
	if limit > 0 && len(rowsList) > limit {
		rowsList = rowsList[len(rowsList)-limit:]
	}

	out := make([]MessageFull, len(rowsList))
	for i, r := range rowsList {
		out[i] = MessageFull{
			Msg:       r.msg,
			CreatedAt: r.cre,
		}
		if r.meta == "" {
			continue
		}
		var meta map[string]json.RawMessage
		if err := json.Unmarshal([]byte(r.meta), &meta); err != nil {
			continue
		}
		if v, ok := meta["parts"]; ok && len(v) > 0 && string(v) != "null" {
			out[i].Parts = v
		}
		if v, ok := meta["thinking"]; ok {
			var thinkingText string
			if json.Unmarshal(v, &thinkingText) == nil {
				out[i].Thinking = thinkingText
			}
		}
		out[i].Attachments = AttachmentsFromMultiContent(out[i].Msg.MultiContent)
	}
	return out
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
	return s.ListConversationsLimit(0)
}

// ListConversationsLimit returns up to `limit` active (non-archived)
// conversations, ordered by updated_at DESC. limit <= 0 means
// "no cap" (used by legacy callers / tests). The handler-layer
// pagination passes limit=200 to bound the response size.
func (s *Store) ListConversationsLimit(limit int) []Conversation {
	_ = s.Flush()
	q := `SELECT id, COALESCE(title,''), created_at, updated_at, COALESCE(metadata,''), archived, vector_store FROM conversations WHERE archived = 0 ORDER BY updated_at DESC, id DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.Query(q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		var c Conversation
		var created, updated int64
		var archived int
		if err := rows.Scan(&c.ID, &c.Title, &created, &updated, &c.Metadata, &archived, &c.VectorStore); err != nil {
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
		`SELECT id, COALESCE(title,''), created_at, updated_at, COALESCE(metadata,''), archived, vector_store FROM conversations WHERE id = ?`,
		id,
	).Scan(&c.ID, &c.Title, &created, &updated, &c.Metadata, &archived, &c.VectorStore)
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

// SetConversationVectorStore sets the vector store binding for a session.
func (s *Store) SetConversationVectorStore(id, vectorStore string) error {
	_, err := s.db.Exec(`UPDATE conversations SET vector_store = ? WHERE id = ?`, vectorStore, id)
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
	return s.ListArchivedConversationsLimit(0)
}

// ListArchivedConversationsLimit returns up to `limit` archived
// conversations, ordered by updated_at DESC. limit <= 0 means
// "no cap" (legacy callers / tests).
func (s *Store) ListArchivedConversationsLimit(limit int) []Conversation {
	_ = s.Flush()
	q := `SELECT id, COALESCE(title,''), created_at, updated_at, COALESCE(metadata,''), archived, vector_store FROM conversations WHERE archived = 1 ORDER BY updated_at DESC, id DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.Query(q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		var c Conversation
		var created, updated int64
		var archived int
		if err := rows.Scan(&c.ID, &c.Title, &created, &updated, &c.Metadata, &archived, &c.VectorStore); err != nil {
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

// ForkConversation copies all messages up to and including
// beforeID from sourceConvID into a brand-new conversation.
// The new title is "[Fork] " + source title. Returns the new
// conversation.
func (s *Store) ForkConversation(sourceConvID string, beforeID int64) (*Conversation, error) {
	_ = s.Flush()

	src, err := s.GetConversation(sourceConvID)
	if err != nil {
		return nil, fmt.Errorf("fork: source conversation: %w", err)
	}

	newID := newConvID()
	now := time.Now().Unix()
	title := "[Fork] " + src.Title
	if _, err := s.db.Exec(
		`INSERT INTO conversations(id, title, created_at, updated_at, metadata) VALUES (?, ?, ?, ?, ?)`,
		newID, title, now, now, src.Metadata,
	); err != nil {
		return nil, fmt.Errorf("fork: create conversation: %w", err)
	}

	// Read all messages into memory BEFORE starting a transaction.
	// With SetMaxOpenConns(1) the single connection is held while
	// rows is open; a tx.Begin() would deadlock waiting for it.
	rows, err := s.db.Query(
		`SELECT role, content, metadata, created_at, msg_type, submit_to_llm, is_archived FROM messages
		 WHERE conversation_id = ? AND id <= ? ORDER BY id ASC`,
		sourceConvID, beforeID,
	)
	if err != nil {
		return nil, fmt.Errorf("fork: query messages: %w", err)
	}

	type row struct {
		role, content, meta         string
		created                     int64
		msgType, submitToLLM        int
		// isArchived at fork source. We do NOT copy the
		// regen_group_id from the source: a fork is its own
		// conversation with its own regenerate history
		// (user-confirmed scope: "fork 出去新会话一切从 0
		// 开始"). The only thing we need to preserve per-row
		// is the visibility flag — if the user forked at a
		// point where the active reply was the 2nd regen
		// sibling, the new conversation must show the same
		// content as the active one (and the user can
		// regen from there to get fresh history).
		isArchived int
	}
	var msgs []row
	for rows.Next() {
		var r row
		var metaStr sql.NullString
		if err := rows.Scan(&r.role, &r.content, &metaStr, &r.created, &r.msgType, &r.submitToLLM, &r.isArchived); err != nil {
			rows.Close()
			return nil, err
		}
		if metaStr.Valid {
			r.meta = metaStr.String
		}
		msgs = append(msgs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(msgs) == 0 {
		s.db.Exec(`DELETE FROM conversations WHERE id = ?`, newID)
		return nil, fmt.Errorf("fork: no messages to copy (before_id %d not found in session %s)", beforeID, sourceConvID)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	// Clean up the empty conversation on failure.
	ok := false
	defer func() {
		if !ok {
			tx.Rollback()
			s.db.Exec(`DELETE FROM conversations WHERE id = ?`, newID)
		}
	}()

	stmt, err := tx.Prepare(
		`INSERT INTO messages(conversation_id, role, content, created_at, metadata, msg_type, submit_to_llm, seq, is_archived) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	// The fork's per-conversation seq counter starts at
	// 1 (the new conversation has no prior history).
	// Walking the source msgs in id-ASC order (already
	// guaranteed by the SELECT above) means we can
	// stamp seq=i+1 for the i-th row — no MAX(seq)
	// lookup needed. regen_group_id is always NULL on
	// the fork: the new conversation has no regenerate
	// history of its own (any regen the user does from
	// here is a fresh group).
	for i, r := range msgs {
		if _, err := stmt.Exec(newID, r.role, r.content, r.created, r.meta, r.msgType, r.submitToLLM, int64(i+1), r.isArchived); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ok = true

	conv, err := s.GetConversation(newID)
	if err != nil {
		return nil, err
	}
	return &conv, nil
}

// GetLastUserMessageID returns the SQLite row id of the most
// recently inserted user message for the given session. Returns
// 0 when no user messages exist.
func (s *Store) GetLastUserMessageID(convID string) int64 {
	_ = s.Flush()
	var id int64
	_ = s.db.QueryRow(
		`SELECT id FROM messages WHERE conversation_id = ? AND role = 'user' ORDER BY id DESC LIMIT 1`,
		convID,
	).Scan(&id)
	return id
}

// ValidateUserMessageID is the P1-3 guard used by the
// Regenerate endpoint. Returns nil when msgID exists in
// convID AND has role='user'. Returns a descriptive error
// otherwise so the handler can pass it straight back to
// the client (gin.H{"error": err.Error()}). The check is
// deliberately strict — a regenerate of an assistant
// message id (mistake or malice) would leave the agent
// loop running with no user prompt to drive it.
func (s *Store) ValidateUserMessageID(convID string, msgID int64) error {
	_ = s.Flush()
	if msgID <= 0 {
		return fmt.Errorf("user_message_id must be > 0, got %d", msgID)
	}
	var role string
	err := s.db.QueryRow(
		`SELECT role FROM messages WHERE conversation_id = ? AND id = ?`,
		convID, msgID,
	).Scan(&role)
	if err == sql.ErrNoRows {
		return fmt.Errorf("message id %d not found in session %s", msgID, convID)
	}
	if err != nil {
		return err
	}
	if role != "user" {
		return fmt.Errorf("message id %d has role %q, want \"user\"", msgID, role)
	}
	return nil
}

// GetLastMessageID returns the highest SQLite row id for the given
// session (the most recently inserted message of any role). Returns
// 0 when the session has no messages.
func (s *Store) GetLastMessageID(convID string) int64 {
	_ = s.Flush()
	var id int64
	_ = s.db.QueryRow(
		`SELECT id FROM messages WHERE conversation_id = ? ORDER BY id DESC LIMIT 1`,
		convID,
	).Scan(&id)
	return id
}

// MaxRegenPerGroup caps the number of sibling replies a single
// user prompt can accumulate via regenerate. Once a group has
// `MaxRegenPerGroup` rows, ArchiveSiblings deletes the oldest
// archived sibling in the same transaction so the group never
// grows unbounded. 20 was chosen by the user during planning —
// large enough that no real user hits it, small enough that
// the archive SELECT cost stays O(20) per regen.
const MaxRegenPerGroup = 20

// ArchiveSiblings marks every row in the regen group (active +
// archived) as archived except the one with id == keepActiveID,
// which becomes the new active. Called from the Regenerate
// handler just before re-running the agent loop: the user has
// asked for a fresh answer, so the previously-active reply
// (and any other siblings) move out of the main timeline.
//
// The 20-row cap is enforced in the same transaction: if the
// group would exceed MaxRegenPerGroup after this archive pass,
// the oldest archived siblings are hard-deleted until the
// group fits the cap. The active row is never deleted by the
// cap — even a 1-active + 19-archived group is within the
// cap and stays untouched.
//
// groupID is the string form of the user message's row id
// (the same value the agent loop stamps on the new
// assistant reply's regen_group_id column). Empty groupID is
// rejected — the caller must know which user message is
// being regenerated.
//
// Returns the number of rows hard-deleted by the cap (0 when
// the group was already within the cap). Useful for tests
// and the cap-recovery telemetry.
func (s *Store) ArchiveSiblings(convID, groupID string, keepActiveID int64) (deleted int, err error) {
	_ = s.Flush()
	if convID == "" {
		return 0, fmt.Errorf("ArchiveSiblings: empty convID")
	}
	if groupID == "" {
		return 0, fmt.Errorf("ArchiveSiblings: empty groupID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 1. Mark every group row as archived except keepActiveID.
	//    `is_archived = CASE WHEN id = ? THEN 0 ELSE 1 END`
	//    is a single statement that runs once instead of N
	//    individual UPDATEs. The idx_messages_conv_group
	//    index covers the (convID, groupID) filter.
	if _, err := tx.Exec(
		`UPDATE messages
		 SET is_archived = CASE WHEN id = ? THEN 0 ELSE 1 END
		 WHERE conversation_id = ? AND regen_group_id = ?`,
		keepActiveID, convID, groupID,
	); err != nil {
		return 0, fmt.Errorf("archive siblings: %w", err)
	}

	// 2. Enforce MaxRegenPerGroup in the same transaction.
	//    Count the (active + archived) rows in the group; if
	//    over the cap, hard-delete the oldest archived rows
	//    by id ASC. The active row is never touched — the
	//    cap protects storage, not the visible reply.
	//
	//    The cap is computed as `MaxRegenPerGroup - 1`
	//    because the regen flow inserts a new assistant row
	//    AFTER ArchiveSiblings returns. Without the -1, the
	//    group would be at MaxRegenPerGroup after ArchiveSiblings
	//    and at MaxRegenPerGroup+1 after the insert. Leaving
	//    a one-row buffer keeps the post-insert count exactly
	//    at the cap.
	var n int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM messages
		 WHERE conversation_id = ? AND regen_group_id = ?`,
		convID, groupID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("count group: %w", err)
	}
	capLimit := MaxRegenPerGroup - 1
	if capLimit < 1 {
		capLimit = 1
	}
	if n > capLimit {
		excess := n - capLimit
		// Hard-delete the `excess` oldest archived rows. ORDER
		// BY id ASC picks the oldest by insertion time (id is
		// AUTOINCREMENT, monotonically increasing per session).
		// The `is_archived = 1` guard is defensive: even though
		// we just archived every non-keepActiveID row, a future
		// caller might have a keepActiveID == 0 path that
		// archives everything; we still want to delete
		// archived-only rows.
		res, err := tx.Exec(
			`DELETE FROM messages
			 WHERE id IN (
			     SELECT id FROM messages
			     WHERE conversation_id = ? AND regen_group_id = ?
			       AND is_archived = 1
			     ORDER BY id ASC
			     LIMIT ?
			 )`,
			convID, groupID, excess,
		)
		if err != nil {
			return 0, fmt.Errorf("cap delete: %w", err)
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			deleted = int(affected)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return deleted, nil
}

// ListSiblings returns every row in the regen group (active +
// archived), oldest-first by id. Used by the
// GET /sessions/:id/messages/:user_msg_id/replies endpoint
// to build the bubble's ◀ N/M ▶ pager. The P1-4 visibility
// filter is NOT applied — the caller wants the full sibling
// set, archived or not, so the user can paginate to any
// historical reply.
//
// groupID is the string form of the user message's row id
// (same convention as ArchiveSiblings).
//
// Returns parallel slices mirroring the rest of the store's
// "with meta" API: msgs (decoded ChatMessage), metas (raw
// metadata JSON), createds (Unix timestamps), ids (SQLite
// row ids), seqs (per-conversation logical position).
// is_archived and regen_group_id are returned for the UI
// pager to compute the active index.
func (s *Store) ListSiblings(convID, groupID string) ([]llm.ChatMessage, []string, []int64, []int64, []int64, []bool, []string) {
	_ = s.Flush()
	if convID == "" || groupID == "" {
		return nil, nil, nil, nil, nil, nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, role, content, metadata, created_at, msg_type, submit_to_llm, seq, regen_group_id, is_archived
		 FROM messages
		 WHERE conversation_id = ? AND regen_group_id = ?
		 ORDER BY id ASC`,
		convID, groupID,
	)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil
	}
	defer rows.Close()

	type row struct {
		id           int64
		msg          llm.ChatMessage
		meta         string
		created      int64
		seq          int64
		regenGroupID string
		isArchived   bool
	}
	var rev []row
	for rows.Next() {
		var (
			id                    int64
			role, content         string
			metaStr               sql.NullString
			created               int64
			msgType, submitToLLM  int
			seq                   sql.NullInt64
			regenGroup            sql.NullString
			isArchived            int
		)
		if err := rows.Scan(&id, &role, &content, &metaStr, &created, &msgType, &submitToLLM, &seq, &regenGroup, &isArchived); err != nil {
			break
		}
		meta := ""
		if metaStr.Valid {
			meta = metaStr.String
		}
		seqVal := int64(0)
		if seq.Valid {
			seqVal = seq.Int64
		}
		rg := ""
		if regenGroup.Valid {
			rg = regenGroup.String
		}
		msgs := decodeChatMessages(role, content, meta, msgType, submitToLLM)
		for _, m := range msgs {
			rev = append(rev, row{id: id, msg: m, meta: meta, created: created, seq: seqVal, regenGroupID: rg, isArchived: isArchived == 1})
		}
	}
	n := len(rev)
	out := make([]llm.ChatMessage, n)
	metas := make([]string, n)
	createds := make([]int64, n)
	ids := make([]int64, n)
	seqs := make([]int64, n)
	isArchiveds := make([]bool, n)
	regenGroupIDs := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = rev[i].msg
		metas[i] = rev[i].meta
		createds[i] = rev[i].created
		ids[i] = rev[i].id
		seqs[i] = rev[i].seq
		isArchiveds[i] = rev[i].isArchived
		regenGroupIDs[i] = rev[i].regenGroupID
	}
	return out, metas, createds, ids, seqs, isArchiveds, regenGroupIDs
}

// ActivateSibling makes the row identified by activeID the
// group's only active reply, archiving every other row in
// the same group. Used by the POST .../activate endpoint
// when the user clicks ◀/▶ on the bubble's pager to view
// a different historical reply.
//
// `activeID` MUST belong to the group identified by groupID;
// the function verifies this and returns an error if not.
// The check prevents a malicious client from activating an
// arbitrary row from a different group / conversation.
//
// Returns nil on success. The caller (handler) is
// responsible for re-querying the active row's content
// (or the full sibling set) to send back to the client —
// this function only updates the visibility flag.
func (s *Store) ActivateSibling(convID, groupID string, activeID int64) error {
	_ = s.Flush()
	if convID == "" || groupID == "" {
		return fmt.Errorf("ActivateSibling: empty convID or groupID")
	}
	if activeID <= 0 {
		return fmt.Errorf("ActivateSibling: activeID must be > 0")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify the target row belongs to the right group /
	// conversation. Two queries because the CASE expression
	// in the UPDATE below doesn't error on a missing row
	// (the WHERE convID=AND group=AND id= would match zero
	// rows, no error, but the side effect of "every other
	// row archived" would still fire — and that's wrong if
	// activeID is a row from a different group).
	var (
		rowConv     string
		rowGroup    sql.NullString
	)
	err := s.db.QueryRow(
		`SELECT conversation_id, regen_group_id FROM messages WHERE id = ?`,
		activeID,
	).Scan(&rowConv, &rowGroup)
	if err == sql.ErrNoRows {
		return fmt.Errorf("ActivateSibling: message id %d not found", activeID)
	}
	if err != nil {
		return err
	}
	if rowConv != convID {
		return fmt.Errorf("ActivateSibling: message id %d belongs to a different conversation", activeID)
	}
	if !rowGroup.Valid || rowGroup.String != groupID {
		return fmt.Errorf("ActivateSibling: message id %d is not in group %q", activeID, groupID)
	}

	// One statement does both halves: target → active, every
	// other row in the group → archived. The CASE picks the
	// right value per row. The (convID, groupID) WHERE keeps
	// the scope tight (no accidental effect on rows from
	// other groups even if a future refactor passes the
	// wrong activeID past the guard above).
	_, err = s.db.Exec(
		`UPDATE messages
		 SET is_archived = CASE WHEN id = ? THEN 0 ELSE 1 END
		 WHERE conversation_id = ? AND regen_group_id = ?`,
		activeID, convID, groupID,
	)
	return err
}

// DeleteMessagesFrom deletes all messages with id >= fromID in the
// given conversation and returns the deleted messages so the caller
// can undo the operation with RestoreMessages.
func (s *Store) DeleteMessagesFrom(conversationID string, fromID int64) ([]Message, error) {
	_ = s.Flush()

	// Snapshot the rows before deleting.
	//
	// msg_type and submit_to_llm are part of the snapshot
	// on purpose: the rollback handler converts each row
	// through buildMessageResponse, which uses MsgType to
	// filter out standalone tool_call / tool_result /
	// exec_command rows (their data is already embedded
	// in the parent assistant message's parts). Without
	// these columns, the filter would never fire and the
	// rollback would return tool rows as empty assistant
	// bubbles, then the frontend would splice them back
	// as zero-content messages on undo.
	//
	// seq is part of the snapshot for the same reason:
	// the new seq-based cursor relies on every row having
	// a valid seq. RestoreMessages must reuse the original
	// seq, otherwise the restored row's seq would collide
	// with whatever MAX(seq)+1 looks like at undo time and
	// (a) the cursor would skip the row on the next page
	// request, (b) two rows in the same conversation
	// could end up with the same seq.
	//
	// regen_group_id and is_archived are part of the
	// snapshot so an undo restores the full regen
	// history — if the user rolled back a turn that had
	// 3 regen siblings, undoing must bring back all 3
	// in their original active/archived state, not
	// collapse them to a single active row.
	rows, err := s.db.Query(
		`SELECT id, conversation_id, role, content, tokens, created_at, metadata, msg_type, submit_to_llm, seq, regen_group_id, is_archived
		 FROM messages WHERE conversation_id = ? AND id >= ? ORDER BY id`,
		conversationID, fromID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deleted []Message
	for rows.Next() {
		var m Message
		var created int64
		var seq sql.NullInt64
		var regenGroup sql.NullString
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.Tokens, &created, &m.Metadata, &m.MsgType, &m.SubmitToLLM, &seq, &regenGroup, &m.IsArchived); err != nil {
			return deleted, err
		}
		if seq.Valid {
			m.Seq = seq.Int64
		}
		if regenGroup.Valid {
			rg := regenGroup.String
			m.RegenGroupID = &rg
		}
		m.CreatedAt = time.Unix(created, 0)
		deleted = append(deleted, m)
	}
	if err := rows.Err(); err != nil {
		return deleted, err
	}

	if len(deleted) == 0 {
		return nil, nil
	}

	if _, err := s.db.Exec(
		`DELETE FROM messages WHERE conversation_id = ? AND id >= ?`,
		conversationID, fromID,
	); err != nil {
		return nil, err
	}
	return deleted, nil
}

// RestoreMessages inserts previously-deleted messages back into the
// messages table with their original ids and seqs. This is the
// inverse of DeleteMessagesFrom. Callers should only restore
// messages that were previously returned by DeleteMessagesFrom.
//
// The pre-fix code only restored id/conversation_id/role/content/
// tokens/created_at/metadata — three fields were silently
// dropped on every undo:
//   - msg_type     → restored rows defaulted to 0 (MsgTypeText)
//   - submit_to_llm → restored rows defaulted to 1 (the original
//                     intent was the opposite for some categories,
//                     e.g. thinking is submit_to_llm=0 and
//                     tool_result for exec_command is also 0)
//   - seq          → restored rows got NULL seq, breaking the
//                    new seq-based cursor (migration 8)
//
// Now we restore all of them. The caller (handler
// UndoRollback) populates these from the MessageResponse
// payload, which is the wire shape that already carries the
// server-decoded values. Without this, after-undo rows
// would have wrong content-shape (e.g. tool rows reading
// as text, thinking rows reading as assistant text) AND
// the seq-based cursor would skip them (NULL seq doesn't
// match `seq < ?` unless we make the SQL null-safe).
func (s *Store) RestoreMessages(messages []Message) error {
	if len(messages) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`INSERT OR REPLACE INTO messages(id, conversation_id, role, content, tokens, created_at, metadata, msg_type, submit_to_llm, seq, regen_group_id, is_archived)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range messages {
		var regenGroupArg sql.NullString
		if m.RegenGroupID != nil && *m.RegenGroupID != "" {
			regenGroupArg = sql.NullString{String: *m.RegenGroupID, Valid: true}
		}
		isArchived := 0
		if m.IsArchived {
			isArchived = 1
		}
		if _, err := stmt.Exec(
			m.ID, m.ConversationID, m.Role, m.Content, m.Tokens,
			m.CreatedAt.Unix(), m.Metadata, m.MsgType, m.SubmitToLLM, m.Seq,
			regenGroupArg, isArchived,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	return s.LastCompressedIDFor(s.currentID)
}

// LastCompressedIDFor is the multi-session-safe variant — pass
// the conversation id explicitly.
func (s *Store) LastCompressedIDFor(convID string) int64 {
	if convID == "" {
		return 0
	}
	var maxEnd sql.NullInt64
	_ = s.db.QueryRow(
		`SELECT MAX(range_end) FROM summaries WHERE conversation_id = ?`,
		convID,
	).Scan(&maxEnd)
	if maxEnd.Valid {
		return maxEnd.Int64
	}
	return 0
}

// CompressedSummary returns the concatenated text of all summaries
// for the current conversation, or empty string if none.
func (s *Store) CompressedSummary() string {
	return s.CompressedSummaryFor(s.currentID)
}

// CompressedSummaryFor is the multi-session-safe variant.
func (s *Store) CompressedSummaryFor(convID string) string {
	if convID == "" {
		return ""
	}
	rows, err := s.db.Query(
		`SELECT summary FROM summaries WHERE conversation_id = ? ORDER BY range_start ASC`,
		convID,
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
	return s.GetChatMessagesAfterIDFor(s.currentID, limit, afterID)
}

// GetChatMessagesAfterIDFor is the multi-session-safe variant of
// GetChatMessagesAfterID. Pass the conversation id explicitly.
func (s *Store) GetChatMessagesAfterIDFor(convID string, limit int, afterID int64) ([]llm.ChatMessage, []string, []int64) {
	_ = s.Flush()
	if convID == "" {
		return nil, nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, role, content, metadata, created_at, msg_type, submit_to_llm FROM messages
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
				msgType, submitToLLM  int
			)
			if err := rows.Scan(&id, &role, &content, &metaStr, &created, &msgType, &submitToLLM); err != nil {
				break
			}
			meta := ""
			if metaStr.Valid {
				meta = metaStr.String
			}
			msgs := decodeChatMessages(role, content, meta, msgType, submitToLLM)
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

// TodoItem is a single task in a session's todo list.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// SaveTodos persists a session's todo list to SQLite.
// Replaces the entire list atomically.
func (s *Store) SaveTodos(sessionID string, todos []TodoItem) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM todo_items WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	for i, t := range todos {
		if _, err := tx.Exec(
			`INSERT INTO todo_items(session_id, item_id, content, status, sort_order) VALUES (?, ?, ?, ?, ?)`,
			sessionID, t.ID, t.Content, t.Status, i,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadTodos loads a session's todo list from SQLite.
// Returns nil if no todos exist.
func (s *Store) LoadTodos(sessionID string) []TodoItem {
	rows, err := s.db.Query(
		`SELECT item_id, content, status FROM todo_items WHERE session_id = ? ORDER BY sort_order ASC`,
		sessionID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TodoItem
	for rows.Next() {
		var t TodoItem
		if err := rows.Scan(&t.ID, &t.Content, &t.Status); err != nil {
			continue
		}
		out = append(out, t)
	}
	return out
}

// newConvID generates a sortable, unique conversation id.
// Uses nanosecond precision + a small atomic counter to guarantee
// uniqueness even when multiple NewConversation() calls happen in
// the same nanosecond (e.g. during tests or fast startup).
var convCounter atomic.Int64

func newConvID() string {
	n := convCounter.Add(1)
	return fmt.Sprintf("conv_%d_%d", time.Now().UnixNano(), n)
}

// SearchMessages performs a simple LIKE-based full-text search
// across messages in all active (non-archived) conversations.
// Returns up to `limit` results sorted by created_at desc.
//
// User input is escaped so that LIKE metacharacters (`%`, `_`)
// in the search query don't behave as wildcards. A search for
// "100%" matches the literal substring "100%", not "100" +
// anything.
func (s *Store) SearchMessages(q string, limit int) []SearchResult {
	_ = s.Flush()
	if q == "" {
		return nil
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return nil
	}
	// Escape LIKE metacharacters: backslash, percent, underscore.
	// SQLite uses `\` as the ESCAPE char by default, so we prefix
	// each metachar with `\`.
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)

	rows, err := s.db.Query(
		`SELECT m.conversation_id, COALESCE(c.title, ''), m.id, m.role, m.content, m.created_at
		 FROM messages m
		 JOIN conversations c ON c.id = m.conversation_id AND c.archived = 0
		 WHERE m.content LIKE ? ESCAPE '\'
		 ORDER BY m.created_at DESC
		 LIMIT ?`,
		"%"+escaped+"%", limitOrHuge(limit),
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		var content string
		if err := rows.Scan(&r.ConversationID, &r.ConversationTitle, &r.MessageID, &r.Role, &content, &r.CreatedAt); err != nil {
			break
		}
		r.Snippet = snippet(content, q, 120)
		out = append(out, r)
	}
	return out
}

// snippet extracts a short window around the first occurrence of
// `q` in `content`, capped at maxLen runes.
func snippet(content, q string, maxLen int) string {
	lower := strings.ToLower(content)
	qlower := strings.ToLower(q)
	idx := strings.Index(lower, qlower)
	if idx < 0 {
		runes := []rune(content)
		if len(runes) <= maxLen {
			return content
		}
		return string(runes[:maxLen]) + "…"
	}
	// Center the window around the match.
	runes := []rune(content)
	start := idx - (maxLen-len(q))/2
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(runes) {
		end = len(runes)
		start = end - maxLen
		if start < 0 {
			start = 0
		}
	}
	s := string(runes[start:end])
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s = s + "…"
	}
	return s
}

func limitOrHuge(n int) int {
	if n <= 0 {
		return 1 << 30
	}
	return n
}

// ConversationTokenStats holds aggregated token usage for one conversation.
type ConversationTokenStats struct {
	ConversationID    string  `json:"conversation_id"`
	ConversationTitle string  `json:"conversation_title"`
	TokensIn          int     `json:"tokens_in"`
	TokensOut         int     `json:"tokens_out"`
	MsgCount          int     `json:"msg_count"`
	UpdatedAt         int64   `json:"updated_at"`
}

// TokenStats scans messages metadata for assistant messages with
// tokens_in / tokens_out keys and aggregates per conversation.
//
// Two queries (instead of a single correlated subquery that ran
// once per row → O(N²)): one to gather per-conversation metadata
// (title, updated_at, msg_count) and one to gather per-message
// token data. Both run in O(N) over the messages table.
func (s *Store) TokenStats() []ConversationTokenStats {
	_ = s.Flush()

	type convMeta struct {
		Title     string
		UpdatedAt int64
		MsgCount  int
	}
	// Query 1: per-conversation metadata + msg_count via GROUP BY.
	meta := make(map[string]*convMeta)
	rows, err := s.db.Query(`
		SELECT c.id, COALESCE(c.title, ''), c.updated_at,
		       (SELECT COUNT(*) FROM messages m2 WHERE m2.conversation_id = c.id) AS msg_count
		FROM conversations c
		WHERE c.archived = 0
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, title string
			var updatedAt int64
			var msgCount int
			if err := rows.Scan(&id, &title, &updatedAt, &msgCount); err == nil {
				meta[id] = &convMeta{Title: title, UpdatedAt: updatedAt, MsgCount: msgCount}
			}
		}
	}

	// Query 2: per-message token data (O(N) scan, no correlated subquery).
	type tokenAgg struct {
		TokensIn  int
		TokensOut int
	}
	tokens := make(map[string]*tokenAgg)
	rows2, err := s.db.Query(`
		SELECT m.conversation_id, m.metadata
		FROM messages m
		WHERE m.role = 'assistant' AND m.metadata IS NOT NULL AND m.metadata != ''
	`)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var convID, metaStr string
			if err := rows2.Scan(&convID, &metaStr); err != nil {
				continue
			}
			var m map[string]string
			if err := json.Unmarshal([]byte(metaStr), &m); err != nil {
				continue
			}
			e, ok := tokens[convID]
			if !ok {
				e = &tokenAgg{}
				tokens[convID] = e
			}
			if t, ok := m["tokens_in"]; ok {
				if v, err := strconv.Atoi(t); err == nil {
					e.TokensIn += v
				}
			}
			if t, ok := m["tokens_out"]; ok {
				if v, err := strconv.Atoi(t); err == nil {
					e.TokensOut += v
				}
			}
		}
	}

	var out []ConversationTokenStats
	for convID, m := range meta {
		t := tokens[convID]
		tin, tout := 0, 0
		if t != nil {
			tin, tout = t.TokensIn, t.TokensOut
		}
		out = append(out, ConversationTokenStats{
			ConversationID:    convID,
			ConversationTitle: m.Title,
			TokensIn:          tin,
			TokensOut:         tout,
			MsgCount:          m.MsgCount,
			UpdatedAt:         m.UpdatedAt,
		})
	}
	// Sort by updated_at desc.
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
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
