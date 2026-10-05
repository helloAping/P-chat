package cli

import (
	"fmt"
	"strings"
	"time"
)

// toolResult holds the full output of one tool call plus metadata.
// The REPL keeps a small ring buffer of recent tool results so the
// user can /expand them to see what was returned (instead of the
// truncated one-line summary shown in the live stream).
type toolResult struct {
	seq       int
	tool      string
	toolID    string
	sessionID string
	args      string
	result    string
	err       string
	duration  time.Duration
	at        time.Time
	truncated bool
	fullLen   int
}

// toolResultCache holds the most recent N tool results in insertion
// order. Lookups go by 1-based sequence number (`/expand 1` is the
// oldest, `/expand N` the most recent, or `/expand last`).
type toolResultCache struct {
	max     int
	results []*toolResult
	nextSeq int
}

func newToolResultCache(max int) *toolResultCache {
	if max <= 0 {
		max = 20
	}
	return &toolResultCache{max: max}
}

func (c *toolResultCache) record(tool, toolID, sessionID, args, result, errStr string, dur time.Duration, truncated bool, fullLen int) {
	c.nextSeq++
	r := &toolResult{
		seq:       c.nextSeq,
		tool:      tool,
		toolID:    toolID,
		sessionID: sessionID,
		args:      args,
		result:    result,
		err:       errStr,
		duration:  dur,
		at:        time.Now(),
		truncated: truncated,
		fullLen:   fullLen,
	}
	c.results = append(c.results, r)
	if len(c.results) > c.max {
		// Drop oldest.
		c.results = c.results[len(c.results)-c.max:]
	}
}

// replaceResult fills a cached truncated result after it is loaded on demand.
func (c *toolResultCache) replaceResult(r *toolResult, result string) {
	if r == nil {
		return
	}
	r.result = result
	r.truncated = false
}

// get returns the result with the displayed sequence number.
// Eviction does not renumber entries, so a positional lookup would
// expand the wrong result after the cache reaches its limit.
func (c *toolResultCache) get(seq int) *toolResult {
	for _, result := range c.results {
		if result.seq == seq {
			return result
		}
	}
	return nil
}

// last returns the most recent result, or nil.
func (c *toolResultCache) last() *toolResult {
	if len(c.results) == 0 {
		return nil
	}
	return c.results[len(c.results)-1]
}

// list returns a short summary of all stored results, oldest first.
func (c *toolResultCache) list() []toolResult {
	out := make([]toolResult, len(c.results))
	for i, r := range c.results {
		out[i] = *r
	}
	return out
}

func (c *toolResultCache) len() int { return len(c.results) }

// formatOneLine produces a one-line summary of a tool result for
// listing purposes. Long lines are truncated with "…".
func formatOneLine(s string, max int) string {
	if max <= 0 {
		max = 80
	}
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

// describe produces a human-readable summary of a tool call result.
// Truncates long content but preserves the size in bytes.
func describe(r *toolResult) string {
	if r == nil {
		return "(not found)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  #%d  %s", r.seq, r.tool)
	if r.args != "" {
		fmt.Fprintf(&b, "  %s", formatOneLine(r.args, 60))
	}
	resultBytes := len(r.result)
	if r.truncated && r.fullLen > 0 {
		resultBytes = r.fullLen
	}
	fmt.Fprintf(&b, "  (%s, %d bytes result", r.duration.Round(time.Millisecond), resultBytes)
	if r.err != "" {
		fmt.Fprintf(&b, ", err=%s", formatOneLine(r.err, 60))
	}
	b.WriteString(")")
	return b.String()
}
