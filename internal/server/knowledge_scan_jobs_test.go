package server

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestScanJobSnapshotTracksProgress(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	job := newScanJob("counting", cancel)
	job.startRunning(3)
	job.setCurrent(2)
	stats := indexScanStats{Changed: 1, Skipped: 2, Deleted: 3, Failed: 4, L3: 5}
	job.setStats(stats)
	job.finish("ok: done", 3, stats)

	snap := job.snapshot()
	if snap.Status != "ok: done" || snap.Current != 3 || snap.Total != 3 || snap.Chunks != 5 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap.Changed != 1 || snap.Skipped != 2 || snap.Deleted != 3 || snap.Failed != 4 {
		t.Fatalf("unexpected stats snapshot: %+v", snap)
	}
}

func TestScanJobSnapshotIsSafeUnderConcurrentUpdates(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	job := newScanJob("counting", cancel)
	job.startRunning(100)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				job.setCurrent(offset + n)
				job.setStats(indexScanStats{Changed: n, Skipped: n + 1, Deleted: n + 2, Failed: n + 3})
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				_ = job.snapshot()
			}
		}()
	}
	wg.Wait()
}

func TestScanJobManagerLoadStoreDelete(t *testing.T) {
	mgr := newScanJobManager()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	job := newScanJob("counting", cancel)
	if stale, ok := mgr.Claim("kb", job, time.Now(), 30*time.Minute); !ok || stale != nil {
		t.Fatalf("Claim() = (%v, %v), want initial claim", stale, ok)
	}
	got, ok := mgr.Load("kb")
	if !ok || got != job {
		t.Fatalf("Load() = (%v, %v), want stored job", got, ok)
	}
	mgr.Delete("kb")
	if _, ok := mgr.Load("kb"); ok {
		t.Fatal("expected job to be deleted")
	}
}

func TestScanJobManagerClaimRejectsActiveJob(t *testing.T) {
	mgr := newScanJobManager()
	_, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	_, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	first := newScanJob("running", cancel1)
	if _, ok := mgr.Claim("kb", first, time.Now(), 30*time.Minute); !ok {
		t.Fatal("expected first claim to succeed")
	}
	second := newScanJob("counting", cancel2)
	if stale, ok := mgr.Claim("kb", second, time.Now(), 30*time.Minute); ok || stale != nil {
		t.Fatalf("Claim() = (%v, %v), want active job rejection", stale, ok)
	}
	got, ok := mgr.Load("kb")
	if !ok || got != first {
		t.Fatalf("active job should remain registered, got (%v, %v)", got, ok)
	}
}

func TestScanJobManagerClaimReplacesStaleJob(t *testing.T) {
	mgr := newScanJobManager()
	_, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	_, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	first := newScanJob("running", cancel1)
	first.mu.Lock()
	first.startedAt = time.Now().Add(-time.Hour)
	first.mu.Unlock()
	if _, ok := mgr.Claim("kb", first, time.Now().Add(-time.Hour), 30*time.Minute); !ok {
		t.Fatal("expected first claim to succeed")
	}

	second := newScanJob("counting", cancel2)
	stale, ok := mgr.Claim("kb", second, time.Now(), 30*time.Minute)
	if !ok || stale != first {
		t.Fatalf("Claim() = (%v, %v), want stale replacement", stale, ok)
	}
	got, ok := mgr.Load("kb")
	if !ok || got != second {
		t.Fatalf("new job should be registered, got (%v, %v)", got, ok)
	}
}

func TestIsScanJobTerminal(t *testing.T) {
	for _, status := range []string{"ok: indexed", "error: failed"} {
		if !isScanJobTerminal(status) {
			t.Fatalf("%q should be terminal", status)
		}
	}
	if isScanJobTerminal("running") {
		t.Fatal("running should not be terminal")
	}
}
