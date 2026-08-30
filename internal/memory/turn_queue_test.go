package memory

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestMigration_TurnQueue_Schema(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	if !hasTable(s.db, "turn_queue") {
		t.Fatal("turn_queue table missing")
	}
	assertColumnExists(t, s, "turn_queue", "session_id")
	assertColumnExists(t, s, "turn_queue", "payload_json")
	assertColumnExists(t, s, "turn_queue", "client_msg_id")
	assertColumnExists(t, s, "turn_queue", "attachment_count")
}

func TestMigration_TurnQueue_Rollback(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	if err := s.Rollback(11); err != nil {
		t.Fatalf("Rollback(11): %v", err)
	}
	if hasTable(s.db, "turn_queue") {
		t.Fatal("turn_queue table still exists after rollback")
	}
	cur, _, err := s.AppliedMigrations()
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if cur != 11 {
		t.Fatalf("current migration = %d, want 11", cur)
	}
}

func TestMigration_TurnQueue_Idempotent(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	if err := s.migrateTo(12); err != nil {
		t.Fatalf("migrateTo(12): %v", err)
	}
	if err := s.migrateTo(12); err != nil {
		t.Fatalf("migrateTo(12) second run: %v", err)
	}
	assertColumnExists(t, s, "turn_queue", "finished_at")
}

func TestTurnQueue_CRUDLifecycle(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	sessionID, err := s.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}

	first, err := s.CreateTurnQueueItem(sessionID, []byte(`{"message":"first","client_msg_id":1001}`), "first", 1001, 1)
	if err != nil {
		t.Fatalf("CreateTurnQueueItem first: %v", err)
	}
	second, err := s.CreateTurnQueueItem(sessionID, []byte(`{"message":"second","client_msg_id":1002}`), "second", 1002, 0)
	if err != nil {
		t.Fatalf("CreateTurnQueueItem second: %v", err)
	}
	if first.ID >= second.ID {
		t.Fatalf("queue ids not increasing: first=%d second=%d", first.ID, second.ID)
	}

	items, err := s.ListTurnQueueItems(sessionID)
	if err != nil {
		t.Fatalf("ListTurnQueueItems: %v", err)
	}
	if len(items) != 2 || items[0].Message != "first" || items[1].Message != "second" {
		t.Fatalf("unexpected FIFO list: %+v", items)
	}

	claimed, found, err := s.ClaimNextTurnQueueItem(sessionID)
	if err != nil {
		t.Fatalf("ClaimNextTurnQueueItem: %v", err)
	}
	if !found {
		t.Fatal("expected a claimable item")
	}
	if claimed.ID != first.ID || claimed.Status != TurnQueueStatusRunning {
		t.Fatalf("unexpected claimed item: %+v", claimed)
	}
	if claimed.StartedAt.IsZero() {
		t.Fatalf("claimed item missing StartedAt: %+v", claimed)
	}

	done, found, err := s.CompleteTurnQueueItem(sessionID, claimed.ID)
	if err != nil {
		t.Fatalf("CompleteTurnQueueItem: %v", err)
	}
	if !found || done.Status != TurnQueueStatusDone || done.FinishedAt.IsZero() {
		t.Fatalf("unexpected completed item: found=%v item=%+v", found, done)
	}

	claimed, found, err = s.ClaimNextTurnQueueItem(sessionID)
	if err != nil {
		t.Fatalf("ClaimNextTurnQueueItem second: %v", err)
	}
	if !found || claimed.ID != second.ID {
		t.Fatalf("expected second item next, found=%v item=%+v", found, claimed)
	}

	failed, found, err := s.FailTurnQueueItem(sessionID, claimed.ID, "network down")
	if err != nil {
		t.Fatalf("FailTurnQueueItem: %v", err)
	}
	if !found || failed.Status != TurnQueueStatusFailed || failed.Error != "network down" {
		t.Fatalf("unexpected failed item: found=%v item=%+v", found, failed)
	}

	requeued, found, err := s.RequeueFailedTurnQueueItem(sessionID, failed.ID)
	if err != nil {
		t.Fatalf("RequeueFailedTurnQueueItem: %v", err)
	}
	if !found || requeued.Status != TurnQueueStatusQueued || requeued.Error != "" || !requeued.StartedAt.IsZero() {
		t.Fatalf("unexpected requeued item: found=%v item=%+v", found, requeued)
	}

	canceled, found, err := s.CancelTurnQueueItem(sessionID, requeued.ID)
	if err != nil {
		t.Fatalf("CancelTurnQueueItem: %v", err)
	}
	if !found || canceled.Status != TurnQueueStatusCancelled {
		t.Fatalf("unexpected canceled item: found=%v item=%+v", found, canceled)
	}

	items, err = s.ListTurnQueueItems(sessionID)
	if err != nil {
		t.Fatalf("ListTurnQueueItems after cancel: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("cancelled/done items should be hidden, got %+v", items)
	}
}

