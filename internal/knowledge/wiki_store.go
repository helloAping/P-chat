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

// WikiSection is one structured entry in the knowledge base.
type WikiSection struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Source  string `json:"source"` // relative path within base
	Base    string `json:"base"`   // knowledge base name
	Heading string `json:"heading,omitempty"`
}

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
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, fmt.Errorf("open wiki db: %w", err)
	}
	ws := &WikiStore{db: db, dir: dir, name: name}
	if err := ws.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return ws, nil
}

func (ws *WikiStore) migrate() error {
	_, err := ws.db.Exec(`
		CREATE TABLE IF NOT EXISTS wiki_sections (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			title   TEXT    NOT NULL,
			content TEXT    NOT NULL,
			source  TEXT    NOT NULL,
			base    TEXT    NOT NULL,
			heading TEXT
		);
		CREATE VIRTUAL TABLE IF NOT EXISTS wiki_fts USING fts5(
			title,
			content,
			source,
			tokenize='unicode61'
		);
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
		-- ── Three-level index tables (v2 schema) ──
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
		-- FTS5 index — sync via triggers, covers title+keywords+overview
		CREATE VIRTUAL TABLE IF NOT EXISTS index_fts USING fts5(
			title, keywords, overview,
			content='index_nodes', content_rowid='id',
			tokenize='unicode61'
		);
	`)
	if err != nil {
		return err
	}
	return ws.syncFTS5Triggers()
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

// RemoveStaleSources deletes sections (and FTS entries) for sources in the
// given base that are NOT in currentSources. Also cleans file_mtimes.
func (ws *WikiStore) RemoveStaleSources(ctx context.Context, base string, currentSources map[string]bool) error {
	rows, err := ws.db.QueryContext(ctx,
		`SELECT DISTINCT source FROM wiki_sections WHERE base = ?`, base)
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
			`DELETE FROM wiki_fts WHERE rowid IN (SELECT id FROM wiki_sections WHERE base = ? AND source = ?)`, base, src)
		tx.ExecContext(ctx, `DELETE FROM wiki_sections WHERE base = ? AND source = ?`, base, src)
		tx.ExecContext(ctx, `DELETE FROM file_mtimes WHERE base = ? AND source = ?`, base, src)
	}
	return tx.Commit()
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

// ReplaceBase clears all sections for a base and inserts the new set in a
// single transaction. The FTS5 index is kept in sync.
func (ws *WikiStore) ReplaceBase(ctx context.Context, base string, sections []WikiSection) error {
	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM wiki_fts WHERE rowid IN (SELECT id FROM wiki_sections WHERE base = ?)`, base); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wiki_sections WHERE base = ?`, base); err != nil {
		return err
	}

	return insertSectionsTx(ctx, tx, base, sections)
}

// ReplaceSource deletes all sections for a single source within a base,
// then inserts the new set. Used by incremental scanning.
func (ws *WikiStore) ReplaceSource(ctx context.Context, base, source string, sections []WikiSection) error {
	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM wiki_fts WHERE rowid IN (SELECT id FROM wiki_sections WHERE base = ? AND source = ?)`, base, source); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM wiki_sections WHERE base = ? AND source = ?`, base, source); err != nil {
		return err
	}

	return insertSectionsTx(ctx, tx, base, sections)
}

