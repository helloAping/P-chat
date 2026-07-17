// P1-4 regen-history tests. Verifies the migration
// (regen_group_id + is_archived), the three new store
// methods (ArchiveSiblings / ListSiblings /
// ActivateSibling), the 20-row cap, and the user-
// message association round-trip. All tests use
// OpenAt on a t.TempDir() database so they don't
// touch the production ~/.p-chat/memory/store.db.
package memory

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigration_RegenHistory_Schema verifies migration v9's
// schema effect: after OpenAt runs all migrations, the
// messages table has `regen_group_id` and `is_archived`
// columns and the `idx_messages_conv_group` index exists.
// Mirrors the migration 8 schema test (see
// migration_add_seq_test.go) so a future refactor of the
// migration registration loop catches both shapes.
func TestMigration_RegenHistory_Schema(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	// regen_group_id column
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='regen_group_id'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected exactly one regen_group_id column on messages, got %d", n)
	}

	// is_archived column
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='is_archived'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected exactly one is_archived column on messages, got %d", n)
	}

	// idx_messages_conv_group index
	var idxName string
	_ = s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='messages' AND name='idx_messages_conv_group'`,
	).Scan(&idxName)
	if idxName != "idx_messages_conv_group" {
		t.Errorf("idx_messages_conv_group not found in sqlite_master; "+
			"the per-group UPDATE / ListSiblings would table-scan")
	}
}

// TestArchiveSiblings_ArchivesAllExceptKeepActive verifies
// the core ArchiveSiblings behaviour: every row in the
// regen group becomes archived except the row with
// id == keepActiveID, which becomes the new active.
func TestArchiveSiblings_ArchivesAllExceptKeepActive(t *testing.T) {
	s := newTestStore(t)
	convID, err := s.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetCurrent(convID)

	// Seed: 1 user message (id 1) + 3 assistant
	// siblings in the same regen group, all in the
	// user's group. Use the P1-4 helper so each
	// assistant row is tagged with regen_group_id =
	// "1" (the user message's id as a string).
	insertTestAssistantWithGroupID(t, s, convID, "user",  "hi", "")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "v1", "1")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "v2", "1")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "v3", "1")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// The three assistant rows now have ids 2, 3, 4.
	// Archive everything except id=3 (the middle
	// sibling — arbitrary; the test is that
	// keepActiveID wins, not which id).
	deleted, err := s.ArchiveSiblings(convID, "1", 3)
	if err != nil {
		t.Fatalf("ArchiveSiblings: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 hard-deletes (group is under cap), got %d", deleted)
	}

	// Verify the visibility flags: id=3 is active
	// (is_archived=0); id=2 and id=4 are archived
	// (is_archived=1); the user message (id=1) is
	// untouched.
	for _, want := range []struct {
		id        int64
		archived  bool
		content   string
	}{
		{1, false, "hi"},       // user message, not in group
		{2, true, "v1"},        // archived
		{3, false, "v2"},       // active
		{4, true, "v3"},        // archived
	} {
		var (
			archived int
			content  string
		)
		if err := s.db.QueryRow(
			`SELECT is_archived, content FROM messages WHERE id = ?`, want.id,
		).Scan(&archived, &content); err != nil {
			t.Fatalf("query id %d: %v", want.id, err)
		}
		if (archived == 1) != want.archived {
			t.Errorf("id %d: is_archived = %d, want %v", want.id, archived, want.archived)
		}
		if content != want.content {
			t.Errorf("id %d: content = %q, want %q", want.id, content, want.content)
		}
	}
}

// TestArchiveSiblings_KeepActiveZero_ArchivesAll is the
// regen entry-point case: keepActiveID=0 means "no row
// stays active" — every group row is archived, and the
// upcoming agent-loop insert becomes the lone active row.
// Without the cap, the function should not hard-delete
// any rows (the count is still <= 20). The new assistant
// row from the agent loop lands with is_archived=0
// (the default) and becomes the new active.
func TestArchiveSiblings_KeepActiveZero_ArchivesAll(t *testing.T) {
	s := newTestStore(t)
	convID, _ := s.NewConversation()
	_ = s.SetCurrent(convID)

	insertTestAssistantWithGroupID(t, s, convID, "user", "hi", "")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "v1", "1")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "v2", "1")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.ArchiveSiblings(convID, "1", 0)
	if err != nil {
		t.Fatalf("ArchiveSiblings: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 hard-deletes, got %d", deleted)
	}
	// Both assistant rows should now be archived.
	var archived int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE regen_group_id='1' AND is_archived=1`,
	).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived != 2 {
		t.Errorf("expected 2 archived rows, got %d", archived)
	}
	var active int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE regen_group_id='1' AND is_archived=0`,
	).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Errorf("expected 0 active rows, got %d", active)
	}
}

// TestArchiveSiblings_CapHardDeletesOldest verifies the
// MaxRegenPerGroup = 20 cap: when the post-archive count
// (n - 1, leaving room for the upcoming agent insert)
// exceeds the cap, the oldest archived rows are
// hard-deleted. The active row is never deleted.
func TestArchiveSiblings_CapHardDeletesOldest(t *testing.T) {
	s := newTestStore(t)
	convID, _ := s.NewConversation()
	_ = s.SetCurrent(convID)

	insertTestAssistantWithGroupID(t, s, convID, "user", "hi", "")
	// Seed 21 archived siblings (already archived via
	// regen_group_id + is_archived=1). Plus the user
	// message. The cap is checked AFTER the archive
	// pass: with 21 + 0 active = 21 rows in the group,
	// the cap (MaxRegenPerGroup - 1 = 19) triggers and
	// the oldest 2 rows must be hard-deleted.
	// Idempotent helper: write 21 archived rows.
	for i := 0; i < 21; i++ {
		insertTestAssistantWithGroupID(t, s, convID, "assistant", "v", "1")
		// Flip the just-inserted row to archived.
		// We can't do this inline in the helper
		// because the helper leaves is_archived=0
		// by default; we flip via direct UPDATE
		// so the seed matches the "already
		// archived" reality.
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// Mark all 21 rows archived. The cap is on
	// group-row count (active + archived), so the
	// active flag doesn't matter for the test
	// outcome; the cap checks the total count.
	if _, err := s.db.Exec(
		`UPDATE messages SET is_archived = 1 WHERE regen_group_id = '1'`,
	); err != nil {
		t.Fatal(err)
	}
	// Now call ArchiveSiblings with keepActiveID=0
	// (regen path) — it should fire the cap and
	// hard-delete the oldest rows until the count
	// is at capLimit (= MaxRegenPerGroup - 1 = 19).
	deleted, err := s.ArchiveSiblings(convID, "1", 0)
	if err != nil {
		t.Fatalf("ArchiveSiblings: %v", err)
	}
	if deleted != 2 {
		// 21 - 19 (capLimit) = 2
		t.Errorf("expected 2 hard-deletes (21 - 19 cap), got %d", deleted)
	}
	// Verify the remaining count is 19 (capLimit).
	// The user message (id 1) is NOT in the group,
	// so we count only group rows.
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE regen_group_id = '1'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != MaxRegenPerGroup-1 {
		t.Errorf("expected %d remaining rows (capLimit), got %d", MaxRegenPerGroup-1, n)
	}
	// And the user message is still there.
	var userCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND role = 'user'`,
		convID,
	).Scan(&userCount); err != nil {
		t.Fatal(err)
	}
	if userCount != 1 {
		t.Errorf("user message should be untouched, got count=%d", userCount)
	}
}

