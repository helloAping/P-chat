// Package recall — semantic-search engine for the `/recall` slash
// command and the agent's autonomous recall tool.
//
// The Engine wraps a knowledge.WikiStore (FTS5-backed SQLite index)
// so callers don't need to know about knowledge base configuration
// or pagination. The Engine is intentionally narrow: one Print
// method, one Search method, no goroutines, no caching layer of
// its own (the underlying WikiStore has its own LRU).
//
// History: this package used to ship as a 5-line stub that
// returned nil from every method. T12 of the project plan
// replaces the stub with a real implementation. Wiring
// (calling SetRecallEngine from cmd/pchat) is part of T13.
package recall

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	_ "modernc.org/sqlite" // sqlite driver for the underlying wiki store

	"github.com/p-chat/pchat/internal/knowledge"
)

// Engine performs a semantic / keyword search across the
// configured wiki knowledge base. Construct one with NewEngine
// per active wiki base, then pass to REPL.SetRecallEngine.
type Engine struct {
	wikiName string // wiki base name passed to GetOrOpenWikiStore
	wikiDir  string // directory the wiki.db lives in
	out      io.Writer
}

// NewEngine returns an Engine that searches the wiki base
// identified by (wikiName, wikiDir). Pass wikiName="" to search
// all bases (__all__). Pass an io.Writer to redirect output
// (default: os.Stdout) — tests pass a *bytes.Buffer.
func NewEngine(wikiName, wikiDir string, out io.Writer) *Engine {
	if out == nil {
		out = defaultWriter{}
	}
	return &Engine{
		wikiName: wikiName,
		wikiDir:  wikiDir,
		out:      out,
	}
}

// defaultWriter is a tiny shim so we don't have to import "os"
// in this package's hot path; the standard library's os.Stdout
// is the concrete writer we use at the call site.
type defaultWriter struct{}

func (defaultWriter) Write(p []byte) (int, error) {
	return fmt.Print(string(p))
}

// Search runs a query and returns the top-k results without
// printing anything. Returns ErrNoWiki when the underlying
// store can't be opened; callers can choose to fall back to
// a different base or surface the error to the user.
func (e *Engine) Search(ctx context.Context, query string, topK int) ([]knowledge.IndexSearchItem, error) {
	if topK <= 0 {
		topK = 5
	}
	if topK > 50 {
		topK = 50
	}

	store, err := knowledge.GetOrOpenWikiStore(e.wikiName, e.wikiDir)
	if err != nil {
		return nil, fmt.Errorf("open wiki store %q: %w", e.wikiName, err)
	}

	res, err := store.LookupSearch(ctx, query, e.wikiName, false, 0, 1, topK)
	if err != nil {
		return nil, fmt.Errorf("wiki search: %w", err)
	}
	if res == nil {
		return nil, nil
	}
	return res.Items, nil
}

// PrintSearch runs a query and writes a human-readable
// summary of the top-k results to the engine's writer.
// Accepts ctx as any (not context.Context) so this package
// can stay free of the context import; the only callers
// pass a real context, which we type-assert.
//
// This is the legacy entry point used by the REPL's
// `/recall <query>` command; the agent's `recall` tool
// prefers Search + structured return.
func (e *Engine) PrintSearch(ctx any, query string, topK int) error {
	c, _ := ctx.(context.Context)
	if c == nil {
		c = context.Background()
	}

	items, err := e.Search(c, query, topK)
	if err != nil {
		fmt.Fprintf(e.out, "recall: %v\n", err)
		return err
	}
	if len(items) == 0 {
		fmt.Fprintf(e.out, "recall: no results for %q\n", query)
		return nil
	}

	for i, it := range items {
		if i >= topK {
			break
		}
		title := it.Title
		if title == "" {
			title = it.Source
		}
		overview := it.Overview
		if overview == "" {
			overview = "(no overview)"
		}
		// Trim overview to a single line for the CLI summary view.
		if idx := strings.IndexAny(overview, "\n\r"); idx >= 0 {
			overview = overview[:idx]
		}
		if len([]rune(overview)) > 120 {
			overview = string([]rune(overview)[:120]) + "…"
		}
		fmt.Fprintf(e.out, "%2d. %s\n    %s\n    source: %s\n",
			i+1, title, overview, it.Source)
	}
	fmt.Fprintf(e.out, "— %d result(s) for %q in %s\n", len(items), query, time.Now().Format("15:04:05"))
	return nil
}

// Close releases the underlying wiki store's file handles so
// the test harness can clean up its temp dir. Production
// callers don't need to call this — the WikiStore cache keeps
// the store open for the process lifetime.
func (e *Engine) Close() error {
	store, err := knowledge.GetOrOpenWikiStore(e.wikiName, e.wikiDir)
	if err != nil {
		return err
	}
	return store.Close()
}