func TestTurnQueue_ClearKeepsRunningItem(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	sessionID, err := s.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	first, err := s.CreateTurnQueueItem(sessionID, []byte(`{"message":"running","client_msg_id":2001}`), "running", 2001, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTurnQueueItem(sessionID, []byte(`{"message":"queued","client_msg_id":2002}`), "queued", 2002, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimNextTurnQueueItem(sessionID); err != nil {
		t.Fatalf("claim running: %v", err)
	}

	count, err := s.ClearTurnQueue(sessionID)
	if err != nil {
		t.Fatalf("ClearTurnQueue: %v", err)
	}
	if count != 1 {
		t.Fatalf("cleared count = %d, want 1", count)
	}
	items, err := s.ListTurnQueueItems(sessionID)
	if err != nil {
		t.Fatalf("ListTurnQueueItems: %v", err)
	}
	if len(items) != 1 || items[0].ID != first.ID || items[0].Status != TurnQueueStatusRunning {
		t.Fatalf("running item should remain visible for recovery, got %+v", items)
	}
}

func TestTurnQueue_FailedHeadBlocksClaim(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	sessionID, err := s.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	first, err := s.CreateTurnQueueItem(sessionID, []byte(`{"message":"first","client_msg_id":3001}`), "first", 3001, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTurnQueueItem(sessionID, []byte(`{"message":"second","client_msg_id":3002}`), "second", 3002, 0); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := s.ClaimNextTurnQueueItem(sessionID)
	if err != nil || !found || claimed.ID != first.ID {
		t.Fatalf("claim first: item=%+v found=%v err=%v", claimed, found, err)
	}
	if _, _, err := s.FailTurnQueueItem(sessionID, claimed.ID, "network down"); err != nil {
		t.Fatalf("FailTurnQueueItem: %v", err)
	}

	_, found, err = s.ClaimNextTurnQueueItem(sessionID)
	if !errors.Is(err, ErrTurnQueueBlocked) {
		t.Fatalf("claim behind failed head err = %v, want ErrTurnQueueBlocked", err)
	}
	if found {
		t.Fatal("claim behind failed head should not return an item")
	}
}

func TestTurnQueue_RunningHeadBlocksClaim(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	sessionID, err := s.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	first, err := s.CreateTurnQueueItem(sessionID, []byte(`{"message":"first","client_msg_id":4001}`), "first", 4001, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateTurnQueueItem(sessionID, []byte(`{"message":"second","client_msg_id":4002}`), "second", 4002, 0)
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := s.ClaimNextTurnQueueItem(sessionID)
	if err != nil || !found || claimed.ID != first.ID {
		t.Fatalf("claim first: item=%+v found=%v err=%v", claimed, found, err)
	}

	_, found, err = s.ClaimNextTurnQueueItem(sessionID)
	if !errors.Is(err, ErrTurnQueueRunning) {
		t.Fatalf("claim behind running head err = %v, want ErrTurnQueueRunning", err)
	}
	if found {
		t.Fatal("claim behind running head should not return an item")
	}
	items, err := s.ListTurnQueueItems(sessionID)
	if err != nil {
		t.Fatalf("ListTurnQueueItems: %v", err)
	}
	if len(items) != 2 || items[0].ID != first.ID || items[0].Status != TurnQueueStatusRunning || items[1].ID != second.ID || items[1].Status != TurnQueueStatusQueued {
		t.Fatalf("running head should preserve FIFO state, got %+v", items)
	}
}

func TestTurnQueue_CompleteAndFailRequireRunning(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	sessionID, err := s.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	item, err := s.CreateTurnQueueItem(sessionID, []byte(`{"message":"queued","client_msg_id":5001}`), "queued", 5001, 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, found, err := s.CompleteTurnQueueItem(sessionID, item.ID); err != nil || found {
		t.Fatalf("complete queued item: found=%v err=%v", found, err)
	}
	if _, found, err := s.FailTurnQueueItem(sessionID, item.ID, "stale failure"); err != nil || found {
		t.Fatalf("fail queued item: found=%v err=%v", found, err)
	}

	claimed, found, err := s.ClaimNextTurnQueueItem(sessionID)
	if err != nil || !found || claimed.ID != item.ID {
		t.Fatalf("claim after stale finish attempts: item=%+v found=%v err=%v", claimed, found, err)
	}
	if _, found, err := s.CompleteTurnQueueItem(sessionID, item.ID); err != nil || !found {
		t.Fatalf("complete running item: found=%v err=%v", found, err)
	}
	if _, found, err := s.FailTurnQueueItem(sessionID, item.ID, "late failure"); err != nil || found {
		t.Fatalf("fail completed item: found=%v err=%v", found, err)
	}
}
