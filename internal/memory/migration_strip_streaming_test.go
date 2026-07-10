package memory

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var timeNow = time.Now

// TestMigration_StripStreamingFlag verifies migration v7 —
// it removes the `streaming` key from every persisted parts
// blob. The key was a live-UI signal that should never have
// been persisted in the first place; pre-fix rows had it
// baked into meta["parts"] and would render the "思考中…"
// spinner on a thought the user already saw the LLM finish.
//
// `json_remove` is the right tool for the migration: it
// strips the key by JSON path regardless of the surrounding
// document shape, and `json_extract` returning non-null is
// the natural "key exists" guard. We exercise three shapes:
//   - "streaming":true in the middle of a parts object
//   - "streaming":false (the now-also-stale value)
//   - the streaming key at the TOP of the metadata
//     envelope (outside the parts blob, in case any
//     future row wrote it that way)
//
// The migration only operates on the top-level keys of
// the metadata JSON. A `streaming` key inside a JSON
// string value (e.g. the inner part of a `parts` blob
// when `parts` is a string) is a different concern and
// is the next-write's problem: `snapshotStructural` no
// longer sets `Streaming` on any part, so the inner
// string never has it post-migration.
func TestMigration_StripStreamingFlag(t *testing.T) {
	fixtures := []struct {
		name    string
		content string
		meta    string
	}{
		{
			name:    "middle",
			content: "r1",
			meta:    `{"parts":"[{\"kind\":\"thinking\",\"text\":\"r1\"}]","thinking":"r1","streaming":true}`,
		},
		{
			name:    "end",
			content: "r2",
			meta:    `{"parts":"[{\"kind\":\"thinking\",\"text\":\"r2\"}]","streaming":false}`,
		},
		{
			name:    "top-level-only",
			content: "r3",
			meta:    `{"streaming":true,"parts":"[{\"kind\":\"thinking\",\"text\":\"r3\"}]"}`,
		},
	}

	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenAt(filepath.Join(dir, "test.db"), 50)
			if err != nil {
				t.Fatalf("OpenAt: %v", err)
			}
			defer s.Close()

			conv, err := s.NewConversation()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetCurrent(conv); err != nil {
				t.Fatal(err)
			}

			// Plant a "v6-era" row: delete the v7 entry
			// (and any later version) from the migrations
			// table to simulate the pre-upgrade state,
			// then insert a row with `streaming` baked
			// into the metadata. Newer migrations (e.g.
			// v8 add_message_seq) must be removed from
			// the table too — the migration runner skips
			// any version <= MAX(version) and would
			// otherwise leave v7 un-applied because v8
			// is recorded as the latest. The actual v8
			// column on the messages table can stay (its
			// migration is the schema-side effect we don't
			// need to undo for this test) — the
			// plant-then-migrateTo(7) shape is the
			// canonical "test a specific historical
			// migration" pattern.
			if _, err := s.db.Exec(
				`DELETE FROM schema_migrations WHERE version >= 7`,
			); err != nil {
				t.Fatalf("downgrade schema_migrations: %v", err)
			}
			now := timeNow().Unix()
			if _, err := s.db.Exec(
				`INSERT INTO messages(conversation_id, role, content, created_at, metadata, msg_type, submit_to_llm)
				 VALUES (?, 'assistant', ?, ?, ?, 0, 1)`,
				conv, f.content, now, f.meta,
			); err != nil {
				t.Fatalf("plant row: %v", err)
			}

			// Pre-check: the planted row has streaming.
			pre := readAllMeta(t, s)
			if !strings.Contains(pre[0], `"streaming"`) {
				t.Fatalf("pre-migration: planted row should contain streaming, got %s", pre[0])
			}

			// Re-run only v7. migrateTo(7) is bounded
			// by targetVersion so the runner won't try
			// to re-apply v8 against a DB that already
			// has the seq column.
			if err := s.migrateTo(7); err != nil {
				t.Fatalf("migrateTo(7): %v", err)
			}

			post := readAllMeta(t, s)
			if len(post) != 1 {
				t.Fatalf("want 1 row post-migration, got %d", len(post))
			}
			if strings.Contains(post[0], `"streaming"`) {
				t.Errorf("post-migration: streaming still in metadata: %s", post[0])
			}
			if !strings.Contains(post[0], `"parts"`) {
				t.Errorf("post-migration: lost parts key: %s", post[0])
			}
		})
	}
}

// readAllMeta returns the raw metadata string for every
// assistant message in the store, in ASC id order.
func readAllMeta(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(
		`SELECT metadata FROM messages WHERE role = 'assistant' ORDER BY id`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}