func insertSectionsTx(ctx context.Context, tx *sql.Tx, base string, sections []WikiSection) error {

	insertSection, err := tx.PrepareContext(ctx,
		`INSERT INTO wiki_sections (title, content, source, base, heading) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insertSection.Close()

	insertFTS, err := tx.PrepareContext(ctx,
		`INSERT INTO wiki_fts (rowid, title, content, source) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insertFTS.Close()

	for _, s := range sections {
		res, err := insertSection.ExecContext(ctx, s.Title, s.Content, s.Source, base, s.Heading)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := insertFTS.ExecContext(ctx, id, s.Title, s.Content, s.Source); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AppendSections inserts new sections without clearing existing data.
func (ws *WikiStore) AppendSections(ctx context.Context, sections []WikiSection) error {
	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	insertSection, err := tx.PrepareContext(ctx,
		`INSERT INTO wiki_sections (title, content, source, base, heading) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insertSection.Close()

	insertFTS, err := tx.PrepareContext(ctx,
		`INSERT INTO wiki_fts (rowid, title, content, source) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insertFTS.Close()

	for _, s := range sections {
		res, err := insertSection.ExecContext(ctx, s.Title, s.Content, s.Source, s.Base, s.Heading)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := insertFTS.ExecContext(ctx, id, s.Title, s.Content, s.Source); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetSection returns a single section by its primary key.
func (ws *WikiStore) GetSection(ctx context.Context, id int64) (*WikiSection, error) {
	var s WikiSection
	var heading sql.NullString
	err := ws.db.QueryRowContext(ctx,
		`SELECT id, title, content, source, base, heading FROM wiki_sections WHERE id = ?`, id).
		Scan(&s.ID, &s.Title, &s.Content, &s.Source, &s.Base, &heading)
	if err != nil {
		return nil, err
	}
	s.Heading = heading.String
	return &s, nil
}

// DeleteSection removes a single section and its FTS entry by id.
func (ws *WikiStore) DeleteSection(ctx context.Context, id int64) error {
	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM wiki_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wiki_sections WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// InsertSection inserts a single section and returns its id.
func (ws *WikiStore) InsertSection(ctx context.Context, s WikiSection) (int64, error) {
	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO wiki_sections (title, content, source, base, heading) VALUES (?,?,?,?,?)`,
		s.Title, s.Content, s.Source, s.Base, s.Heading)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wiki_fts (rowid, title, content, source) VALUES (?,?,?,?)`,
		id, s.Title, s.Content, s.Source); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateSection updates the title and content of a section and its FTS entry.
func (ws *WikiStore) UpdateSection(ctx context.Context, id int64, title, content string) error {
	tx, err := ws.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE wiki_sections SET title = ?, content = ? WHERE id = ?`, title, content, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE wiki_fts SET title = ?, content = ? WHERE rowid = ?`, title, content, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SearchFTS does a full-text search across titles and returns ranked results.
// Falls back to LIKE if FTS5 returns nothing.
func (ws *WikiStore) SearchFTS(ctx context.Context, query string, topK int) ([]WikiSection, error) {
	if topK <= 0 {
		topK = 5
	}
	// Strategy 1: prefix match on title terms
	terms := tokenizeForFTS(query)
	// Strategy 2: full query as phrase
	// Strategy 3: content search as fallback

	// Build FTS5 query: try title prefix + content match
	var ftsQueries []string
	if len(terms) > 0 {
		ftsQueries = append(ftsQueries,
			buildFTSClause("title", terms),
		)
	}
	ftsQueries = append(ftsQueries,
		fmt.Sprintf(`"%s"`, escapeFTS(query)),
	)

	for _, ftsQuery := range ftsQueries {
		sections, err := ws.ftsSearch(ctx, ftsQuery, topK)
		if err == nil && len(sections) > 0 {
			return sections, nil
		}
		// nil error + 0 results = no match, try next strategy.
		// non-nil error = real DB issue, try LIKE fallback.
	}

	// Strategy 4: LIKE fallback on title
	rows, err := ws.db.QueryContext(ctx,
		`SELECT id, title, content, source, base, heading FROM wiki_sections
		 WHERE title LIKE ? ORDER BY title LIMIT ?`,
		"%"+query+"%", topK)
	if err != nil {
		return nil, err
	}
	sections, _ := scanSections(rows)
	rows.Close()
	if len(sections) == 0 {
		// Strategy 5: LIKE fallback on content
		rows2, err := ws.db.QueryContext(ctx,
			`SELECT id, title, content, source, base, heading FROM wiki_sections
			 WHERE content LIKE ? ORDER BY title LIMIT ?`,
			"%"+query+"%", topK)
		if err != nil {
			return nil, err
		}
		defer rows2.Close()
		return scanSections(rows2)
	}
	return sections, nil
}

func (ws *WikiStore) ftsSearch(ctx context.Context, ftsQuery string, topK int) ([]WikiSection, error) {
	rows, err := ws.db.QueryContext(ctx,
		`SELECT id, title, content, source, base, heading
		 FROM wiki_fts WHERE wiki_fts MATCH ? ORDER BY rank LIMIT ?`,
		ftsQuery, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanSections(rows)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListBase returns all section titles for a base (or all bases if base is empty).
// When base is empty the result is capped at 5000 rows to avoid unbounded memory use.
func (ws *WikiStore) ListBase(ctx context.Context, base string) ([]WikiSection, error) {
	var rows *sql.Rows
	var err error
	if base == "" {
		rows, err = ws.db.QueryContext(ctx,
			`SELECT id, title, content, source, base, heading
			 FROM wiki_sections ORDER BY source, id LIMIT 5000`)
	} else {
		rows, err = ws.db.QueryContext(ctx,
			`SELECT id, title, content, source, base, heading
			 FROM wiki_sections WHERE base = ? ORDER BY source, id`, base)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSections(rows)
}

// Index returns a compact index (titles grouped by source) for the system prompt.
func (ws *WikiStore) Index(ctx context.Context, maxSections int) (string, error) {
	if maxSections <= 0 {
		maxSections = 500
	}
	rows, err := ws.db.QueryContext(ctx,
		`SELECT title, source FROM wiki_sections ORDER BY source, id LIMIT ?`, maxSections)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	type entry struct{ title, source string }
	var entries []entry
	for rows.Next() {
		var title, source string
		if err := rows.Scan(&title, &source); err != nil {
			continue
		}
		entries = append(entries, entry{title, source})
	}
	if len(entries) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("## Knowledge Base Index\n\n")
	currentSource := ""
	count := 0
	for _, e := range entries {
		if e.source != currentSource {
			if count > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "### %s\n", e.source)
			currentSource = e.source
			count = 0
		}
		count++
		b.WriteString(fmt.Sprintf("  - %s\n", e.title))
	}
	if count > 0 {
		b.WriteString("\n")
	}
	b.WriteString("> 使用 `wiki_lookup(title=\"...\")` 获取条目全文。\n")
	b.WriteString("> 使用 `grep(pattern=\"...\")` 搜索文件精确文本。\n")
	return b.String(), rows.Err()
}

func scanSections(rows *sql.Rows) ([]WikiSection, error) {
	var out []WikiSection
	for rows.Next() {
		var s WikiSection
		var heading sql.NullString
		if err := rows.Scan(&s.ID, &s.Title, &s.Content, &s.Source, &s.Base, &heading); err != nil {
			return out, err
		}
		s.Heading = heading.String
		out = append(out, s)
	}
	return out, rows.Err()
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
		if r <= 127 && (r == ' ' || r == ',' || r == ';' || r == ':' || r == '！' || r == '，' || r == '。') {
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

func buildFTSClause(field string, terms []string) string {
	var parts []string
	for _, t := range terms {
		parts = append(parts,
			fmt.Sprintf("%s:%s*", field, escapeFTS(t)))
	}
	return strings.Join(parts, " OR ")
}

func escapeFTS(s string) string {
	// Quote the term for FTS5; double quotes inside need doubling.
	s = strings.ReplaceAll(s, `"`, `""`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return `"` + s + `"`
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
	// Double-check after acquiring lock.
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

// ── Three-level index node operations ──

// ReplaceBaseNodes clears all nodes + contents for a base and inserts the
// new set in a single transaction. The index_fts is kept in sync via triggers.
func (ws *WikiStore) ReplaceBaseNodes(ctx context.Context, base string, nodes []IndexNode, contents []ContentNode) error {
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
	return strings.ReplaceAll(q, `"`, `""`)
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