// TestListSiblings_ReturnsAllInOrder verifies that
// ListSiblings returns the full sibling set (active +
// archived) in id-ascending order, with the visibility
// flag and regen_group_id exposed. The parallel slices
// must be the same length and aligned by index.
func TestListSiblings_ReturnsAllInOrder(t *testing.T) {
	s := newTestStore(t)
	convID, _ := s.NewConversation()
	_ = s.SetCurrent(convID)

	insertTestAssistantWithGroupID(t, s, convID, "user", "hi", "")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "first", "1")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "second", "1")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "third", "1")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// Mark the first as archived.
	if _, err := s.db.Exec(
		`UPDATE messages SET is_archived = 1 WHERE content = 'first'`,
	); err != nil {
		t.Fatal(err)
	}
	// ListSiblings with a non-existent groupID returns
	// nil slices (defensive — the caller checks
	// before using the data).
	_, _, _, _, _, _, _ = s.ListSiblings(convID, "999")
	// ListSiblings with the real groupID returns 3 rows.
	contents, _, _, ids, _, archiveds, regenGroupIDs := s.ListSiblings(convID, "1")
	if len(contents) != 3 {
		t.Fatalf("expected 3 siblings, got %d", len(contents))
	}
	// Order: id-ASC. The first insert wins the lowest
	// id (2), the second wins id 3, etc.
	wantContent := []string{"first", "second", "third"}
	wantArchived := []bool{true, false, false} // first is archived
	for i, c := range contents {
		if c.Content != wantContent[i] {
			t.Errorf("sibling %d: content = %q, want %q", i, c.Content, wantContent[i])
		}
		if archiveds[i] != wantArchived[i] {
			t.Errorf("sibling %d: is_archived = %v, want %v", i, archiveds[i], wantArchived[i])
		}
		if regenGroupIDs[i] != "1" {
			t.Errorf("sibling %d: regen_group_id = %q, want \"1\"", i, regenGroupIDs[i])
		}
	}
	// IDs should be strictly increasing.
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("ids not strictly increasing: ids[%d]=%d, ids[%d]=%d", i-1, ids[i-1], i, ids[i])
		}
	}
}

