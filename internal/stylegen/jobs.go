package stylegen

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// jobs.go — in-memory background JobManager for stylegen runs.
//
// Start() registers a job and runs Generate() in a background goroutine,
// broadcasting ProgressEvent to every subscriber. Subscribers are closed
// when the job finishes so an SSE stream can terminate cleanly. Finished
// jobs are pruned by a TTL sweeper so a long-running server never leaks
// job state.

// DefaultJobTTL is how long a finished job is kept before the sweeper
// removes it. Long enough for a user to reopen a finished modal and read
// the result, short enough to bound memory.
const DefaultJobTTL = 10 * time.Minute

// DefaultMaxJobs bounds the number of retained jobs; the oldest finished
// jobs are evicted first when the cap is hit.
const DefaultMaxJobs = 200

// StageInfo is one step of the progress checklist shown in the modal.
type StageInfo struct {
	Stage string `json:"stage"`
	Label string `json:"label"`
	Done  bool   `json:"done"`
}

// JobStatus is a pollable snapshot of a job (GET /stylegen/:job).
type JobStatus struct {
	ID        string      `json:"id"`
	Status    string      `json:"status"` // running | done | error
	Stages    []StageInfo `json:"stages,omitempty"`
	Result    *Result     `json:"result,omitempty"`
	Error     string      `json:"error,omitempty"`
	ErrorKind string      `json:"error_kind,omitempty"`
	CreatedAt int64       `json:"created_at"` // unix seconds
	DoneAt    int64       `json:"done_at,omitempty"`
}

type job struct {
	status  string
	stages  []StageInfo
	result  *Result
	errMsg  string
	errKind string
	subs    map[chan ProgressEvent]struct{}
	created time.Time
	doneAt  time.Time
}

// JobManager owns the in-memory job table. All methods are safe for
// concurrent use; Start() itself is the only writer besides the per-job
// runner goroutine.
type JobManager struct {
	deps Deps

	mu   sync.Mutex
	jobs map[string]*job
	next int

	ttl  time.Duration
	max  int
	stop chan struct{}
}

// NewJobManager creates a JobManager that runs Generate with the given
// deps. A sweeper goroutine prunes finished jobs after DefaultJobTTL.
func NewJobManager(deps Deps) *JobManager {
	jm := &JobManager{
		deps: deps,
		jobs: make(map[string]*job),
		ttl:  DefaultJobTTL,
		max:  DefaultMaxJobs,
		stop: make(chan struct{}),
	}
	go jm.sweepLoop()
	return jm
}

// Stop halts the sweeper goroutine. Safe to call once at shutdown.
func (jm *JobManager) Stop() {
	select {
	case <-jm.stop:
	default:
		close(jm.stop)
	}
}

// Start registers a new job and returns its id. The job runs in a
// background goroutine immediately; it does not block the caller.
func (jm *JobManager) Start(p Params) string {
	jm.mu.Lock()
	jm.next++
	id := fmt.Sprintf("sg-%d", jm.next)
	jm.jobs[id] = &job{
		status:  "running",
		stages:  nil,
		subs:    make(map[chan ProgressEvent]struct{}),
		created: time.Now(),
	}
	jm.evictLocked()
	jm.mu.Unlock()

	go jm.run(id, p)
	return id
}

// run executes Generate for a job, records the outcome and closes every
// subscriber channel so SSE streams terminate.
func (jm *JobManager) run(id string, p Params) {
	result, err := Generate(context.Background(), jm.deps, p, func(ev ProgressEvent) {
		jm.observe(id, ev)
	})

	jm.mu.Lock()
	j := jm.jobs[id]
	if j != nil {
		j.doneAt = time.Now()
		switch {
		case err != nil:
			j.status = "error"
			j.errKind = string(KindOf(err))
			if j.errKind == "" {
				j.errKind = string(KindLLM)
			}
			j.errMsg = err.Error()
		default:
			j.status = "done"
			j.result = result
		}
		for ch := range j.subs {
			close(ch)
		}
		j.subs = make(map[chan ProgressEvent]struct{})
	}
	jm.mu.Unlock()
}

