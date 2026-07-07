package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ── Three-level index node types ──

// IndexNode is a node in the hierarchical knowledge index tree.
// Level 1 = root (one per base), Level 2 = file, Level 3 = section.
type IndexNode struct {
	ID        int    `json:"id"`
	ParentID  int    `json:"parent_id"`
	Base      string `json:"base"`
	Level     int    `json:"level"`      // 1=root, 2=file, 3=section
	Source    string `json:"source"`     // file path (L2/L3)
	Kind      string `json:"kind"`       // text|image|pdf|audio|video
	SortOrder int    `json:"sort_order"`
	Title     string `json:"title"`
	Keywords  string `json:"keywords"`   // comma-separated
	Overview  string `json:"overview"`   // 1-3 sentence summary
}

// ContentNode is a leaf content block attached to an L3 IndexNode.
type ContentNode struct {
	ID          int    `json:"id"`
	NodeID      int    `json:"node_id"`
	Content     string `json:"content"`
	ContentType string `json:"content_type"` // text|image_description|audio_transcript
	SortOrder   int    `json:"sort_order"`
}

// IndexSearchResult is the returned payload for pageable wiki_lookup results.
type IndexSearchResult struct {
	Total   int              `json:"total"`
	Page    int              `json:"page"`
	Size    int              `json:"size"`
	HasMore bool             `json:"has_more"`
	Items   []IndexSearchItem `json:"items"`
}

// IndexSearchItem wraps an IndexNode with optional children and parent.
type IndexSearchItem struct {
	ID       int            `json:"id"`
	Level    int            `json:"level"`
	Title    string         `json:"title"`
	Keywords string         `json:"keywords"`
	Overview string         `json:"overview"`
	Source   string         `json:"source"`
	Kind     string         `json:"kind"`
	Rank     float64        `json:"rank,omitempty"`
	Parent   *NodeRef       `json:"parent,omitempty"`
	Children []ContentNode  `json:"children,omitempty"`
}

// NodeRef is a lightweight reference to a parent node.
type NodeRef struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

// WikiStore manages the FTS5-backed wiki sections database.
type WikiStore struct {
	db   *sql.DB
	dir  string
	name string
}

// NewWikiStore opens/creates a SQLite wiki store at the given path.
func NewWikiStore(name, dir string) (*WikiStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "wiki.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("open wiki db: %w", err)
	}
	ws := &WikiStore{db: db, dir: dir, name: name}
	if err := ws.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := ws.checkIntegrity(); err != nil {
		log.Printf("[knowledge] wiki store %q integrity warning: %v — attempting repair", name, err)
		if repErr := ws.repairIndexFTS(context.Background()); repErr != nil {
			log.Printf("[knowledge] wiki store %q repair failed: %v — the index will be rebuilt on next scan", name, repErr)
		}
	}
	return ws, nil
}

func (ws *WikiStore) migrate() error {
	_, err := ws.db.Exec(`
		CREATE TABLE IF NOT EXISTS media_cache (
			sha256  TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		);
		CREATE TABLE IF NOT EXISTS file_mtimes (
			base   TEXT NOT NULL,
			source TEXT NOT NULL,
			mtime  INTEGER NOT NULL,
			PRIMARY KEY (base, source)
		);
		CREATE TABLE IF NOT EXISTS index_nodes (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_id  INTEGER NOT NULL DEFAULT 0,
			base       TEXT    NOT NULL,
			level      INTEGER NOT NULL DEFAULT 3,
			source     TEXT    NOT NULL DEFAULT '',
			kind       TEXT    NOT NULL DEFAULT 'text',
			sort_order INTEGER NOT NULL DEFAULT 0,
			title      TEXT    NOT NULL,
			keywords   TEXT    NOT NULL DEFAULT '',
			overview   TEXT    NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS contents (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			node_id      INTEGER NOT NULL,
			content      TEXT    NOT NULL,
			content_type TEXT    NOT NULL DEFAULT 'text',
			sort_order   INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (node_id) REFERENCES index_nodes(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_nodes_parent ON index_nodes(parent_id, sort_order);
		CREATE INDEX IF NOT EXISTS idx_nodes_level_base ON index_nodes(level, base);
		CREATE INDEX IF NOT EXISTS idx_contents_node ON contents(node_id, sort_order);
		CREATE VIRTUAL TABLE IF NOT EXISTS index_fts USING fts5(
			title, keywords, overview,
			content='index_nodes', content_rowid='id',
			tokenize='unicode61'
		);
	`)
	if err != nil {
		return err
	}
	if err := ws.syncFTS5Triggers(); err != nil {
		return err
	}
	// Attempt a non-fatal FTS rebuild on startup for databases that may have
	// been corrupted by previous versions (modernc.org/sqlite double-delete).
	if _, err := ws.db.Exec(`INSERT INTO index_fts(index_fts) VALUES('rebuild')`); err != nil {
		log.Printf("[knowledge] migrate: index_fts rebuild warning (may be ok for fresh db): %v", err)
	}
	return nil
}

