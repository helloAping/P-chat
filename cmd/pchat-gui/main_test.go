package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFindServerBinary_FindsSibling(t *testing.T) {
	dir := t.TempDir()
	// Create a fake pchat-server.exe beside a fake "self" path.
	// findServerBinary() uses os.Executable() so we can only test the
	// "PATH" branch deterministically; the sibling branch is exercised
	// by the smoke test that actually launches the real binary.
	fake := filepath.Join(dir, "pchat-server.exe")
	if err := os.WriteFile(fake, []byte("MZ"), 0644); err != nil {
		t.Fatal(err)
	}
	orig := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+orig)

	got, err := findServerBinary()
	if err != nil {
		t.Fatalf("findServerBinary: %v", err)
	}
	if !strings.EqualFold(filepath.Base(got), "pchat-server.exe") {
		t.Fatalf("expected pchat-server.exe, got %q", got)
	}
}

func TestFindServerBinary_NotFound(t *testing.T) {
	// findServerBinary walks CWD up to 5 levels looking for
	// bin/pchat-server.exe (dev-mode fallback). If the test binary
	// runs from inside the project tree, the walk will find it and
	// defeat the "not found" probe. Chdir to a temp dir so the walk
	// has no chance of finding a matching file.
	cwd, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(cwd)

	t.Setenv("PATH", t.TempDir())
	if _, err := findServerBinary(); err == nil {
		t.Fatal("expected error when pchat-server.exe is not in PATH or beside the binary")
	}
}

func TestPickPreferredPort_FirstPreferredWhenAvailable(t *testing.T) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPortStart))
	if err != nil {
		t.Skipf("preferred port %d is already occupied: %v", preferredPortStart, err)
	}
	_ = l.Close()

	port, err := pickPreferredPort()
	if err != nil {
		t.Fatal(err)
	}
	if port != preferredPortStart {
		t.Fatalf("pickPreferredPort() = %d, want %d", port, preferredPortStart)
	}
}

func TestPickPreferredPort_SkipsOccupiedPreferredPorts(t *testing.T) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPortStart))
	if err != nil {
		t.Skipf("preferred port %d is already occupied: %v", preferredPortStart, err)
	}
	defer l.Close()

	port, err := pickPreferredPort()
	if err != nil {
		t.Fatal(err)
	}
	if port == preferredPortStart {
		t.Fatalf("pickPreferredPort() reused occupied port %d", preferredPortStart)
	}
	if port < preferredPortStart || port > preferredPortEnd {
		t.Fatalf("pickPreferredPort() = %d, want next port in %d-%d", port, preferredPortStart+1, preferredPortEnd)
	}
}

func TestPickFreePort_Valid(t *testing.T) {
	p1, err := pickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	p2, err := pickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	if p1 == 0 || p2 == 0 {
		t.Fatalf("ports should be nonzero, got %d and %d", p1, p2)
	}
	// The OS may reuse the same port if the previous one was closed,
	// so we only assert both are valid.
}

func TestPickConfigPath_PrefersJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	// Neither file: should return "" (fresh install — pchat-server
	// uses built-in defaults; see pickConfigPath godoc).
	if p := pickConfigPath(); p != "" {
		t.Fatalf("expected empty string for fresh install, got %q", p)
	}

	// Both files: prefer json.
	if err := os.MkdirAll(filepath.Join(home, ".p-chat"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".p-chat", "config.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".p-chat", "config.yaml"), []byte("a: b"), 0644); err != nil {
		t.Fatal(err)
	}
	if p := pickConfigPath(); !strings.HasSuffix(p, "config.json") {
		t.Fatalf("expected config.json, got %q", p)
	}

	// Only yaml: should fall back to yaml.
	os.Remove(filepath.Join(home, ".p-chat", "config.json"))
	if p := pickConfigPath(); !strings.HasSuffix(p, "config.yaml") {
		t.Fatalf("expected config.yaml fallback, got %q", p)
	}
}