// observe records a stage transition (events without content/thinking)
// and broadcasts the event to every subscriber. Content deltas are
// dropped rather than blocking the generator when a subscriber's buffer
// is full — the final result event carries the complete text.
func (jm *JobManager) observe(id string, ev ProgressEvent) {
	if ev.Stage != "" && ev.Content == "" && ev.Thinking == "" {
		jm.mu.Lock()
		if j := jm.jobs[id]; j != nil {
			found := false
			for i := range j.stages {
				if j.stages[i].Stage == ev.Stage {
					j.stages[i].Done = true
					found = true
					break
				}
			}
			if !found {
				j.stages = append(j.stages, StageInfo{Stage: ev.Stage, Label: ev.Label, Done: true})
			}
		}
		jm.mu.Unlock()
	}
	jm.broadcast(id, ev)
}

// broadcast fans an event out to every subscriber without holding the
// lock during sends.
func (jm *JobManager) broadcast(id string, ev ProgressEvent) {
	jm.mu.Lock()
	j := jm.jobs[id]
	if j == nil {
		jm.mu.Unlock()
		return
	}
	subs := make([]chan ProgressEvent, 0, len(j.subs))
	for ch := range j.subs {
		subs = append(subs, ch)
	}
	jm.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // buffer full: drop this delta, the result event has it all
		}
	}
}

// Subscribe returns a channel of ProgressEvent for a job. A finished
// job (done/error) immediately yields a terminal event and closes the
// channel; a running job yields a live channel that is closed when the
// job completes. Returns ok=false when the job does not exist.
func (jm *JobManager) Subscribe(id string) (<-chan ProgressEvent, bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	j := jm.jobs[id]
	if j == nil {
		return nil, false
	}
	ch := make(chan ProgressEvent, 16)
	switch j.status {
	case "done":
		ch <- ProgressEvent{Stage: "done", Label: "完成"}
		close(ch)
		return ch, true
	case "error":
		ch <- ProgressEvent{Stage: "error", Label: j.errMsg}
		close(ch)
		return ch, true
	default:
		j.subs[ch] = struct{}{}
		return ch, true
	}
}

// Status returns a snapshot of a job for polling, or nil if the job does
// not exist.
func (jm *JobManager) Status(id string) *JobStatus {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	j := jm.jobs[id]
	if j == nil {
		return nil
	}
	st := &JobStatus{
		ID:        id,
		Status:    j.status,
		Result:    j.result,
		Error:     j.errMsg,
		ErrorKind: j.errKind,
		CreatedAt: j.created.Unix(),
	}
	if !j.doneAt.IsZero() {
		st.DoneAt = j.doneAt.Unix()
	}
	if len(j.stages) > 0 {
		st.Stages = make([]StageInfo, len(j.stages))
		copy(st.Stages, j.stages)
	}
	return st
}

// Wait blocks until the job reaches a terminal state or the context is
// cancelled. It is the CLI http-mode polling primitive.
func (jm *JobManager) Wait(ctx context.Context, id string) *JobStatus {
	for {
		if st := jm.Status(id); st != nil && st.Status != "running" {
			return st
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// evictLocked removes finished jobs beyond the TTL / max cap. Caller
// holds jm.mu.
func (jm *JobManager) evictLocked() {
	if len(jm.jobs) <= jm.max {
		return
	}
	// Evict oldest finished jobs until under the cap.
	for len(jm.jobs) > jm.max {
		var oldest *job
		var oldestID string
		for id, j := range jm.jobs {
			if j.status == "running" {
				continue
			}
			if oldest == nil || j.doneAt.Before(oldest.doneAt) {
				oldest, oldestID = j, id
			}
		}
		if oldest == nil {
			return // all running
		}
		delete(jm.jobs, oldestID)
	}
}

// sweepLoop periodically prunes finished jobs older than the TTL.
func (jm *JobManager) sweepLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-jm.stop:
			return
		case <-ticker.C:
			jm.sweep()
		}
	}
}

func (jm *JobManager) sweep() {
	cutoff := time.Now().Add(-jm.ttl)
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for id, j := range jm.jobs {
		if j.status != "running" && j.doneAt.Before(cutoff) {
			delete(jm.jobs, id)
		}
	}
}