// syncFTS5Triggers creates or replaces the triggers that keep
// index_fts in sync with index_nodes (for level >= 2 nodes only).
func (ws *WikiStore) syncFTS5Triggers() error {
	triggers := []string{
		`DROP TRIGGER IF EXISTS fts_ins`,
		`DROP TRIGGER IF EXISTS fts_del`,
		`DROP TRIGGER IF EXISTS fts_upd`,
		`CREATE TRIGGER fts_ins AFTER INSERT ON index_nodes
		 WHEN NEW.level >= 2
		 BEGIN
		   INSERT INTO index_fts(rowid, title, keywords, overview)
		   VALUES (NEW.id, NEW.title, NEW.keywords, NEW.overview);
		 END`,
		`CREATE TRIGGER fts_del AFTER DELETE ON index_nodes
		 WHEN OLD.level >= 2
		 BEGIN
		   INSERT INTO index_fts(index_fts, rowid, title, keywords, overview)
		   VALUES ('delete', OLD.id, OLD.title, OLD.keywords, OLD.overview);
		 END`,
		`CREATE TRIGGER fts_upd AFTER UPDATE ON index_nodes
		 WHEN NEW.level >= 2
		 BEGIN
		   INSERT INTO index_fts(index_fts, rowid, title, keywords, overview)
		   VALUES ('delete', OLD.id, OLD.title, OLD.keywords, OLD.overview);
		   INSERT INTO index_fts(rowid, title, keywords, overview)
		   VALUES (NEW.id, NEW.title, NEW.keywords, NEW.overview);
		 END`,
	}
	for _, sql := range triggers {
		if _, err := ws.db.Exec(sql); err != nil {
			return fmt.Errorf("trigger sql [%s]: %w", sql, err)
		}
	}
	return nil
}

// GetFileMtime returns the stored mtime for a source file, or 0 if not found.
func (ws *WikiStore) GetFileMtime(ctx context.Context, base, source string) (int64, error) {
	var mtime int64
	err := ws.db.QueryRowContext(ctx,
		`SELECT mtime FROM file_mtimes WHERE base = ? AND source = ?`, base, source).Scan(&mtime)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return mtime, err
}

// SetFileMtime upserts the mtime for a source file.
func (ws *WikiStore) SetFileMtime(ctx context.Context, base, source string, mtime int64) error {
	_, err := ws.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO file_mtimes (base, source, mtime) VALUES (?, ?, ?)`,
		base, source, mtime)
	return err
}

// CacheMediaDescription stores an AI-generated description keyed by file SHA256.
func (ws *WikiStore) CacheMediaDescription(ctx context.Context, sha256, content string) error {
	_, err := ws.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO media_cache (sha256, content, created_at) VALUES (?, ?, strftime('%s','now'))`,
		sha256, content)
	return err
}

// GetCachedMediaDescription returns a cached description or empty string.
func (ws *WikiStore) GetCachedMediaDescription(ctx context.Context, sha256 string) (string, error) {
	var content string
	err := ws.db.QueryRowContext(ctx,
		`SELECT content FROM media_cache WHERE sha256 = ?`, sha256).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return content, err
}

// Close closes the database.
func (ws *WikiStore) Close() error {
	return ws.db.Close()
}

// Name returns the store name.
func (ws *WikiStore) Name() string { return ws.name }

// ── Three-level index node operations ──

// ReplaceBaseNodes clears all nodes + contents for a base and inserts the
// new set in a single transaction. The index_fts is kept in sync via triggers.
func (ws *WikiStore) ReplaceBaseNodes(ctx context.Context, base string, nodes []IndexNode, contents []ContentNode) error {
	err := ws.replaceBaseNodesInternal(ctx, base, nodes, contents)
	if err != nil && isFTS5Corrupt(err) {
		if repErr := ws.repairIndexFTS(ctx); repErr != nil {
			log.Printf("[knowledge] repair index_fts failed: %v", repErr)
			return fmt.Errorf("index is corrupted and auto-repair failed — delete %s and re-scan: %w", filepath.Join(ws.dir, "wiki.db"), repErr)
		}
		log.Printf("[knowledge] repaired index_fts, retrying ReplaceBaseNodes")
		err = ws.replaceBaseNodesInternal(ctx, base, nodes, contents)
	}
	return err
}

// replaceBaseNodesInternal is the inner body of ReplaceBaseNodes. It
// manually clears index_fts before deleting index_nodes (so the fts_del
// trigger is a no-op during bulk deletes), then clears contents followed
// by index_nodes (FK cascade), then inserts the new set.
func (ws *WikiStore) replaceBaseNodesInternal(ctx context.Context, base string, nodes []IndexNode, contents []ContentNode) error {
	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM index_fts WHERE rowid IN (SELECT id FROM index_nodes WHERE base = ?)`, base); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM contents WHERE node_id IN (SELECT id FROM index_nodes WHERE base = ?)`, base); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM index_nodes WHERE base = ?`, base); err != nil {
		return err
	}

	if err := insertNodesTx(ctx, tx, nodes); err != nil {
		return err
	}
	if err := insertContentsTx(ctx, tx, contents); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceSourceNodes deletes all nodes for a single source within a base,
// then inserts the new set. Used by incremental scanning.
func (ws *WikiStore) ReplaceSourceNodes(ctx context.Context, base, source string, nodes []IndexNode, contents []ContentNode) error {
	err := ws.replaceSourceNodesInternal(ctx, base, source, nodes, contents)
	if err != nil && isFTS5Corrupt(err) {
		if repErr := ws.repairIndexFTS(ctx); repErr != nil {
			log.Printf("[knowledge] repair index_fts failed: %v", repErr)
			return fmt.Errorf("index is corrupted and auto-repair failed — delete %s and re-scan: %w", filepath.Join(ws.dir, "wiki.db"), repErr)
		}
		log.Printf("[knowledge] repaired index_fts, retrying ReplaceSourceNodes for %s", source)
		err = ws.replaceSourceNodesInternal(ctx, base, source, nodes, contents)
	}
	return err
}

func (ws *WikiStore) replaceSourceNodesInternal(ctx context.Context, base, source string, nodes []IndexNode, contents []ContentNode) error {
	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM index_fts WHERE rowid IN (SELECT id FROM index_nodes WHERE base = ? AND source = ?)`, base, source); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM contents WHERE node_id IN (SELECT id FROM index_nodes WHERE base = ? AND source = ?)`, base, source); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM index_nodes WHERE base = ? AND source = ?`, base, source); err != nil {
		return err
	}

	if err := insertNodesTx(ctx, tx, nodes); err != nil {
		return err
	}
	if err := insertContentsTx(ctx, tx, contents); err != nil {
		return err
	}
	return tx.Commit()
}

