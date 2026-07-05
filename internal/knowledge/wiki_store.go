package knowledge

import (
	"context"
	"database/sql"
	"fmt"
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
	`)
	return err
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
