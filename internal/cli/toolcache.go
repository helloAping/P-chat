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
	seq      int
	tool     string
	args     string
	result   string
	err      string
	duration time.Duration
	at       time.Time
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

func (c *toolResultCache) record(tool, args, result, errStr string, dur time.Duration) {
	c.nextSeq++
	r := &toolResult{
		seq:      c.nextSeq,
		tool:     tool,
		args:     args,
		result:   result,
		err:      errStr,
		duration: dur,
		at:       time.Now(),
	}
	c.results = append(c.results, r)
	if len(c.results) > c.max {
		// Drop oldest.
		c.results = c.results[len(c.results)-c.max:]
	}
}

// get returns the result with the given 1-based index from the
// beginning. Returns nil if not found.
func (c *toolResultCache) get(idx int) *toolResult {
	if idx < 1 || idx > len(c.results) {
		return nil
	}
	return c.results[idx-1]
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
	fmt.Fprintf(&b, "  (%s, %d bytes result", r.duration.Round(time.Millisecond), len(r.result))
	if r.err != "" {
		fmt.Fprintf(&b, ", err=%s", formatOneLine(r.err, 60))
	}
	b.WriteString(")")
	return b.String()
}
