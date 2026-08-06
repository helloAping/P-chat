package agent

// toolResultCache.go — bounded in-memory store for tool results that
// were truncated before reaching the frontend. When a tool result
// exceeds MaxToolResultFullBytes, the SSE event carries only the
// display preview; the full body is parked here so the frontend can
// fetch it on demand (GET /sessions/:id/messages/:msg_id/tool-result/:tool_id).
//
// Bounded: entries are capped by count and total bytes, oldest-first
// eviction. A single long turn can produce several large results;
// without a cap the cache itself would become the memory leak we are
// trying to fix.

import (
	"sync"
	"time"
)

// truncatedResult is one parked tool result.
type truncatedResult struct {
	content   string
	storedAt  time.Time
	sessionID string
}

// toolResultCache is a small LRU keyed by tool_id. Safe for
// concurrent use. Evicts oldest entries when over cap.
type toolResultCache struct {
	mu      sync.Mutex
	entries map[string]truncatedResult
	// maxEntries bounds the entry count; maxBytes bounds the total
	// retained content across all entries.
	maxEntries int
	maxBytes   int
	curBytes   int
}

// maxCachedToolResults / maxCachedToolResultBytes bound the
// truncation cache. 64 entries × up to ~1 MiB each keeps the
// worst-case footprint under ~64 MiB while covering a long
// aicoding session's worth of large outputs.
const (
	maxCachedToolResults     = 64
	maxCachedToolResultBytes = 64 << 20
	// toolResultCacheTTL is how long a parked result stays fetchable.
	// The frontend fetches on demand shortly after the tool event;
	// anything older than this is stale.
	toolResultCacheTTL = 30 * time.Minute
)

var truncatedCache = &toolResultCache{
	entries:    make(map[string]truncatedResult),
	maxEntries: maxCachedToolResults,
	maxBytes:   maxCachedToolResultBytes,
}

// storeTruncatedResult parks a full tool result under its tool_id.
// Evicts oldest entries when the cache exceeds its bounds. Omitted
// (no-op) when content is empty.
func storeTruncatedResult(sessionID, toolID, content string) {
	if toolID == "" || content == "" {
		return
	}
	truncatedCache.mu.Lock()
	defer truncatedCache.mu.Unlock()
	truncatedCache.entries[toolID] = truncatedResult{
		content:   content,
		storedAt:  time.Now(),
		sessionID: sessionID,
	}
	truncatedCache.curBytes += len(content)
	truncatedCache.evictLocked()
}

// LookupTruncatedResult returns the full tool result for toolID if
// present and fresh. The sessionID check keeps a tool_id from one
// conversation from leaking into another.
func LookupTruncatedResult(sessionID, toolID string) (string, bool) {
	truncatedCache.mu.Lock()
	defer truncatedCache.mu.Unlock()
	e, ok := truncatedCache.entries[toolID]
	if !ok {
		return "", false
	}
	if e.sessionID != sessionID {
		return "", false
	}
	if time.Since(e.storedAt) > toolResultCacheTTL {
		delete(truncatedCache.entries, toolID)
		truncatedCache.curBytes -= len(e.content)
		return "", false
	}
	return e.content, true
}

// rekeyTruncatedResult reassigns an existing cache entry to a
// different session id. Used when a truncated result was parked
// under a sub-agent's internal session id and the parent agent's
// forwarder wants the frontend (which only knows the parent
// session id) to be able to fetch it. No-op when the entry is
// missing or expired.
func rekeyTruncatedResult(sessionID, toolID string) {
	truncatedCache.mu.Lock()
	defer truncatedCache.mu.Unlock()
	e, ok := truncatedCache.entries[toolID]
	if !ok {
		return
	}
	if time.Since(e.storedAt) > toolResultCacheTTL {
		delete(truncatedCache.entries, toolID)
		truncatedCache.curBytes -= len(e.content)
		return
	}
	if e.sessionID == sessionID {
		return
	}
	e.sessionID = sessionID
	truncatedCache.entries[toolID] = e
}

// evictLocked drops oldest entries until the cache fits within
// maxEntries / maxBytes. Caller must hold mu.
func (c *toolResultCache) evictLocked() {
	for len(c.entries) > c.maxEntries || c.curBytes > c.maxBytes {
		var oldestKey string
		var oldestAt time.Time
		first := true
		for k, e := range c.entries {
			if first || e.storedAt.Before(oldestAt) {
				oldestKey = k
				oldestAt = e.storedAt
				first = false
			}
		}
		if oldestKey == "" {
			return
		}
		c.curBytes -= len(c.entries[oldestKey].content)
		delete(c.entries, oldestKey)
	}
}