func insertNodesTx(ctx context.Context, tx *sql.Tx, nodes []IndexNode) error {
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO index_nodes (id, parent_id, base, level, source, kind, sort_order, title, keywords, overview)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, n := range nodes {
		if _, err := stmt.ExecContext(ctx, n.ID, n.ParentID, n.Base, n.Level, n.Source, n.Kind, n.SortOrder, n.Title, n.Keywords, n.Overview); err != nil {
			return err
		}
	}
	return nil
}

func insertContentsTx(ctx context.Context, tx *sql.Tx, contents []ContentNode) error {
	if len(contents) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO contents (node_id, content, content_type, sort_order) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range contents {
		if _, err := stmt.ExecContext(ctx, c.NodeID, c.Content, c.ContentType, c.SortOrder); err != nil {
			return err
		}
	}
	return nil
}

// DeleteNode deletes a node and all its descendants.
// The fts_del trigger handles index_fts cleanup; ON DELETE CASCADE handles contents.
// For L2 (file-level) nodes, also clears the file_mtimes entry so re-scan will
// re-index the source file.
// If a prior corruption left index_fts in a bad state, repairs it and retries once.
func (ws *WikiStore) DeleteNode(ctx context.Context, nodeID int) error {
	err := ws.deleteNodeInternal(ctx, nodeID)
	if err != nil && isFTS5Corrupt(err) {
		if repErr := ws.repairIndexFTS(ctx); repErr != nil {
			log.Printf("[knowledge] repair index_fts failed: %v", repErr)
			return fmt.Errorf("%w (repair also failed: %v)", err, repErr)
		}
		log.Printf("[knowledge] repaired index_fts, retrying delete node %d", nodeID)
		err = ws.deleteNodeInternal(ctx, nodeID)
	}
	return err
}

func (ws *WikiStore) deleteNodeInternal(ctx context.Context, nodeID int) error {
	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var nodeSource string
	var nodeLevel int
	var nodeBase string
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(source,''), level, base FROM index_nodes WHERE id = ?`, nodeID,
	).Scan(&nodeSource, &nodeLevel, &nodeBase)
	if err != nil {
		return err
	}

	allIDs, err := collectDescendantIDs(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	allIDs = append(allIDs, nodeID)

	placeholders := make([]string, len(allIDs))
	args := make([]interface{}, len(allIDs))
	for i, id := range allIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `DELETE FROM index_nodes WHERE id IN (` + joinStrings(placeholders, ",") + `)`
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}

	if nodeLevel == 2 && nodeSource != "" && nodeBase != "" {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM file_mtimes WHERE base = ? AND source = ?`,
			nodeBase, nodeSource); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// checkIntegrity runs PRAGMA integrity_check on the database and