func TestNormalizeCloseBehavior(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", closeBehaviorExit},
		{closeBehaviorExit, closeBehaviorExit},
		{closeBehaviorTray, closeBehaviorTray},
		{"bogus", closeBehaviorExit},
	}
	for _, tc := range cases {
		if got := normalizeCloseBehavior(tc.in); got != tc.want {
			t.Fatalf("normalizeCloseBehavior(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShouldPreventClose(t *testing.T) {
	if !shouldPreventClose(false, closeBehaviorTray) {
		t.Fatal("tray close behavior should prevent window close")
	}
	if shouldPreventClose(false, closeBehaviorExit) {
		t.Fatal("exit close behavior should not prevent window close")
	}
	if shouldPreventClose(true, closeBehaviorTray) {
		t.Fatal("quitting should allow the application to close")
	}
}

func TestReadCloseBehavior_JSONAndYAML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".p-chat")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	jsonPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(jsonPath, []byte(`{"ui":{"close_behavior":"tray"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readCloseBehavior(); got != closeBehaviorTray {
		t.Fatalf("JSON close behavior = %q, want tray", got)
	}

	if err := os.Remove(jsonPath); err != nil {
		t.Fatal(err)
	}
	yaml := []byte("ui:\n  close_behavior: \"tray\" # keep alive\n")
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), yaml, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readCloseBehavior(); got != closeBehaviorTray {
		t.Fatalf("YAML close behavior = %q, want tray", got)
	}
}

func TestStopServer_NoProcessIsIdempotent(t *testing.T) {
	app := NewApp()
	app.stopServer()
	app.stopServer()

	app.serverCmd = &exec.Cmd{}
	app.stopServer()
	app.stopServer()
}

func TestRecentTraySessions_LimitsAndSkipsEmptyIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sessions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[
			{"id":"","title":"skip"},
			{"id":"s1","title":"one","project_path":"D:/one"},
			{"id":"s2","title":"two"},
			{"id":"s3","title":"three"},
			{"id":"s4","title":"four"},
			{"id":"s5","title":"five"},
			{"id":"s6","title":"six"}
		]}`))
	}))
	defer srv.Close()

	app := NewApp()
	backend := srv.URL
	app.backendURL.Store(&backend)

	got := app.recentTraySessions()
	if len(got) != trayRecentSessionLimit {
		t.Fatalf("recentTraySessions len = %d, want %d", len(got), trayRecentSessionLimit)
	}
	if got[0].ID != "s1" || got[0].ProjectPath != "D:/one" {
		t.Fatalf("first session = %+v, want s1 with project path", got[0])
	}
	if got[len(got)-1].ID != "s5" {
		t.Fatalf("last capped session = %q, want s5", got[len(got)-1].ID)
	}
}

func TestTraySessionMenuLabel_TruncatesAndEscapesAmpersand(t *testing.T) {
	long := traySession{ID: "s1", Title: strings.Repeat("会", traySessionTitleLimit+3)}
	got := traySessionMenuLabel(1, long)
	if !strings.HasPrefix(got, "2. ") {
		t.Fatalf("label prefix = %q, want numbered prefix", got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("label should contain ellipsis, got %q", got)
	}

	withAmpersand := traySession{ID: "s2", Title: "A & B"}
	got = traySessionMenuLabel(0, withAmpersand)
	if !strings.Contains(got, "A && B") {
		t.Fatalf("label should escape ampersand for Win32 menus, got %q", got)
	}
}

func TestAcquireSingleInstance_DetectsExistingInstance(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("single-instance mutex is currently Windows-only")
	}
	t.Setenv("PCHAT_SINGLE_INSTANCE_MUTEX", fmt.Sprintf(`Local\PChatGuiTest-%d`, time.Now().UnixNano()))

	first, already, err := acquireSingleInstance()
	if err != nil {
		t.Fatalf("first acquireSingleInstance: %v", err)
	}
	defer first.release()
	if already {
		t.Fatal("first acquire should not report an existing instance")
	}

	second, already, err := acquireSingleInstance()
	if err != nil {
		t.Fatalf("second acquireSingleInstance: %v", err)
	}
	if second != nil {
		defer second.release()
	}
	if !already {
		t.Fatal("second acquire should report an existing instance")
	}
}
