package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/export"
	"github.com/p-chat/pchat/internal/memory"
)

// ExportSession renders a session to markdown or JSON and
// returns it as an attachment download. The handler is
// the canonical export path: it reads the rich row shape
// straight from the memory store (so the output is always
// self-contained — no in-memory blob: URLs to break, no
// dependency on the SPA's render pipeline).
//
// URL shape:
//
//	GET /api/v1/sessions/:id/export?format=md|markdown|json
//
// Defaults: format=markdown.
//
// Response headers:
//
//	Content-Type:        text/markdown | application/json
//	Content-Disposition: attachment; filename="<safe>.md"
//	Content-Length:      <bytes> (set explicitly so the
//	                     browser can show a progress bar
//	                     on large exports)
//
// Status codes:
//
//	200 — file body
//	400 — unknown / missing format
//	404 — session not found
//	503 — memory store not configured
//
// The implementation shares the rendering core with the
// CLI /export command (internal/export package), so a
// session exported from the REPL and one exported from
// the SPA produce byte-identical output for the same
// data. The CLI path additionally does the file I/O
// (write to disk) and arg parsing; the HTTP path
// additionally does content-type / disposition headers
// and the per-request file body.
func (h *Handler) ExportSession(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session id is required"})
		return
	}
	// Verify the session exists before we ask the
	// store to render anything. GetMessagesFullByID
	// silently returns nil for an unknown id, which
	// would otherwise surface as a 200 with an empty
	// envelope — confusing.
	convVal, err := h.store.GetConversation(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("session not found: %s", err)})
		return
	}
	conv := &convVal
	formatStr := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "markdown")))
	format, err := parseExportFormat(formatStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	msgs := h.store.GetMessagesFullByID(id)
	if len(msgs) == 0 {
		// Empty session still produces a valid
		// header-only file; the user almost
		// certainly wants to download it anyway
		// rather than see a 404.
	}

	var (
		body    string
		ext     string
		mime    string
		renderErr error
	)
	switch format {
	case export.FormatMarkdown:
		body = export.ToMarkdown(conv, msgs)
		ext = "md"
		mime = "text/markdown; charset=utf-8"
	case export.FormatJSON:
		body, renderErr = export.ToJSON(conv, msgs)
		ext = "json"
		mime = "application/json; charset=utf-8"
	}
	if renderErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": renderErr.Error()})
		return
	}

	// Two filenames: the plain `filename="..."` is
	// always ASCII (built from the session id +
	// timestamp) so it round-trips through every
	// HTTP client and OS file dialog without
	// mangling. The `filename*=UTF-8''...` carries
	// the human-readable title for browsers that
	// honour RFC 5987 (all of them since ~2015);
	// they prefer the encoded form when both are
	// present, so the user gets a nicely named
	// file. The frontend parses `filename*` first
	// and falls back to `filename=`.
	asciiName := asciiFilename(conv, ext)
	unicodeName := unicodeFilename(conv, ext)
	c.Header("Content-Type", mime)
	c.Header("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
			asciiName, export.URLEncodeFilename(unicodeName)))
	c.Header("Content-Length", fmt.Sprintf("%d", len(body)))
	c.String(http.StatusOK, body)
}

// parseExportFormat normalises the ?format= query value.
// Accepts "md" as a synonym for "markdown". Anything
// else returns a 400-grade error.
func parseExportFormat(s string) (export.Format, error) {
	switch s {
	case "markdown", "md", "":
		return export.FormatMarkdown, nil
	case "json":
		return export.FormatJSON, nil
	default:
		return "", fmt.Errorf("unknown export format: %q (want markdown | json)", s)
	}
}

// asciiFilename produces the safe ASCII filename used
// for the plain `filename="..."` Content-Disposition
// parameter. The stem is the sanitised title when it's
// pure ASCII, otherwise it falls back to the session id
// (always ASCII). The parameter must contain no
// non-ASCII bytes — some HTTP stacks (older curl, some
// embedded clients) mangle anything outside ASCII in
// the plain parameter.
func asciiFilename(conv *memory.Conversation, ext string) string {
	ts := time.Now().Format("20060102-150405")
	stem := asciiStem(conv)
	return fmt.Sprintf("pchat-%s-%s.%s", stem, ts, ext)
}

// unicodeFilename produces the human-readable filename
// (session title + timestamp) for the
// `filename*=UTF-8''...` parameter. The title is
// sanitised (filesystem-unsafe characters replaced)
// and capped at 60 chars so a pathological 5KB title
// doesn't blow up the header. The whole filename is
// percent-encoded by the caller before it lands in
// the wire header, so any UTF-8 character is safe to
// emit here.
func unicodeFilename(conv *memory.Conversation, ext string) string {
	ts := time.Now().Format("20060102-150405")
	stem := conv.Title
	if stem == "" {
		stem = conv.ID
	}
	if len(stem) > 60 {
		stem = stem[:60]
	}
	safe := export.SanitizeFilename(stem)
	return fmt.Sprintf("pchat-%s-%s.%s", safe, ts, ext)
}

// asciiStem is the stem used for the plain
// `filename="..."` parameter. It returns the
// sanitised title when every byte is printable ASCII
// (i.e. the user can actually see it in their
// download dialog), otherwise the session id (also
// ASCII, always unique).
func asciiStem(conv *memory.Conversation) string {
	title := conv.Title
	if title == "" {
		return conv.ID
	}
	if len(title) > 60 {
		title = title[:60]
	}
	safe := export.SanitizeFilename(title)
	if isAllASCII(safe) && safe != "" {
		return safe
	}
	return conv.ID
}

// isAllASCII reports whether s contains only printable
// ASCII (no high bytes, no control characters).
// Anything outside 0x20-0x7e is rejected, including the
// common UTF-8 sequences for Chinese / emoji / etc.
func isAllASCII(s string) bool {
	for _, c := range s {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// Ensure filepath is referenced (so goimports doesn't
// strip it on a future pass). The package is only used
// implicitly via the filename's extension extraction in
// some callers; the explicit reference here keeps the
// import set stable.
var _ = filepath.Ext