// returns an error if corruption is detected. The caller should
// attempt a repair (repairIndexFTS) and retry.
func (ws *WikiStore) checkIntegrity() error {
	rows, err := ws.db.Query(`PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity_check failed: %w", err)
	}
	defer rows.Close()
	var msgs []string
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return fmt.Errorf("scan integrity result: %w", err)
		}
		if msg != "ok" {
			msgs = append(msgs, msg)
		}
	}
	if len(msgs) > 0 {
		return fmt.Errorf("integrity_check: %s", strings.Join(msgs, "; "))
	}
	return nil
}

// isFTS5Corrupt returns true when err indicates an FTS5 virtual table
// corruption (SQLITE_CORRUPT_VTAB 267).
func isFTS5Corrupt(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "malformed") || strings.Contains(s, "disk image") || strings.Contains(s, "corrupt")
}

// repairIndexFTS drops and recreates the index_fts virtual table,
// then rebuilds it from the content table (index_nodes).
func (ws *WikiStore) repairIndexFTS(ctx context.Context) error {
	// Strategy 1: try in-place rebuild first (fast, non-destructive).
	if _, err := ws.db.ExecContext(ctx,
		`INSERT INTO index_fts(index_fts) VALUES('rebuild')`); err == nil {
		log.Printf("[knowledge] repairIndexFTS: in-place rebuild succeeded")
		return nil
	}

	// Strategy 2: drop triggers, drop+recreate the virtual table, rebuild.
	log.Printf("[knowledge] repairIndexFTS: in-place rebuild failed, trying drop+recreate")
	if err := ws.rebuildFTSFromScratch(ctx); err == nil {
		log.Printf("[knowledge] repairIndexFTS: drop+recreate succeeded")
		return nil
	}

	// Strategy 3: the database file itself may be damaged at the
	// page level. Run VACUUM to rebuild the entire DB file into
	// a clean copy, then retry drop+recreate.
	log.Printf("[knowledge] repairIndexFTS: drop+recreate failed, trying VACUUM + recreate")
	if _, err := ws.db.ExecContext(ctx, `VACUUM`); err != nil {
		log.Printf("[knowledge] repairIndexFTS: VACUUM failed: %v", err)
		return fmt.Errorf("index_fts is corrupted and all repair strategies failed — delete wiki.db and re-scan to rebuild: %w", err)
	}
	if err := ws.rebuildFTSFromScratch(ctx); err != nil {
		log.Printf("[knowledge] repairIndexFTS: recreate after VACUUM failed: %v", err)
		return fmt.Errorf("index_fts is corrupted and all repair strategies failed (after VACUUM) — delete wiki.db and re-scan to rebuild: %w", err)
	}
	log.Printf("[knowledge] repairIndexFTS: VACUUM + recreate succeeded")
	return nil
}

// rebuildFTSFromScratch drops the existing index_fts table and
// triggers, then recreates everything and triggers a rebuild from
// the content table. Used by repairIndexFTS strategies 2 and 3.
func (ws *WikiStore) rebuildFTSFromScratch(ctx context.Context) error {
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS fts_ins`,
		`DROP TRIGGER IF EXISTS fts_del`,
		`DROP TRIGGER IF EXISTS fts_upd`,
	} {
		if _, err := ws.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("drop trigger: %w", err)
		}
	}
	if _, err := ws.db.ExecContext(ctx, `DROP TABLE IF EXISTS index_fts`); err != nil {
		return fmt.Errorf("drop index_fts: %w", err)
	}
	if _, err := ws.db.ExecContext(ctx,
		`CREATE VIRTUAL TABLE index_fts USING fts5(
			title, keywords, overview,
			content='index_nodes', content_rowid='id',
			tokenize='unicode61'
		)`); err != nil {
		return fmt.Errorf("create index_fts: %w", err)
	}
	if err := ws.syncFTS5Triggers(); err != nil {
		return err
	}
	if _, err := ws.db.ExecContext(ctx,
		`INSERT INTO index_fts(index_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuild index_fts: %w", err)
	}
	return nil
}

func collectDescendantIDs(ctx context.Context, tx *sql.Tx, parentID int) ([]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM index_nodes WHERE parent_id = ?`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
		children, err := collectDescendantIDs(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, children...)
	}
	return ids, rows.Err()
}

// tokenizeForFTS splits a query into CJK-aware tokens for prefix matching.
func tokenizeForFTS(query string) []string {
	// Split on whitespace and punctuation, keep CJK characters as individual tokens.
	var tokens []string
	current := strings.Builder{}
	flush := func() {
		s := strings.TrimSpace(current.String())
		if s != "" {
			tokens = append(tokens, s)
		}
		current.Reset()
	}
	for _, r := range query {
		if r <= 127 && (r == ' ' || r == ',' || r == ';' || r == ':' || r == '.' || r == '?' || r == '!') {
			flush()
		} else if r > 127 {
			// CJK character — each is its own token
			flush()
			tokens = append(tokens, string(r))
		} else {
			current.WriteRune(r)
		}
	}
	flush()
	return tokens
}

func joinStrings(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	if len(ss) == 1 {
		return ss[0]
	}
	n := len(sep) * (len(ss) - 1)
	for i := 0; i < len(ss); i++ {
		n += len(ss[i])
	}
	b := make([]byte, n)
	bp := copy(b, ss[0])
	for _, s := range ss[1:] {
		bp += copy(b[bp:], sep)
		bp += copy(b[bp:], s)
	}
	return string(b)
}

// GetL1Overview returns the L1 node's overview for prompt injection.
// Returns "" when no L1 node exists for the given base.
func (ws *WikiStore) GetL1Overview(ctx context.Context, base string) (string, error) {
	var overview string
	err := ws.db.QueryRowContext(ctx,
		`SELECT overview FROM index_nodes WHERE base = ? AND level = 1 LIMIT 1`, base).Scan(&overview)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return overview, err
}

// CountNodes returns the number of index_nodes at level >= 2 for a base.
func (ws *WikiStore) CountNodes(ctx context.Context, base string) int {
	var count int
	ws.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM index_nodes WHERE base = ? AND level >= 2`, base).Scan(&count)
	return count
}

// wikiStoreCache stores per-(dir,name) wiki store instances.
var wikiStoreCache sync.Map
var wikiStoreMu sync.Mutex

// GetOrOpenWikiStore returns a cached wiki store keyed by (dir, name).
// Each unique combination gets its own SQLite database.
func GetOrOpenWikiStore(name, dir string) (*WikiStore, error) {
	key := dir + "/" + name
	if ws, ok := wikiStoreCache.Load(key); ok {
		return ws.(*WikiStore), nil
	}
	wikiStoreMu.Lock()
	defer wikiStoreMu.Unlock()
	if ws, ok := wikiStoreCache.Load(key); ok {
		return ws.(*WikiStore), nil
	}
	ws, err := NewWikiStore(name, dir)
	if err != nil {
		return nil, err
	}
	wikiStoreCache.Store(key, ws)
	return ws, nil
}

// CloseWikiStore closes all cached wiki stores.
func CloseWikiStore() {
	wikiStoreMu.Lock()
	defer wikiStoreMu.Unlock()
	wikiStoreCache.Range(func(key, value interface{}) bool {
		value.(*WikiStore).Close()
		return true
	})
	wikiStoreCache = sync.Map{}
}

// ListChildren returns paginated child nodes under parentID.
func (ws *WikiStore) ListChildren(ctx context.Context, parentID, page, size int) (*IndexSearchResult, error) {
	if size <= 0 || size > 100 {
		size = 50
	}
	if page <= 0 {
		page = 1
	}

	var total int
	if err := ws.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM index_nodes WHERE parent_id = ?`, parentID).Scan(&total); err != nil {
		return nil, err
	}
	if total == 0 {
		return &IndexSearchResult{Total: 0, Page: page, Size: size, HasMore: false}, nil
	}

	rows, err := ws.db.QueryContext(ctx,
		`SELECT id, level, title, keywords, overview, source, kind
		 FROM index_nodes WHERE parent_id = ?
		 ORDER BY sort_order LIMIT ? OFFSET ?`,
		parentID, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]IndexSearchItem, 0, size)
	for rows.Next() {
		var it IndexSearchItem
		if err := rows.Scan(&it.ID, &it.Level, &it.Title, &it.Keywords, &it.Overview, &it.Source, &it.Kind); err != nil {
			return nil, err
		}
		items = append(items, it)
	}

	return &IndexSearchResult{
		Total:   total,
		Page:    page,
		Size:    size,
		HasMore: (page * size) < total,
		Items:   items,
	}, nil
}

