package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanZipPathRejectsUnsafeNames(t *testing.T) {
	bad := []string{
		"../pchat.exe",
		"web/../../pchat.exe",
		"/tmp/pchat.exe",
		`C:\Temp\pchat.exe`,
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := cleanZipPath(name); err == nil {
				t.Fatal("expected unsafe zip entry error")
			}
		})
	}
}

func TestRunAppliesZipAndBacksUpExistingFiles(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "P-Chat")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "pchat.exe"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "pchat-update-windows-amd64-v1.0.13.zip")
	writeZip(t, zipPath, map[string]string{
		"pchat.exe":      "new",
		"web/index.html": "<html></html>",
	})

	if err := run(options{
		installDir:  installDir,
		packagePath: zipPath,
		waitTimeout: timeZero(),
	}, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(installDir, "pchat.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("pchat.exe = %q, want new", got)
	}
	backups, err := filepath.Glob(filepath.Join(root, "backups", "*", "pchat.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, want 1", len(backups))
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "old" {
		t.Fatalf("backup = %q, want old", backup)
	}
}

func TestRunRejectsPackageHashMismatch(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "P-Chat")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "pchat-update-windows-amd64-v1.0.13.zip")
	writeZip(t, zipPath, map[string]string{"pchat.exe": "new"})

	bad := sha256Hex([]byte("different"))
	if err := run(options{
		installDir:     installDir,
		packagePath:    zipPath,
		expectedSHA256: bad,
		waitTimeout:    timeZero(),
	}, log.New(io.Discard, "", 0)); err == nil {
		t.Fatal("expected sha mismatch error")
	}
}

func TestUpdateFileModeForOS_MarksUnixPayloadExecutables(t *testing.T) {
	cases := []struct {
		name string
		goos string
		rel  string
	}{
		{name: "linux-gui", goos: "linux", rel: "pchat-gui"},
		{name: "linux-server", goos: "linux", rel: "pchat-server"},
		{name: "linux-cli", goos: "linux", rel: "pchat"},
		{name: "linux-updater", goos: "linux", rel: "pchat-updater"},
		{name: "linux-script", goos: "linux", rel: "uninstall.sh"},
		{name: "mac-main-binary", goos: "darwin", rel: "Contents/MacOS/pchat-gui"},
		{name: "mac-resource-server", goos: "darwin", rel: "Contents/Resources/pchat-server"},
		{name: "mac-resource-updater-backslash", goos: "darwin", rel: `Contents\Resources\pchat-updater`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := updateFileModeForOS(tc.rel, 0o644, tc.goos)
			if got.Perm() != 0o755 {
				t.Fatalf("mode = %v, want 0755", got.Perm())
			}
		})
	}
}

func TestUpdateFileModeForOS_LeavesAssetsAndWindowsModesAlone(t *testing.T) {
	if got := updateFileModeForOS("web/index.html", 0o644, "linux"); got.Perm() != 0o644 {
		t.Fatalf("linux asset mode = %v, want 0644", got.Perm())
	}
	if got := updateFileModeForOS("pchat.exe", 0o644, "windows"); got.Perm() != 0o644 {
		t.Fatalf("windows mode = %v, want 0644", got.Perm())
	}
}

func writeZip(t *testing.T, filePath string, files map[string]string) {
	t.Helper()
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func timeZero() time.Duration { return 0 }
