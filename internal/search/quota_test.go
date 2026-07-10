package search

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ====================================================================
// QuotaTracker basic semantics
// ====================================================================

func TestQuotaTracker_Unlimited(t *testing.T) {
	q := NewQuotaTracker(0)
	for i := 0; i < 100; i++ {
		ok, _ := q.CheckAndIncrement()
		if !ok {
			t.Fatalf("iteration %d should be allowed (limit=0)", i)
		}
	}
	used, limit, allowed := q.Peek()
	if limit != 0 {
		t.Errorf("Peek limit = %d, want 0", limit)
	}
	if used != 100 {
		t.Errorf("Peek used = %d, want 100", used)
	}
	if !allowed {
		t.Error("Peek should report allowed when limit=0")
	}
}

func TestQuotaTracker_BlocksAtLimit(t *testing.T) {
	q := NewQuotaTracker(3)
	// 3 calls allowed
	for i := 0; i < 3; i++ {
		ok, remaining := q.CheckAndIncrement()
		if !ok {
			t.Fatalf("call %d should be allowed", i)
		}
		if remaining != 2-i {
			t.Errorf("call %d remaining = %d, want %d", i, remaining, 2-i)
		}
	}
	// 4th call denied
	ok, _ := q.CheckAndIncrement()
	if ok {
		t.Error("4th call should be denied")
	}
	used, limit, allowed := q.Peek()
	if used != 3 || limit != 3 || allowed {
		t.Errorf("Peek = (used=%d, limit=%d, allowed=%v), want (3, 3, false)", used, limit, allowed)
	}
}

func TestQuotaTracker_SetLimit(t *testing.T) {
	q := NewQuotaTracker(0)
	// Used 5 calls (unlimited)
	for i := 0; i < 5; i++ {
		_, _ = q.CheckAndIncrement()
	}
	// Now cap at 3 — 6th call should be denied
	q.SetLimit(3)
	ok, _ := q.CheckAndIncrement()
	if ok {
		t.Error("after SetLimit(3) with 5 used, 6th call should be denied")
	}
	// Raise to 10 — 7th call should be allowed
	q.SetLimit(10)
	ok, _ = q.CheckAndIncrement()
	if !ok {
		t.Error("after SetLimit(10), 7th call should be allowed (used becomes 6)")
	}
}

func TestQuotaTracker_SetLimitNegativeClampsToZero(t *testing.T) {
	q := NewQuotaTracker(0)
	q.SetLimit(-5)
	if q.Limit() != 0 {
		t.Errorf("SetLimit(-5) should clamp to 0, got %d", q.Limit())
	}
}

func TestQuotaTracker_DayRollover(t *testing.T) {
	q := &QuotaTracker{dateFn: func() time.Time { return time.Date(2025, 1, 1, 23, 59, 0, 0, time.UTC) }}
	q.limit.Store(2)
	q.day = "2025-01-01"
	q.used = 2 // already at the cap

	// Roll forward to 2025-01-02 — counter resets
	q.dateFn = func() time.Time { return time.Date(2025, 1, 2, 0, 0, 1, 0, time.UTC) }
	ok, _ := q.CheckAndIncrement()
	if !ok {
		t.Error("after day rollover, first call should be allowed")
	}
	if q.Used() != 1 {
		t.Errorf("Used = %d, want 1 after rollover", q.Used())
	}
}

func TestQuotaTracker_ResetsAt(t *testing.T) {
	q := &QuotaTracker{dateFn: func() time.Time {
		return time.Date(2025, 6, 15, 14, 30, 0, 0, time.UTC)
	}}
	q.limit.Store(10)
	resets := q.ResetsAt()
	want := time.Date(2025, 6, 16, 0, 0, 0, 0, time.UTC)
	if !resets.Equal(want) {
		t.Errorf("ResetsAt = %v, want %v", resets, want)
	}
}

func TestQuotaTracker_Concurrent(t *testing.T) {
	q := NewQuotaTracker(100)
	var wg sync.WaitGroup
	var allowed atomic.Int32
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := q.CheckAndIncrement(); ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	// Exactly 100 should have been allowed (the limit).
	if got := allowed.Load(); got != 100 {
		t.Errorf("allowed = %d, want exactly 100 (the cap)", got)
	}
}

// ====================================================================
// Global quota (process-wide)
// ====================================================================

func TestGlobalQuota_DefaultIsUnlimited(t *testing.T) {
	SetQuotaLimit(0)
	if Quota().Limit() != 0 {
		t.Errorf("default quota limit should be 0 (unlimited), got %d", Quota().Limit())
	}
}

func TestSetQuotaLimit_UpdatesInPlace(t *testing.T) {
	SetQuotaLimit(50)
	defer SetQuotaLimit(0)
	if Quota().Limit() != 50 {
		t.Errorf("SetQuotaLimit(50) didn't take effect: %d", Quota().Limit())
	}
	// Use some quota.
	for i := 0; i < 5; i++ {
		_, _ = Quota().CheckAndIncrement()
	}
	// Raising the cap should preserve the in-flight used count.
	SetQuotaLimit(100)
	if Quota().Used() != 5 {
		t.Errorf("SetQuotaLimit(100) lost the used count: %d", Quota().Used())
	}
}