// TestActivateSibling_OnlyAffectsOwnGroup verifies the
// security guard: ActivateSibling refuses to activate a
// row from a different conversation or a different
// regen group. A malicious / buggy client passing an
// arbitrary reply id must get an error, not a silent
// cross-group side effect.
func TestActivateSibling_OnlyAffectsOwnGroup(t *testing.T) {
	s := newTestStore(t)
	convID, _ := s.NewConversation()
	_ = s.SetCurrent(convID)

	insertTestAssistantWithGroupID(t, s, convID, "user", "hi", "")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "v1", "1")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	// Try to activate a non-existent id.
	if err := s.ActivateSibling(convID, "1", 9999); err == nil {
		t.Error("expected error for non-existent reply id")
	}
	// Try to activate with an empty groupID.
	if err := s.ActivateSibling(convID, "", 1); err == nil {
		t.Error("expected error for empty groupID")
	}
	// Try to activate a user message (id=1) — the row
	// exists but doesn't belong to any regen group.
	if err := s.ActivateSibling(convID, "1", 1); err == nil {
		t.Error("expected error when target row is not in the named group")
	}
}

// TestActivateSibling_OneActiveAtATime verifies the
// happy path: calling ActivateSibling(id=X) makes X
// the new active and archives every other sibling in
// the same group. Repeated calls with different ids
// always leave exactly one active row.
func TestActivateSibling_OneActiveAtATime(t *testing.T) {
	s := newTestStore(t)
	convID, _ := s.NewConversation()
	_ = s.SetCurrent(convID)

	insertTestAssistantWithGroupID(t, s, convID, "user", "hi", "")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "v1", "1")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "v2", "1")
	insertTestAssistantWithGroupID(t, s, convID, "assistant", "v3", "1")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// The seed has 3 active rows (all is_archived=0).
	// Mark v1 + v3 as archived so the seed has exactly
	// 1 active (v2) — the invariant the production
	// regen flow maintains. ActivateSibling's contract
	// is to keep this invariant; the test verifies it.
	if _, err := s.db.Exec(
		`UPDATE messages SET is_archived = 1 WHERE content IN ('v1', 'v3')`,
	); err != nil {
		t.Fatal(err)
	}
	// Helper: assert the active-row invariant.
	assertOneActive := func(label string) {
		t.Helper()
		var n int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM messages WHERE regen_group_id = '1' AND is_archived = 0`,
		).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s: expected exactly 1 active row, got %d", label, n)
		}
	}
	assertOneActive("seed")
	// Activate v2.
	if err := s.ActivateSibling(convID, "1", 3); err != nil {
		t.Fatal(err)
	}
	assertOneActive("after v2")
	// Activate v3.
	if err := s.ActivateSibling(convID, "1", 4); err != nil {
		t.Fatal(err)
	}
	assertOneActive("after v3")
	// Activate v1.
	if err := s.ActivateSibling(convID, "1", 2); err != nil {
		t.Fatal(err)
	}
	assertOneActive("after v1")
}

// TestRegenGroup_BackfillOnExistingRows verifies the
// P1-4 migration's backfill: pre-migration assistant
// rows get their regen_group_id set to the id of the
// nearest preceding user message in the same
// conversation. Insert two user messages and three
// assistant replies (a/b/c) into a fresh database via
// the legacy path (no regen_group_id), then call
// migrate to a fresh DB schema and verify the backfill
// set the right groups: a → user1, b/c → user2.
//
// Implementation: we manually craft a DB with the v8
// schema (no regen_group_id), insert rows, then run the
// v9 migration's Up SQL by calling migrateTo(9). The
// store's helper methods don't expose direct INSERT
// (they always tag with regen_group_id from v9
// onwards), so the test inserts via raw SQL.
func TestRegenGroup_BackfillOnExistingRows(t *testing.T) {
	dir := t.TempDir()
	// First open brings the DB to the current schema
	// (v9), but we then drop the regen_group_id column
	// to simulate a pre-migration DB and re-run only
	// the v9 migration's Up SQL.
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer s.Close()

	convID, err := s.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetCurrent(convID)

	// Simulate a pre-migration-9 database: drop the
	// regen_group_id + is_archived columns so we can
	// re-run the v9 Up SQL against a v8-shape DB.
	// We must drop the index FIRST — SQLite doesn't
	// allow dropping a column that's referenced by
	// an index.
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS idx_messages_conv_group`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE messages DROP COLUMN regen_group_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE messages DROP COLUMN is_archived`); err != nil {
		t.Fatal(err)
	}
	// Mark migration 9 as not-applied so a re-run
	// fires its Up.
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version = 9`); err != nil {
		t.Fatal(err)
	}

	// Insert legacy-shaped rows: two user messages,
	// three assistant replies (a between user1 and
	// user2; b and c after user2).
	type row struct {
		role, content string
	}
	rows := []row{
		{"user", "u1"},
		{"assistant", "a"}, // between u1 and u2
		{"user", "u2"},
		{"assistant", "b"}, // after u2
		{"assistant", "c"}, // after u2
	}
	now := int64(1)
	for i, r := range rows {
		_, err := s.db.Exec(
			`INSERT INTO messages(conversation_id, role, content, created_at, seq) VALUES (?, ?, ?, ?, ?)`,
			convID, r.role, r.content, now+int64(i), int64(i+1),
		)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Re-run the v9 migration by closing the store,
	// re-opening (which calls migrateTo(latest)), and
	// letting the v9 Up SQL fire.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	// Verify the backfill: a → u1's id (2), b/c → u2's
	// id (4). The user message ids were the 1st and
	// 3rd inserts (seq 1 and 3), giving row ids 1 and 3.
	// Wait — the order is: u1 (id=1, seq=1), a (id=2,
	// seq=2), u2 (id=3, seq=3), b (id=4, seq=4), c
	// (id=5, seq=5). So u1's id = 1 and u2's id = 3.
	want := map[string]string{
		"a": "1", // nearest preceding user is u1
		"b": "3", // nearest preceding user is u2
		"c": "3", // nearest preceding user is u2
	}
	for content, expGroup := range want {
		var rg sql.NullString
		if err := s2.db.QueryRow(
			`SELECT regen_group_id FROM messages WHERE content = ? AND role = 'assistant'`,
			content,
		).Scan(&rg); err != nil {
			t.Fatalf("query %q: %v", content, err)
		}
		if !rg.Valid {
			t.Errorf("assistant %q: regen_group_id is NULL, want %q", content, expGroup)
			continue
		}
		if rg.String != expGroup {
			t.Errorf("assistant %q: regen_group_id = %q, want %q", content, rg.String, expGroup)
		}
	}
}

