package search

import (
	"sync"
	"sync/atomic"
	"time"
)

// QuotaTracker counts search invocations per UTC day and
// enforces a configurable daily cap.
//
// Why in-memory: the quota is informational ("don't blow
// through the free tier in one session"), not a hard
// billing boundary. Restarting the server resets the
// counter, which is acceptable behaviour — users see a
// transient "0 used" after a restart, and the cap is
// still applied for the rest of the day. We deliberately
// avoid SQLite for this so we don't need a schema
// migration step (the config is the only thing that
// changes on disk).
//
// All methods are safe for concurrent use.
type QuotaTracker struct {
	// limit is the daily cap. Atomic so we can read it
	// without holding the mutex on every quota check.
	// 0 = unlimited.
	limit atomic.Int64

	mu     sync.Mutex
	day    string // UTC date in YYYY-MM-DD
	used   int    // count for the current day
	dateFn func() time.Time // overridable for tests
}

// NewQuotaTracker creates a tracker with the given daily
// limit. Pass 0 to disable enforcement.
func NewQuotaTracker(dailyLimit int) *QuotaTracker {
	q := &QuotaTracker{
		dateFn: time.Now,
	}
	q.limit.Store(int64(dailyLimit))
	q.rolloverLocked()
	return q
}

// SetLimit updates the daily cap. Existing usage counter is
// preserved. Negative values are clamped to 0 (unlimited).
func (q *QuotaTracker) SetLimit(n int) {
	if n < 0 {
		n = 0
	}
	q.limit.Store(int64(n))
}

// Limit returns the currently configured daily cap. 0 = unlimited.
func (q *QuotaTracker) Limit() int {
	return int(q.limit.Load())
}

// Used returns the number of searches counted so far today
// (UTC). The internal counter is rolled over to today
// before the read so a stale value can never be reported.
func (q *QuotaTracker) Used() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rolloverLocked()
	return q.used
}

// ResetsAt returns the UTC time at which the current day's
// counter rolls over. The caller formats this for display;
// we hand back the raw time so the formatter can pick its
// own timezone convention.
func (q *QuotaTracker) ResetsAt() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rolloverLocked()
	// Find "tomorrow 00:00 UTC" relative to the current day.
	now := q.dateFn().UTC()
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	return tomorrow
}

// CheckAndIncrement atomically enforces the daily cap.
//
//   - If limit == 0 (unlimited), increments and returns (true, 0).
//   - If limit > 0 and used < limit, increments and returns
//     (true, limit - used - 1) so the caller can log "N
//     remaining".
//   - If limit > 0 and used >= limit, returns
//     (false, 0) WITHOUT incrementing. The caller should
//     map this to ErrQuota.
//
// Must be called exactly once per actual search request
// (NOT per LLM tool invocation that returns 0 results —
// we only charge when the upstream API is hit).
func (q *QuotaTracker) CheckAndIncrement() (ok bool, remaining int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rolloverLocked()
	limit := int(q.limit.Load())
	if limit > 0 && q.used >= limit {
		return false, 0
	}
	q.used++
	if limit > 0 {
		return true, limit - q.used
	}
	return true, 0
}

// Peek returns whether the next call would be allowed,
// without mutating state. Useful for the "X / Y used"
// status display.
func (q *QuotaTracker) Peek() (used, limit int, allowed bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rolloverLocked()
	limit = int(q.limit.Load())
	used = q.used
	return used, limit, limit == 0 || used < limit
}

// rolloverLocked advances the counter to the current UTC
// day if needed. Callers MUST hold q.mu.
//
// We compare the *date string* (YYYY-MM-DD) instead of
// timestamps so daylight-saving transitions can't make us
// under- or over-count. Two consecutive calls within the
// same UTC day are O(1) string comparisons.
func (q *QuotaTracker) rolloverLocked() {
	today := q.dateFn().UTC().Format("2006-01-02")
	if today != q.day {
		q.day = today
		q.used = 0
	}
}

// ====================================================================
// Global quota (one process-wide tracker; updated when config changes)
// ====================================================================

var globalQuota atomic.Pointer[QuotaTracker]

func init() {
	// Start unlimited; the server sets the real limit on
	// startup and again whenever config.search.daily_quota
	// changes.
	q := NewQuotaTracker(0)
	globalQuota.Store(q)
}

// SetQuotaLimit updates the process-wide daily cap. Existing
// counter state is preserved (we mutate the tracker in place
// rather than allocating a new one) so the in-flight "used
// today" count survives a settings change.
func SetQuotaLimit(limit int) {
	q := globalQuota.Load()
	if q == nil {
		q = NewQuotaTracker(0)
		globalQuota.Store(q)
	}
	q.SetLimit(limit)
}

// Quota returns the current tracker so handlers can render
// "X / Y used today" status. Never returns nil.
func Quota() *QuotaTracker {
	q := globalQuota.Load()
	if q == nil {
		q = NewQuotaTracker(0)
		globalQuota.Store(q)
	}
	return q
}