// NodeTreeItem is a flattened node for the tree-view API.
type NodeTreeItem struct {
	ID           int    `json:"id"`
	ParentID     int    `json:"parent_id"`
	Level        int    `json:"level"`
	Title        string `json:"title"`
	Keywords     string `json:"keywords"`
	Overview     string `json:"overview"`
	Source       string `json:"source"`
	Kind         string `json:"kind"`
	ChildCount   int    `json:"child_count"`
	ContentCount int    `json:"content_count"`
}

// ListNodes returns all index_nodes and their child/content counts for a base.
func (ws *WikiStore) ListNodes(ctx context.Context, base string) ([]NodeTreeItem, error) {
	rows, err := ws.db.QueryContext(ctx,
		`SELECT
			n.id, COALESCE(n.parent_id, 0), n.level,
			COALESCE(n.title, ''), COALESCE(n.keywords, ''), COALESCE(n.overview, ''),
			COALESCE(n.source, ''), COALESCE(n.kind, ''),
			(SELECT COUNT(*) FROM index_nodes c WHERE c.parent_id = n.id) AS child_count,
			(SELECT COUNT(*) FROM contents ct WHERE ct.node_id = n.id) AS content_count
		 FROM index_nodes n
		 WHERE n.base = ?
		 ORDER BY n.level, n.sort_order`,
		base)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := make([]NodeTreeItem, 0)
	for rows.Next() {
		var it NodeTreeItem
		if err := rows.Scan(&it.ID, &it.ParentID, &it.Level,
			&it.Title, &it.Keywords, &it.Overview,
			&it.Source, &it.Kind,
			&it.ChildCount, &it.ContentCount); err != nil {
			return nil, err
		}
		nodes = append(nodes, it)
	}
	return nodes, rows.Err()
}