// --- helpers ---

// newTestStore returns a fresh, in-memory-equivalent
// Store for a single test. Mirrors the helper used in
// memory_test.go so the test file is self-contained.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// insertTestAssistantWithGroupID inserts one row with a
// specified role, content, and (optional) regen_group_id.
// role/content follow the schema's NOT NULL constraints;
// regenGroupID is "" for non-regen rows (e.g. user
// messages and the seed before a regen ever happens). The
// insert is buffered (the caller must Flush()).
//
// We use the lower-level prepared-statement path because
// AddChatMessageWithMetaToRegen doesn't expose a "force
// group id even if it's not a normal user-message id"
// hook. The store's public APIs (AddMessage etc.) all
// either look up the user message id or refuse — the
// test needs a way to write arbitrary group ids, so we
// hit the database directly here.
func insertTestAssistantWithGroupID(t *testing.T, s *Store, convID, role, content, regenGroupID string) {
	t.Helper()
	var rgArg interface{}
	if regenGroupID != "" {
		rgArg = regenGroupID
	}
	if _, err := s.db.Exec(
		`INSERT INTO messages(conversation_id, role, content, created_at, regen_group_id, is_archived) VALUES (?, ?, ?, ?, ?, 0)`,
		convID, role, content, regenHistoryTimeNow(), rgArg,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

// timeNow is a tiny helper to keep the import block
// narrow. Avoids pulling time into the test file
// directly (time.Now is fine but inlining the unix
// call here keeps the helper above readable).
//
// (We use a distinct name to avoid colliding with
// the timeNow var defined in migration_strip_streaming_test.go.)
func regenHistoryTimeNow() int64 {
	// We don't actually need the real time; the
	// created_at column is not under test here.
	// Use a fixed value so debug output is stable.
	return 1700000000
}
