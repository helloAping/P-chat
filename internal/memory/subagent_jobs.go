package memory

import (
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"
)

const (
	// SubagentJobQueued means the job has been persisted but not started.
	SubagentJobQueued = "queued"
	// SubagentJobRunning means the background subagent is actively running.
	SubagentJobRunning = "running"
	// SubagentJobSucceeded means the subagent completed with a result.
	SubagentJobSucceeded = "succeeded"
	// SubagentJobFailed means the subagent ended with an error.
	SubagentJobFailed = "failed"
	// SubagentJobCancelled means the user cancelled the background job.
	SubagentJobCancelled = "cancelled"
)

// SubagentJob is the durable state for an asynchronous subagent task.
type SubagentJob struct {
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	SessionID    string    `json:"session_id"`
	Status       string    `json:"status"`
	SubagentType string    `json:"subagent_type,omitempty"`
	Model        string    `json:"model,omitempty"`
	Description  string    `json:"description,omitempty"`
	Result       string    `json:"result,omitempty"`
	Error        string    `json:"error,omitempty"`
	ProgressJSON string    `json:"progress_json,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	FinishedAt   time.Time `json:"finished_at,omitempty"`
	CancelledAt  time.Time `json:"cancelled_at,omitempty"`
}

var subagentJobCounter atomic.Int64

// CreateSubagentJob inserts a new asynchronous subagent job.
func (s *Store) CreateSubagentJob(job SubagentJob) (SubagentJob, error) {
	if job.SessionID == "" {
		return SubagentJob{}, fmt.Errorf("session id is required")
	}
	if job.TaskID == "" {
		job.TaskID = newSubagentJobID()
	}
	if job.ID == "" {
		job.ID = newSubagentJobID()
	}
	if job.Status == "" {
		job.Status = SubagentJobQueued
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}

	_, err := s.db.Exec(
		`INSERT INTO subagent_jobs(
			id, task_id, session_id, status, subagent_type, model, description,
			result, error, progress_json, created_at, started_at, finished_at, cancelled_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.TaskID, job.SessionID, job.Status, job.SubagentType, job.Model, job.Description,
		job.Result, job.Error, job.ProgressJSON, job.CreatedAt.Unix(),
		nullableUnix(job.StartedAt), nullableUnix(job.FinishedAt), nullableUnix(job.CancelledAt),
	)
	if err != nil {
		return SubagentJob{}, err
	}
	return job, nil
}

// GetSubagentJob returns one job by the user-visible task id within a session.
func (s *Store) GetSubagentJob(sessionID, taskID string) (SubagentJob, bool, error) {
	if sessionID == "" || taskID == "" {
		return SubagentJob{}, false, nil
	}
	return s.scanSubagentJobRow(s.db.QueryRow(
		`SELECT id, task_id, session_id, status, subagent_type, model, description,
		        result, error, progress_json, created_at, started_at, finished_at, cancelled_at
		   FROM subagent_jobs WHERE session_id = ? AND task_id = ?`,
		sessionID, taskID,
	))
}

// GetSubagentJobByID returns one job by its internal immutable id.
func (s *Store) GetSubagentJobByID(id string) (SubagentJob, bool, error) {
	if id == "" {
		return SubagentJob{}, false, nil
	}
	return s.scanSubagentJobRow(s.db.QueryRow(
		`SELECT id, task_id, session_id, status, subagent_type, model, description,
		        result, error, progress_json, created_at, started_at, finished_at, cancelled_at
		   FROM subagent_jobs WHERE id = ?`,
		id,
	))
}

// ListSubagentJobs returns recent jobs for one session, newest first.
func (s *Store) ListSubagentJobs(sessionID string, limit int) ([]SubagentJob, error) {
	if sessionID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, task_id, session_id, status, subagent_type, model, description,
		        result, error, progress_json, created_at, started_at, finished_at, cancelled_at
		   FROM subagent_jobs
		  WHERE session_id = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		sessionID, limitOrHuge(limit),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SubagentJob
	for rows.Next() {
		job, err := scanSubagentJob(rows)
		if err != nil {
			return out, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// MarkSubagentJobRunning records that a background subagent started.
func (s *Store) MarkSubagentJobRunning(id string) error {
	if id == "" {
		return fmt.Errorf("job id is required")
	}
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE subagent_jobs
		    SET status = ?, started_at = COALESCE(started_at, ?)
		  WHERE id = ?`,
		SubagentJobRunning, now, id,
	)
	return err
}

// UpdateSubagentJobProgress stores the latest progress snapshot JSON.
func (s *Store) UpdateSubagentJobProgress(id, progressJSON string) error {
	if id == "" {
		return fmt.Errorf("job id is required")
	}
	_, err := s.db.Exec(`UPDATE subagent_jobs SET progress_json = ? WHERE id = ?`, progressJSON, id)
	return err
}

// CompleteSubagentJob stores the final successful result.
func (s *Store) CompleteSubagentJob(id, result string) error {
	if id == "" {
		return fmt.Errorf("job id is required")
	}
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE subagent_jobs
		    SET status = ?, result = ?, error = '', finished_at = ?
		  WHERE id = ?`,
		SubagentJobSucceeded, result, now, id,
	)
	return err
}

// FailSubagentJob stores the final failure.
func (s *Store) FailSubagentJob(id, message string) error {
	if id == "" {
		return fmt.Errorf("job id is required")
	}
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE subagent_jobs
		    SET status = ?, error = ?, finished_at = ?
		  WHERE id = ?`,
		SubagentJobFailed, message, now, id,
	)
	return err
}

// CancelSubagentJob marks a job as cancelled.
func (s *Store) CancelSubagentJob(id string) error {
	if id == "" {
		return fmt.Errorf("job id is required")
	}
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE subagent_jobs
		    SET status = ?, cancelled_at = ?, finished_at = COALESCE(finished_at, ?)
		  WHERE id = ?`,
		SubagentJobCancelled, now, now, id,
	)
	return err
}

func (s *Store) scanSubagentJobRow(row *sql.Row) (SubagentJob, bool, error) {
	job, err := scanSubagentJob(row)
	if err == sql.ErrNoRows {
		return SubagentJob{}, false, nil
	}
	if err != nil {
		return SubagentJob{}, false, err
	}
	return job, true, nil
}

type subagentJobScanner interface {
	Scan(dest ...any) error
}

func scanSubagentJob(scanner subagentJobScanner) (SubagentJob, error) {
	var (
		job                             SubagentJob
		createdAt                       int64
		startedAt, finishedAt, canceled sql.NullInt64
	)
	if err := scanner.Scan(
		&job.ID, &job.TaskID, &job.SessionID, &job.Status, &job.SubagentType, &job.Model,
		&job.Description, &job.Result, &job.Error, &job.ProgressJSON, &createdAt,
		&startedAt, &finishedAt, &canceled,
	); err != nil {
		return SubagentJob{}, err
	}
	job.CreatedAt = time.Unix(createdAt, 0)
	job.StartedAt = timeFromNullableUnix(startedAt)
	job.FinishedAt = timeFromNullableUnix(finishedAt)
	job.CancelledAt = timeFromNullableUnix(canceled)
	return job, nil
}

func nullableUnix(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

func timeFromNullableUnix(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0)
}

func newSubagentJobID() string {
	return fmt.Sprintf("sajob_%d_%d", time.Now().UnixNano(), subagentJobCounter.Add(1))
}
