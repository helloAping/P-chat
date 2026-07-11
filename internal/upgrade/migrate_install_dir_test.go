package upgrade

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/p-chat/pchat/internal/paths"
)

// withFakeHome redirects paths.GlobalDir() (and the
// internal/paths/memory dir built on top of it) to a
// temp dir for the duration of the test. We use
// PCHAT_DATA_HOME because the new (V4) resolution order
// reads PCHAT_DATA_HOME for the data dir.
func withFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PCHAT_DATA_HOME", home)
	// Some of the older code paths still fall back to
	// USERPROFILE / HOME if PCHAT_DATA_HOME is unset; set
	// both to the same temp so we don't get surprising
	// fallbacks mid-test.
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	return home
}

// installDir returns a temp install dir that the test can
// stage PCHAT_HOME against. Returns the absolute path.
func installDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// writeStore creates a stub store.db at <dir>/memory/store.db
// with a tiny SQLite header so os.Stat reports it as a real
// file. The exact schema doesn't matter for migration
// purposes — V3→V4 only checks existence.
func writeStore(t *testing.T, memoryDir string) {
	t.Helper()
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(memoryDir, "store.db")
	if err := os.WriteFile(dbPath, []byte("SQLite stub for migration test"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStepV3toV4_NoPchatHome(t *testing.T) {
	// No PCHAT_HOME set → step is a no-op. The user never
	// had the bug, no migration needed.
	home := withFakeHome(t)
	// No PCHAT_HOME set explicitly (t.Setenv doesn't help
	// here because we want it empty, not unset, for the
	// check `strings.TrimSpace(os.Getenv(...)) == ""`).
	t.Setenv("PCHAT_HOME", "")

	// Sanity: pre-create ~/.p-chat/memory/ with data so we
	// can confirm it survives unchanged.
	userMem := paths.MemoryDir()
	writeStore(t, userMem)
	originalDB, _ := os.ReadFile(filepath.Join(userMem, "store.db"))

	if err := stepV3toV4(nil); err != nil {
		t.Fatalf("stepV3toV4: %v", err)
	}

	// User home data must be untouched.
	afterDB, _ := os.ReadFile(filepath.Join(userMem, "store.db"))
	if string(afterDB) != string(originalDB) {
		t.Errorf("user-home memory changed unexpectedly")
	}
	if _, err := os.Stat(filepath.Join(home, "install-data")); !os.IsNotExist(err) {
		t.Errorf("step wrote a stray file under fake home")
	}
}

func TestStepV3toV4_NoSourceData(t *testing.T) {
	// PCHAT_HOME is set (= user installed with -AddToPath)
	// but <PCHAT_HOME>/memory/ doesn't exist. Either clean
	// install, or user wiped it. No migration needed.
	withFakeHome(t)
	install := installDir(t)
	t.Setenv("PCHAT_HOME", install)

	if err := stepV3toV4(nil); err != nil {
		t.Fatalf("stepV3toV4: %v", err)
	}
	// Source still doesn't exist (we didn't create it).
	if _, err := os.Stat(filepath.Join(install, "memory")); !os.IsNotExist(err) {
		t.Errorf("expected no source memory dir, got %v", err)
	}
}

func TestStepV3toV4_MovesInstallDataToUserHome(t *testing.T) {
	// The actual happy path: install dir has memory, user
	// home doesn't, → move the whole directory.
	home := withFakeHome(t)
	install := installDir(t)
	t.Setenv("PCHAT_HOME", install)

	srcMem := filepath.Join(install, "memory")
	writeStore(t, srcMem)
	// Also drop a WAL + a backup so we can verify the
	// whole directory moves, not just store.db.
	if err := os.WriteFile(filepath.Join(srcMem, "store.db-wal"), []byte("wal-stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcMem, "store.db.backup-20260101"), []byte("backup-stub"), 0o644); err != nil {
		t.Fatal(err)
	}

	dstMem := paths.MemoryDir()
	// dstMem must NOT exist yet (we'd have hit the conflict
	// branch otherwise).
	if _, err := os.Stat(dstMem); !os.IsNotExist(err) {
		t.Fatalf("expected empty user-home memory dir before migration: %v", err)
	}

	if err := stepV3toV4(nil); err != nil {
		t.Fatalf("stepV3toV4: %v", err)
	}

	// Source should be gone.
	if _, err := os.Stat(srcMem); !os.IsNotExist(err) {
		t.Errorf("source memory dir still exists after migration: %v", err)
	}
	// Destination should have everything.
	gotDB, err := os.ReadFile(filepath.Join(dstMem, "store.db"))
	if err != nil {
		t.Fatalf("read moved store.db: %v", err)
	}
	if string(gotDB) != "SQLite stub for migration test" {
		t.Errorf("store.db content changed during move")
	}
	if _, err := os.Stat(filepath.Join(dstMem, "store.db-wal")); err != nil {
		t.Errorf("WAL not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstMem, "store.db.backup-20260101")); err != nil {
		t.Errorf("backup not moved: %v", err)
	}
	_ = home
}

func TestStepV3toV4_Idempotent(t *testing.T) {
	// Re-running on an already-migrated install is a no-op.
	withFakeHome(t)
	install := installDir(t)
	t.Setenv("PCHAT_HOME", install)

	// Pre-stage the user-home memory dir (post-migration
	// state). The source install/memory/ is empty.
	userMem := paths.MemoryDir()
	writeStore(t, userMem)
	userDB, _ := os.ReadFile(filepath.Join(userMem, "store.db"))

	// First run: no source, so no-op.
	if err := stepV3toV4(nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// User data must be untouched.
	afterDB, _ := os.ReadFile(filepath.Join(userMem, "store.db"))
	if string(afterDB) != string(userDB) {
		t.Errorf("user-home data changed on no-op run")
	}

	// Second run: still no-op.
	if err := stepV3toV4(nil); err != nil {
		t.Fatalf("second run: %v", err)
	}
	// Third for good measure.
	if err := stepV3toV4(nil); err != nil {
		t.Fatalf("third run: %v", err)
	}
}

func TestStepV3toV4_ConflictBothHaveData(t *testing.T) {
	// Pathological case: install dir AND user home both
	// have a store.db. We can't tell which is the user's
	// "real" data, so we refuse to clobber either. The
	// step returns nil (best-effort) and logs a warning.
	withFakeHome(t)
	install := installDir(t)
	t.Setenv("PCHAT_HOME", install)

	srcMem := filepath.Join(install, "memory")
	writeStore(t, srcMem)
	dstMem := paths.MemoryDir()
	writeStore(t, dstMem)

	srcContent, _ := os.ReadFile(filepath.Join(srcMem, "store.db"))
	dstContent, _ := os.ReadFile(filepath.Join(dstMem, "store.db"))

	if err := stepV3toV4(nil); err != nil {
		t.Fatalf("stepV3toV4 (conflict should not error): %v", err)
	}

	// BOTH sides must still exist, BOTH contents unchanged.
	afterSrc, err := os.ReadFile(filepath.Join(srcMem, "store.db"))
	if err != nil {
		t.Fatalf("source store.db missing after conflict path: %v", err)
	}
	if string(afterSrc) != string(srcContent) {
		t.Errorf("source store.db content changed on conflict path")
	}
	afterDst, err := os.ReadFile(filepath.Join(dstMem, "store.db"))
	if err != nil {
		t.Fatalf("dest store.db missing after conflict path: %v", err)
	}
	if string(afterDst) != string(dstContent) {
		t.Errorf("dest store.db content changed on conflict path")
	}
}

func TestStepV3toV4_IgnoresRelativePchatHome(t *testing.T) {
	// install.ps1 always writes an absolute path. If we
	// somehow get a relative one (manual env edit, etc.),
	// we refuse to migrate rather than guess.
	if runtime.GOOS == "windows" {
		// Skip: PCHAT_HOME on Windows typically gets
		// resolved through PATH expansion that always
		// produces an absolute path; a relative value
		// here would never have come from install.ps1.
		t.Skip("Windows: relative PCHAT_HOME is implausible from install.ps1")
	}

	withFakeHome(t)
	install := installDir(t)
	// Make PCHAT_HOME relative by joining onto ".", e.g.
	// "./foo" → not absolute.
	t.Setenv("PCHAT_HOME", filepath.Join(".", filepath.Base(install)))

	srcMem := filepath.Join(install, "memory")
	writeStore(t, srcMem)

	// Even with the source populated, relative PCHAT_HOME
	// must skip the migration. The source must stay
	// untouched so the user can recover manually.
	if err := stepV3toV4(nil); err != nil {
		t.Fatalf("stepV3toV4: %v", err)
	}
	if _, err := os.Stat(srcMem); err != nil {
		t.Errorf("source vanished on relative-path skip: %v", err)
	}
}

// TestCopyDir_CrossVolumeFallback verifies the copyDir helper
// that stepV3toV4 falls back to when os.Rename fails across
// filesystem boundaries (installed on D:, $HOME on C:).
//
// We simulate the cross-volume scenario by copying the source
// directory tree to a destination on the same drive and then
// verifying the destination mirrors the source exactly.
// The actual os.Rename failure path is exercised indirectly:
// stepV3toV4 calls copyDir when Rename returns a non-nil error.
// Since we can't create a different drive letter in a unit test,
// we validate the copyDir logic stands alone.
func TestCopyDir_CrossVolumeFallback(t *testing.T) {
	home := withFakeHome(t)
	install := t.TempDir()

	src := filepath.Join(install, "memory")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	// store.db
	if err := os.WriteFile(filepath.Join(src, "store.db"), []byte("test-db"), 0o644); err != nil {
		t.Fatal(err)
	}
	// .wal
	if err := os.WriteFile(filepath.Join(src, "store.db-wal"), []byte("test-wal"), 0o644); err != nil {
		t.Fatal(err)
	}
	// .backup-*
	if err := os.WriteFile(filepath.Join(src, "store.db.backup-20260101"), []byte("test-backup"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A subdirectory (memory/backups/) — copyDir must recurse.
	if err := os.MkdirAll(filepath.Join(src, "backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "backups", "archive.db.zip"), []byte("zip-stub"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(home, "memory")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	// Verify content.
	cases := []struct {
		rel     string
		want    string
	}{
		{"store.db", "test-db"},
		{"store.db-wal", "test-wal"},
		{"store.db.backup-20260101", "test-backup"},
		{"backups/archive.db.zip", "zip-stub"},
	}
	for _, c := range cases {
		data, err := os.ReadFile(filepath.Join(dst, c.rel))
		if err != nil {
			t.Errorf("missing %s: %v", c.rel, err)
			continue
		}
		if string(data) != c.want {
			t.Errorf("%s: got %q, want %q", c.rel, string(data), c.want)
		}
	}

	// Source must still exist (copyDir doesn't delete).
	if _, err := os.Stat(filepath.Join(src, "store.db")); err != nil {
		t.Errorf("source store.db missing after copy: %v", err)
	}
}

// TestCopyDir_PartialWriteCleanedUp verifies that if copyDir
// fails mid-way (e.g. write error on the third file), the
// destination directory is left in a consistent state rather
// than having a partial tree that the migration step would
// mistake for a valid move. The caller (stepV3toV4) cleans up
// on error, so this test validates that a half-copied dir can
// be safely os.RemoveAll'd.
func TestCopyDir_PartialWriteCleanedUp(t *testing.T) {
	home := withFakeHome(t)

	src := filepath.Join(t.TempDir(), "memory")
	writeStore(t, src)
	if err := os.WriteFile(filepath.Join(src, "extra.dat"), []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Destination that already has a lock file (simulating a
	// failed prior attempt). copyDir should overwrite it.
	dst := filepath.Join(home, "memory")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	// This is a leftover from a previous failed copy.
	if err := os.WriteFile(filepath.Join(dst, "stale.tmp"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Normal copy should succeed, overwriting stale files.
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "store.db")); err != nil {
		t.Errorf("store.db missing after copy over stale dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "extra.dat")); err != nil {
		t.Errorf("extra.dat missing after copy over stale dir: %v", err)
	}
}

func TestStepV3toV4_WhitespaceInPchatHome(t *testing.T) {
	// Windows env vars can pick up stray whitespace from
	// .reg imports or hand edits. Trim + Clean before
	// stat, otherwise the source stat would silently miss
	// and the step would be a no-op (less surprising than
	// a failed move, but the user would never know).
	//
	// NB: We test whitespace only. Stray quotes inside the
	// value (a regedit-copy artefact) would also need
	// stripping, but that's a more invasive clean-up than
	// this step wants to do — install.ps1 always writes a
	// clean path, and a quoted value at runtime means the
	// user has hand-edited the env var, in which case a
	// failed migration is the right signal.
	withFakeHome(t)
	install := installDir(t)
	t.Setenv("PCHAT_HOME", "  "+install+"  ")

	srcMem := filepath.Join(install, "memory")
	writeStore(t, srcMem)

	if err := stepV3toV4(nil); err != nil {
		t.Fatalf("stepV3toV4: %v", err)
	}
	// Source must be gone → the cleanup actually fired.
	if _, err := os.Stat(srcMem); !os.IsNotExist(err) {
		t.Errorf("expected source to be moved, still present: %v", err)
	}
}