// GetNodeContent returns the content blocks for a given node ID.
func (ws *WikiStore) GetNodeContent(ctx context.Context, nodeID int) ([]ContentNode, error) {
	rows, err := ws.db.QueryContext(ctx,
		`SELECT id, node_id, content, content_type, sort_order
		 FROM contents WHERE node_id = ?
		 ORDER BY sort_order`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	contents := make([]ContentNode, 0)
	for rows.Next() {
		var c ContentNode
		if err := rows.Scan(&c.ID, &c.NodeID, &c.Content, &c.ContentType, &c.SortOrder); err != nil {
			return nil, err
		}
		contents = append(contents, c)
	}
	return contents, rows.Err()
}

// ── Multi-strategy ranked search ──

const (
	weightTitle   = 1.0
	weightKeyword = 0.8
	weightOvervw  = 0.6
	weightL2      = 0.4
	weightContent = 0.2
)

type searchHit struct {
	item     IndexSearchItem
	dedupKey int
	weight   float64
	ftsRank  float64
}

func (ws *WikiStore) LookupSearch(ctx context.Context, query, base string, expand bool, level, page, size int) (*IndexSearchResult, error) {
	if size <= 0 || size > 50 {
		size = 20
	}
	if page <= 0 {
		page = 1
	}

	if query == "" {
		return ws.browseL2(ctx, base, level, page, size)
	}

	return ws.rankedSearch(ctx, query, base, level, expand, page, size)
}

func (ws *WikiStore) browseL2(ctx context.Context, base string, level, page, size int) (*IndexSearchResult, error) {
	baseCond := ``
	args := []interface{}{}
	if base != "" && base != "__all__" {
		baseCond = `AND base = ?`
		args = append(args, base)
	}

	var total int
	row := ws.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM index_nodes WHERE level = 2 `+baseCond, args...)
	if err := row.Scan(&total); err != nil {
		return nil, err
	}
	if total == 0 {
		return &IndexSearchResult{Total: 0, Page: page, Size: size}, nil
	}

	queryArgs := append(args, size, (page-1)*size)
	rows, err := ws.db.QueryContext(ctx,
		`SELECT id, level, title, keywords, overview, source, kind
		 FROM index_nodes WHERE level = 2 `+baseCond+` ORDER BY sort_order LIMIT ? OFFSET ?`,
		queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]IndexSearchItem, 0, size)
	for rows.Next() {
		var it IndexSearchItem
		if err := rows.Scan(&it.ID, &it.Level, &it.Title, &it.Keywords, &it.Overview, &it.Source, &it.Kind); err != nil {
			return nil, err
		}
		items = append(items, it)
	}

	return &IndexSearchResult{
		Total:   total,
		Page:    page,
		Size:    size,
		HasMore: (page * size) < total,
		Items:   items,
	}, rows.Err()
}

func (ws *WikiStore) rankedSearch(ctx context.Context, query, base string, level int, expand bool, page, size int) (*IndexSearchResult, error) {
	seen := make(map[int]bool)
	var hits []searchHit

	baseWhere := `AND n.base = ?`
	baseArg := base
	if base == "" || base == "__all__" {
		baseWhere = ``
	}

	// Strategy 1: L3.title FTS5 prefix match (weight 1.0).
	hits = ws.runFTSQuery(ctx, hits, &seen, query, baseWhere, baseArg, weightTitle,
		`SELECT n.id, n.level, n.title, n.keywords, n.overview, n.source, n.kind,
		        n.parent_id, p.title, bm25(index_fts)
		 FROM index_fts
		 JOIN index_nodes n ON index_fts.rowid = n.id
		 LEFT JOIN index_nodes p ON n.parent_id = p.id
		 WHERE index_fts MATCH ? AND n.level = 3 {:base} ORDER BY rank LIMIT ?`,
		func(q string) string { return fmt.Sprintf(`title:%s*`, q) })

	// Strategy 2: L3.keywords FTS5 prefix match (weight 0.8).
	hits = ws.runFTSQuery(ctx, hits, &seen, query, baseWhere, baseArg, weightKeyword,
		`SELECT n.id, n.level, n.title, n.keywords, n.overview, n.source, n.kind,
		        n.parent_id, p.title, bm25(index_fts)
		 FROM index_fts
		 JOIN index_nodes n ON index_fts.rowid = n.id
		 LEFT JOIN index_nodes p ON n.parent_id = p.id
		 WHERE index_fts MATCH ? AND n.level = 3 {:base} ORDER BY rank LIMIT ?`,
		func(q string) string { return fmt.Sprintf(`keywords:%s*`, q) })

	// Strategy 3: L3.overview FTS5 prefix match (weight 0.6).
	hits = ws.runFTSQuery(ctx, hits, &seen, query, baseWhere, baseArg, weightOvervw,
		`SELECT n.id, n.level, n.title, n.keywords, n.overview, n.source, n.kind,
		        n.parent_id, p.title, bm25(index_fts)
		 FROM index_fts
		 JOIN index_nodes n ON index_fts.rowid = n.id
		 LEFT JOIN index_nodes p ON n.parent_id = p.id
		 WHERE index_fts MATCH ? AND n.level = 3 {:base} ORDER BY rank LIMIT ?`,
		func(q string) string { return fmt.Sprintf(`overview:%s*`, q) })

	// Strategy 4: L2 FTS5 match (weight 0.4).
	hits = ws.runFTSQuery(ctx, hits, &seen, query, baseWhere, baseArg, weightL2,
		`SELECT n.id, n.level, n.title, n.keywords, n.overview, n.source, n.kind,
		        n.parent_id, p.title, bm25(index_fts)
		 FROM index_fts
		 JOIN index_nodes n ON index_fts.rowid = n.id
		 LEFT JOIN index_nodes p ON n.parent_id = p.id
		 WHERE index_fts MATCH ? AND n.level = 2 {:base} ORDER BY rank LIMIT ?`,
		func(q string) string { return fmt.Sprintf(`%s*`, q) })

	// Strategy 5: contents.content LIKE fallback (weight 0.2).
	if len(hits) == 0 {
		hits = ws.addContentLikeHits(ctx, hits, &seen, query, baseWhere, baseArg, weightContent)
	}

	sortHitsByRank(hits)

	total := len(hits)
	if total == 0 {
		return &IndexSearchResult{Total: 0, Page: page, Size: size}, nil
	}

	start := (page - 1) * size
	if start >= len(hits) {
		return &IndexSearchResult{Total: total, Page: page, Size: size, HasMore: false}, nil
	}
	end := start + size
	if end > len(hits) {
		end = len(hits)
	}
	pageItems := hits[start:end]

	items := make([]IndexSearchItem, len(pageItems))
	for i, h := range pageItems {
		items[i] = h.item
		if h.ftsRank > 0 {
			items[i].Rank = (h.ftsRank / (h.ftsRank + 1.0)) * h.weight
		}
	}

	if expand {
		for i := range items {
			if items[i].Level == 3 {
				children, err := ws.loadContents(ctx, items[i].ID)
				if err == nil {
					items[i].Children = children
				}
			}
		}
	}

	return &IndexSearchResult{
		Total:   total,
		Page:    page,
		Size:    size,
		HasMore: (page * size) < total,
		Items:   items,
	}, nil
}

func (ws *WikiStore) runFTSQuery(ctx context.Context, hits []searchHit, seen *map[int]bool, query, baseWhere, baseArg string, weight float64, sqlTmpl string, ftsExpr func(string) string) []searchHit {
	ftsQ := ftsExpr(escapeFTS5(query))
	sql := strings.ReplaceAll(sqlTmpl, `{:base}`, baseWhere)

	var args []interface{}
	args = append(args, ftsQ)
	if baseWhere != "" {
		args = append(args, baseArg)
	}
	args = append(args, 50)

	rows, err := ws.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return hits
	}
	defer rows.Close()

	for rows.Next() {
		var it IndexSearchItem
		var parentID, parentTitle string
		var rank float64
		if err := rows.Scan(&it.ID, &it.Level, &it.Title, &it.Keywords, &it.Overview, &it.Source, &it.Kind,
			&parentID, &parentTitle, &rank); err != nil {
			continue
		}
		if (*seen)[it.ID] {
			continue
		}
		(*seen)[it.ID] = true

		it.Parent = &NodeRef{}
		if pid, ok := parseStrInt(parentID); ok && pid > 0 {
			it.Parent.ID = pid
			it.Parent.Title = parentTitle
		}

		hits = append(hits, searchHit{item: it, dedupKey: it.ID, weight: weight, ftsRank: rank})
	}
	return hits
}

func escapeFTS5(q string) string {
	tokens := tokenizeForFTS(q)
	if len(tokens) == 0 {
		return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
	}
	var parts []string
	for _, t := range tokens {
		parts = append(parts, t)
	}
	return strings.Join(parts, " OR ")
}

func (ws *WikiStore) addContentLikeHits(ctx context.Context, hits []searchHit, seen *map[int]bool, query, baseWhere, baseArg string, weight float64) []searchHit {
	var args []interface{}
	args = append(args, "%"+query+"%")
	if baseWhere != "" && baseArg != "" {
		args = append(args, baseArg)
	}
	args = append(args, 50)

	sqlStmt := `SELECT n.id, n.level, n.title, n.keywords, n.overview, n.source, n.kind,
		            n.parent_id, p.title
		     FROM contents c
		     JOIN index_nodes n ON c.node_id = n.id
		     LEFT JOIN index_nodes p ON n.parent_id = p.id
		     WHERE n.level >= 2 AND c.content LIKE ? ` + baseWhere + `
		     ORDER BY n.id LIMIT ?`

	rows, err := ws.db.QueryContext(ctx, sqlStmt, args...)
	if err != nil {
		return hits
	}
	defer rows.Close()

	for rows.Next() {
		var it IndexSearchItem
		var parentID, parentTitle string
		if err := rows.Scan(&it.ID, &it.Level, &it.Title, &it.Keywords, &it.Overview, &it.Source, &it.Kind,
			&parentID, &parentTitle); err != nil {
			continue
		}
		if (*seen)[it.ID] {
			continue
		}
		(*seen)[it.ID] = true

		it.Parent = &NodeRef{}
		if pid, ok := parseStrInt(parentID); ok && pid > 0 {
			it.Parent.ID = pid
			it.Parent.Title = parentTitle
		}

		hits = append(hits, searchHit{
			item:     it,
			dedupKey: it.ID,
			weight:   weight,
			ftsRank:  0.1, // low baseline for LIKE hits
		})
	}
	return hits
}

func sortHitsByRank(hits []searchHit) {
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && lessHit(hits[j], hits[j-1]); j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
}

func lessHit(a, b searchHit) bool {
	ra := a.weight * a.ftsRank
	rb := b.weight * b.ftsRank
	return ra > rb
}

func (ws *WikiStore) loadContents(ctx context.Context, nodeID int) ([]ContentNode, error) {
	rows, err := ws.db.QueryContext(ctx,
		`SELECT id, node_id, content, content_type, sort_order
		 FROM contents WHERE node_id = ? ORDER BY sort_order`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContentNode
	for rows.Next() {
		var c ContentNode
		if err := rows.Scan(&c.ID, &c.NodeID, &c.Content, &c.ContentType, &c.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func parseStrInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	var n int
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, true
}

// ClearBase removes all sections and index nodes for a base from the database.
// Does NOT delete the wiki.db file on disk — just clears the tables.
func (ws *WikiStore) ClearBase(ctx context.Context, base string) error {
	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete from the old wiki_sections table (manually sync FTS).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM wiki_fts WHERE rowid IN (SELECT id FROM wiki_sections WHERE base = ?)`, base); err != nil {
		return fmt.Errorf("clear wiki_fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM wiki_sections WHERE base = ?`, base); err != nil {
		return fmt.Errorf("clear wiki_sections: %w", err)
	}

	// Delete from index_nodes — FTS triggers handle index_fts automatically.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM contents WHERE node_id IN (SELECT id FROM index_nodes WHERE base = ?)`, base); err != nil {
		return fmt.Errorf("clear contents: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM index_nodes WHERE base = ?`, base); err != nil {
		return fmt.Errorf("clear index_nodes: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM file_mtimes WHERE base = ?`, base); err != nil {
		return fmt.Errorf("clear file_mtimes: %w", err)
	}
	return tx.Commit()
}

// RemoveStaleSourceNodes removes nodes + contents for sources not in currentSources.
func (ws *WikiStore) RemoveStaleSourceNodes(ctx context.Context, base string, currentSources map[string]bool) error {
	rows, err := ws.db.QueryContext(ctx,
		`SELECT DISTINCT source FROM index_nodes WHERE base = ? AND level = 2`, base)
	if err != nil {
		return err
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			continue
		}
		if !currentSources[src] {
			stale = append(stale, src)
		}
	}
	if len(stale) == 0 {
		return nil
	}

	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, src := range stale {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		tx.ExecContext(ctx,
			`DELETE FROM index_fts WHERE rowid IN (SELECT id FROM index_nodes WHERE base = ? AND source = ?)`, base, src)
		tx.ExecContext(ctx,
			`DELETE FROM contents WHERE node_id IN (SELECT id FROM index_nodes WHERE base = ? AND source = ?)`, base, src)
		tx.ExecContext(ctx, `DELETE FROM index_nodes WHERE base = ? AND source = ?`, base, src)
	}
	return tx.Commit()
}

// ── Data migration from legacy wiki_sections ──

// MigrateBaseToIndex converts legacy wiki_sections data for a base into
// the three-level index_nodes + contents format. Safe to call repeatedly —
// skips when index_nodes already has data for this base.
func (ws *WikiStore) MigrateBaseToIndex(ctx context.Context, base string) (int, error) {
	var count int
	if err := ws.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM index_nodes WHERE base = ?`, base).Scan(&count); err == nil && count > 0 {
		return count, nil
	}

	rows, err := ws.db.QueryContext(ctx,
		`SELECT id, title, content, source, heading FROM wiki_sections WHERE base = ? ORDER BY source, id`, base)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type section struct {
		wikiSectionID int
		title, content, source string
		heading sql.NullString
	}
	type sourceGroup struct {
		source   string
		sections []section
	}
	groups := make(map[string]*sourceGroup)
	var groupOrder []string

	for rows.Next() {
		var s section
		if err := rows.Scan(&s.wikiSectionID, &s.title, &s.content, &s.source, &s.heading); err != nil {
			continue
		}
		if _, ok := groups[s.source]; !ok {
			groups[s.source] = &sourceGroup{source: s.source}
			groupOrder = append(groupOrder, s.source)
		}
		groups[s.source].sections = append(groups[s.source].sections, s)
	}
	if len(groupOrder) == 0 {
		return 0, nil
	}

	nextID := 2 // L1 = 1
	var l2Nodes []IndexNode
	var allL3s []IndexNode
	var allContents []ContentNode

	for fi, src := range groupOrder {
		g := groups[src]
		l2Title := src
		if idx := strings.LastIndex(src, "/"); idx >= 0 {
			l2Title = src[idx+1:]
		}
		l2ID := nextID
		nextID++

		var l3Titles []string
		for _, s := range g.sections {
			l3Titles = append(l3Titles, s.title)
		}
		l2Overview := l2Title
		if len(l3Titles) > 0 {
			l2Overview = fmt.Sprintf("%s — %d 章节: %s", l2Title, len(l3Titles),
				strings.Join(l3Titles[:int(mathMin(len(l3Titles), 3))], ", "))
		}

		l2Nodes = append(l2Nodes, IndexNode{
			ID:        l2ID,
			ParentID:  1,
			Base:      base,
			Level:     2,
			Source:    src,
			Kind:      "text",
			SortOrder: fi,
			Title:     l2Title,
			Keywords:  "",
			Overview:  l2Overview,
		})

		for si, s := range g.sections {
			l3ID := nextID
			nextID++
			heading := ""
			if s.heading.Valid {
				heading = s.heading.String
			}
			_ = heading

			allL3s = append(allL3s, IndexNode{
				ID:        l3ID,
				ParentID:  l2ID,
				Base:      base,
				Level:     3,
				Source:    src,
				Kind:      "text",
				SortOrder: si,
				Title:     s.title,
				Keywords:  "",
				Overview:  truncateStr(s.content, 500),
			})

			allContents = append(allContents, ContentNode{
				NodeID:      l3ID,
				Content:     truncateStr(s.content, 3000),
				ContentType: "text",
				SortOrder:   0,
			})
		}
	}

	l1Overview := buildL1OverviewFromNodes(l2Nodes)
	l1Node := IndexNode{
		ID:        1,
		ParentID:  0,
		Base:      base,
		Level:     1,
		Title:     base,
		Keywords:  "",
		Overview:  l1Overview,
		SortOrder: 0,
	}

	allNodes := make([]IndexNode, 0, 1+len(l2Nodes)+len(allL3s))
	allNodes = append(allNodes, l1Node)
	allNodes = append(allNodes, l2Nodes...)
	allNodes = append(allNodes, allL3s...)

	if err := ws.ReplaceBaseNodes(ctx, base, allNodes, allContents); err != nil {
		return 0, fmt.Errorf("migrate base %s: %w", base, err)
	}

	log.Printf("[migrate] %s: %d sections → L1 + %d L2 + %d L3 + %d contents",
		base, len(allL3s), len(l2Nodes), len(allL3s), len(allContents))
	return len(allNodes), nil
}

// TruncateText clips s to max runes, appending nothing.
func TruncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func mathMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func buildL1OverviewFromNodes(l2Nodes []IndexNode) string {
	if len(l2Nodes) == 0 {
		return "[Knowledge Base]\n(no files indexed)\n"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Knowledge Base] (%d files)\n", len(l2Nodes)))
	count := 0
	for _, l2 := range l2Nodes {
		if sb.Len() > 2000 {
			sb.WriteString(fmt.Sprintf("  ...(+%d files omitted)\n", len(l2Nodes)-count))
			break
		}
		sb.WriteString(fmt.Sprintf("· %s\n", l2.Overview))
		count++
	}
	return sb.String()
}

// EnsureMigrated checks if index_nodes has data for each known base and
// migrates legacy wiki_sections data if needed. Called at startup.
func EnsureMigrated(bases []BaseRef) {
	for _, base := range bases {
		store, err := GetOrOpenWikiStore(base.Name, base.Path)
		if err != nil {
			continue
		}
		if _, err := store.MigrateBaseToIndex(context.Background(), base.Name); err != nil {
			log.Printf("[migrate] base %s failed: %v", base.Name, err)
		}
	}
}

// BaseRef is a lightweight reference to a knowledge base for migration.
type BaseRef struct {
	Name    string
	Path    string
	Enabled bool
}
