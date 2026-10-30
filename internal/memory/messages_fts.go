package memory

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

// EnsureMessagesFTS creates the external-content FTS5 index for message
// search (PERF-02), syncs triggers, and rebuilds the index. Idempotent.
// When the messages table is absent (partial upgrade test DBs), this is a no-op;
// memory migrate v14 / Open will create the index once messages exists.
func EnsureMessagesFTS(db *sql.DB) error {
	if db == nil {
		return nil
	}
	if !hasTable(db, "messages") {
		return nil
	}
	if err := createMessagesFTSTable(db); err != nil {
		return err
	}
	if err := syncMessagesFTSTriggers(db); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuild messages_fts: %w", err)
	}
	return nil
}

// ensureMessagesFTSReady makes sure the FTS table and triggers exist without
// forcing a full rebuild on every Open when the index is already present.
func ensureMessagesFTSReady(db *sql.DB) error {
	if db == nil {
		return nil
	}
	if hasMessagesFTS(db) {
		return syncMessagesFTSTriggers(db)
	}
	return EnsureMessagesFTS(db)
}

func createMessagesFTSTable(db *sql.DB) error {
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
		content,
		content='messages',
		content_rowid='id',
		tokenize='unicode61'
	)`); err != nil {
		return fmt.Errorf("create messages_fts: %w", err)
	}
	return nil
}

func syncMessagesFTSTriggers(db *sql.DB) error {
	stmts := []string{
		`DROP TRIGGER IF EXISTS messages_fts_ai`,
		`DROP TRIGGER IF EXISTS messages_fts_ad`,
		`DROP TRIGGER IF EXISTS messages_fts_au`,
		`CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
		   INSERT INTO messages_fts(rowid, content) VALUES (NEW.id, NEW.content);
		 END`,
		`CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
		   INSERT INTO messages_fts(messages_fts, rowid, content)
		   VALUES ('delete', OLD.id, OLD.content);
		 END`,
		`CREATE TRIGGER messages_fts_au AFTER UPDATE OF content ON messages BEGIN
		   INSERT INTO messages_fts(messages_fts, rowid, content)
		   VALUES ('delete', OLD.id, OLD.content);
		   INSERT INTO messages_fts(rowid, content) VALUES (NEW.id, NEW.content);
		 END`,
	}
	for _, sqlStmt := range stmts {
		if _, err := db.Exec(sqlStmt); err != nil {
			return fmt.Errorf("messages_fts trigger: %w", err)
		}
	}
	return nil
}

func hasMessagesFTS(db *sql.DB) bool {
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name='messages_fts'`,
	).Scan(&name)
	return err == nil && name == "messages_fts"
}

// searchQueryPreferFTS reports whether q can use unicode61 FTS5 usefully.
// CJK queries fall back to LIKE: unicode61 treats consecutive CJK runes as
// one token, so mid-phrase Chinese would miss under MATCH.
func searchQueryPreferFTS(q string) bool {
	for _, r := range q {
		if r > 127 {
			return false
		}
	}
	return true
}

// buildMessagesFTSQuery turns a user search string into an FTS5 MATCH
// expression. CJK runes become individual tokens; ASCII words get a
// prefix wildcard. `%` and `_` stay literal (replacing LIKE ESCAPE).
func buildMessagesFTSQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return `""`
	}
	tokens := tokenizeMessagesFTS(q)
	if len(tokens) == 0 {
		return quoteFTSPhrase(q)
	}
	parts := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		quoted := quoteFTSToken(tok)
		if isSingleCJK(tok) {
			parts = append(parts, quoted)
			continue
		}
		// Prefix match for Latin/digit tokens.
		parts = append(parts, quoted+"*")
	}
	return strings.Join(parts, " AND ")
}

func tokenizeMessagesFTS(query string) []string {
	var tokens []string
	var current strings.Builder
	flush := func() {
		s := strings.TrimSpace(current.String())
		if s != "" {
			tokens = append(tokens, s)
		}
		current.Reset()
	}
	for _, r := range query {
		switch {
		case r <= 127 && (unicode.IsSpace(r) || strings.ContainsRune(`,;:.?!`, r)):
			flush()
		case r > 127:
			flush()
			tokens = append(tokens, string(r))
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return tokens
}

func isSingleCJK(tok string) bool {
	runes := []rune(tok)
	return len(runes) == 1 && runes[0] > 127
}

func quoteFTSToken(tok string) string {
	if strings.ContainsAny(tok, `"*^():`) {
		return quoteFTSPhrase(tok)
	}
	return tok
}

func quoteFTSPhrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
