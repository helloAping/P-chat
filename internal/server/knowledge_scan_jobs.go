package server

import (
	"context"
	"strings"
	"sync"
	"time"
)

var scanJobs = newScanJobManager()

type scanJobManager struct {
	mu   sync.Mutex
	jobs map[string]*scanJob
}

func newScanJobManager() *scanJobManager {
	return &scanJobManager{jobs: make(map[string]*scanJob)}
}

func (m *scanJobManager) Load(name string) (*scanJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[name]
	return j, ok
}

func (m *scanJobManager) Claim(name string, job *scanJob, now time.Time, staleAfter time.Duration) (*scanJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.jobs[name]; ok {
		snap := existing.snapshot()
		if !isScanJobTerminal(snap.Status) && now.Sub(snap.StartedAt) < staleAfter {
			return nil, false
		}
		delete(m.jobs, name)
		if !isScanJobTerminal(snap.Status) {
			m.jobs[name] = job
			return existing, true
		}
	}

	m.jobs[name] = job
	return nil, true
}

func (m *scanJobManager) Delete(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, name)
}

type scanJob struct {
	mu        sync.Mutex
	status    string
	startedAt time.Time
	current   int
	total     int
	chunks    int
	changed   int
	skipped   int
	deleted   int
	failed    int
	cancel    context.CancelFunc
}

type scanJobSnapshot struct {
	Status    string
	StartedAt time.Time
	Current   int
	Total     int
	Chunks    int
	Changed   int
	Skipped   int
	Deleted   int
	Failed    int
}

func newScanJob(status string, cancel context.CancelFunc) *scanJob {
	return &scanJob{
		status:    status,
		startedAt: time.Now(),
		cancel:    cancel,
	}
}

func (j *scanJob) snapshot() scanJobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return scanJobSnapshot{
		Status:    j.status,
		StartedAt: j.startedAt,
		Current:   j.current,
		Total:     j.total,
		Chunks:    j.chunks,
		Changed:   j.changed,
		Skipped:   j.skipped,
		Deleted:   j.deleted,
		Failed:    j.failed,
	}
}

func (j *scanJob) setStatus(status string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status = status
}

func (j *scanJob) startRunning(total int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status = "running"
	j.total = total
	j.current = 0
}

func (j *scanJob) setCurrent(current int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.current = current
}

func (j *scanJob) setStats(stats indexScanStats) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.changed = stats.Changed
	j.skipped = stats.Skipped
	j.deleted = stats.Deleted
	j.failed = stats.Failed
}

func (j *scanJob) finish(status string, total int, stats indexScanStats) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status = status
	j.total = total
	j.current = total
	j.chunks = stats.L3
	j.changed = stats.Changed
	j.skipped = stats.Skipped
	j.deleted = stats.Deleted
	j.failed = stats.Failed
}

func (j *scanJob) cancelJob() {
	j.mu.Lock()
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func isScanJobTerminal(status string) bool {
	return strings.HasPrefix(status, "ok: ") || strings.HasPrefix(status, "error: ")
}
