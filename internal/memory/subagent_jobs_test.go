package memory

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMigration_SubagentJobs_Schema(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	if !hasTable(s.db, "subagent_jobs") {
		t.Fatal("subagent_jobs table missing")
	}
	assertColumnExists(t, s, "subagent_jobs", "task_id")
	assertColumnExists(t, s, "subagent_jobs", "session_id")
	assertColumnExists(t, s, "subagent_jobs", "progress_json")
}

func TestMigration_SubagentJobs_Rollback(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	if err := s.Rollback(10); err != nil {
		t.Fatalf("Rollback(10): %v", err)
	}
	if hasTable(s.db, "subagent_jobs") {
		t.Fatal("subagent_jobs table still exists after rollback")
	}
	cur, _, err := s.AppliedMigrations()
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if cur != 10 {
		t.Fatalf("current migration = %d, want 10", cur)
	}
}

func TestMigration_SubagentJobs_Idempotent(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	if err := s.migrateTo(11); err != nil {
		t.Fatalf("migrateTo(11): %v", err)
	}
	if err := s.migrateTo(11); err != nil {
		t.Fatalf("migrateTo(11) second run: %v", err)
	}
	assertColumnExists(t, s, "subagent_jobs", "cancelled_at")
}

func TestSubagentJobs_CRUDLifecycle(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	createdAt := time.Unix(100, 0)
	job, err := s.CreateSubagentJob(SubagentJob{
		TaskID:       "task-1",
		SessionID:    "session-1",
		SubagentType: "explore",
		Model:        "model-a",
		Description:  "inspect repository",
		CreatedAt:    createdAt,
	})
	if err != nil {
		t.Fatalf("CreateSubagentJob: %v", err)
	}
	if job.ID == "" {
		t.Fatal("job id not generated")
	}
	if job.Status != SubagentJobQueued {
		t.Fatalf("status = %q, want queued", job.Status)
	}

	got, ok, err := s.GetSubagentJob("session-1", "task-1")
	if err != nil {
		t.Fatalf("GetSubagentJob: %v", err)
	}
	if !ok {
		t.Fatal("created job not found")
	}
	if got.ID != job.ID || got.SubagentType != "explore" || !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected created job: %+v", got)
	}

	if err := s.MarkSubagentJobRunning(job.ID); err != nil {
		t.Fatalf("MarkSubagentJobRunning: %v", err)
	}
	if err := s.UpdateSubagentJobProgress(job.ID, `{"phase":"reading"}`); err != nil {
		t.Fatalf("UpdateSubagentJobProgress: %v", err)
	}
	if err := s.CompleteSubagentJob(job.ID, "done"); err != nil {
		t.Fatalf("CompleteSubagentJob: %v", err)
	}

	got, ok, err = s.GetSubagentJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetSubagentJobByID: %v", err)
	}
	if !ok {
		t.Fatal("completed job not found by id")
	}
	if got.Status != SubagentJobSucceeded || got.Result != "done" || got.ProgressJSON != `{"phase":"reading"}` {
		t.Fatalf("unexpected completed job: %+v", got)
	}
	if got.StartedAt.IsZero() || got.FinishedAt.IsZero() {
		t.Fatalf("timestamps not set after lifecycle: %+v", got)
	}

	_, err = s.CreateSubagentJob(SubagentJob{
		TaskID:    "task-2",
		SessionID: "session-1",
		Status:    SubagentJobRunning,
		CreatedAt: time.Unix(200, 0),
	})
	if err != nil {
		t.Fatalf("CreateSubagentJob second: %v", err)
	}
	list, err := s.ListSubagentJobs("session-1", 10)
	if err != nil {
		t.Fatalf("ListSubagentJobs: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("jobs length = %d, want 2", len(list))
	}
	if list[0].TaskID != "task-2" || list[1].TaskID != "task-1" {
		t.Fatalf("jobs not ordered newest first: %+v", list)
	}
}

func TestSubagentJobs_FailAndCancel(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	failed, err := s.CreateSubagentJob(SubagentJob{TaskID: "fail", SessionID: "session-1"})
	if err != nil {
		t.Fatalf("CreateSubagentJob failed: %v", err)
	}
	if err := s.FailSubagentJob(failed.ID, "boom"); err != nil {
		t.Fatalf("FailSubagentJob: %v", err)
	}
	got, ok, err := s.GetSubagentJobByID(failed.ID)
	if err != nil || !ok {
		t.Fatalf("Get failed job: ok=%v err=%v", ok, err)
	}
	if got.Status != SubagentJobFailed || got.Error != "boom" || got.FinishedAt.IsZero() {
		t.Fatalf("unexpected failed job: %+v", got)
	}

	cancelled, err := s.CreateSubagentJob(SubagentJob{TaskID: "cancel", SessionID: "session-1"})
	if err != nil {
		t.Fatalf("CreateSubagentJob cancelled: %v", err)
	}
	if err := s.CancelSubagentJob(cancelled.ID); err != nil {
		t.Fatalf("CancelSubagentJob: %v", err)
	}
	got, ok, err = s.GetSubagentJobByID(cancelled.ID)
	if err != nil || !ok {
		t.Fatalf("Get cancelled job: ok=%v err=%v", ok, err)
	}
	if got.Status != SubagentJobCancelled || got.CancelledAt.IsZero() || got.FinishedAt.IsZero() {
		t.Fatalf("unexpected cancelled job: %+v", got)
	}
}

func assertColumnExists(t *testing.T, s *Store, table, column string) {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`,
		table, column,
	).Scan(&n); err != nil {
		t.Fatalf("query column %s.%s: %v", table, column, err)
	}
	if n != 1 {
		t.Fatalf("column %s.%s count = %d, want 1", table, column, n)
	}
}
