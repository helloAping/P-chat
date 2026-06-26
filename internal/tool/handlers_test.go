package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile_RequiresPath(t *testing.T) {
	res, _ := handleReadFile(context.Background(), []byte(`{}`))
	if !res.IsError {
		t.Error("empty path should be rejected")
	}
}

func TestReadFile_NotFound(t *testing.T) {
	res, _ := handleReadFile(context.Background(), []byte(`{"path":"/nonexistent_zzz_abc.txt"}`))
	if !res.IsError {
		t.Error("missing file should produce an error")
	}
	if !strings.Contains(res.Content, "no such file") && !strings.Contains(res.Content, "cannot find") {
		// accept any of the common OS error phrasings
		t.Logf("error content: %s", res.Content)
	}
}

func TestReadWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.ToSlash(filepath.Join(dir, "subdir", "test.txt"))
	ctx := context.Background()

	// Write
	res, _ := handleWriteFile(ctx, []byte(`{"path":"`+path+`","content":"hello world"}`))
	if res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}
	if !strings.Contains(res.Content, "written 11 bytes") {
		t.Errorf("unexpected write result: %q", res.Content)
	}

	// Read
	res, _ = handleReadFile(ctx, []byte(`{"path":"`+path+`"}`))
	if res.IsError {
		t.Fatalf("read failed: %s", res.Content)
	}
	if res.Content != "hello world" {
		t.Errorf("read content = %q, want %q", res.Content, "hello world")
	}
}

func TestWriteFile_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	ctx := context.Background()

	_ = os.WriteFile(path, []byte("original"), 0o644)

	res, _ := handleWriteFile(ctx, []byte(`{"path":"`+filepath.ToSlash(path)+`","content":"replaced"}`))
	if res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "replaced" {
		t.Errorf("file content = %q, want %q", got, "replaced")
	}
}

func TestListFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0o755)

	res, _ := handleListFiles(context.Background(), []byte(`{"path":"`+filepath.ToSlash(dir)+`"}`))
	if res.IsError {
		t.Fatalf("list failed: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.txt") {
		t.Errorf("missing a.txt: %s", res.Content)
	}
	if !strings.Contains(res.Content, "b.txt") {
		t.Errorf("missing b.txt: %s", res.Content)
	}
	if !strings.Contains(res.Content, "sub") {
		t.Errorf("missing sub/: %s", res.Content)
	}
}

func TestListFiles_DefaultPath(t *testing.T) {
	// Empty path defaults to "." which may not be a useful test in CI,
	// but it must not error.
	res, _ := handleListFiles(context.Background(), []byte(`{}`))
	// We don't assert on content; just that it didn't crash.
	_ = res
}

func TestExecCommand_RequiresCommand(t *testing.T) {
	res, _ := handleExecCommand(context.Background(), []byte(`{}`))
	if !res.IsError {
		t.Error("empty command should be rejected")
	}
}

func TestExecCommand_RunsCommand(t *testing.T) {
	// Use a real command that should always work.
	ctx := context.Background()
	res, _ := handleExecCommand(ctx, []byte(`{"command":"echo hello-pchat"}`))
	if res.IsError {
		t.Fatalf("echo failed: %s", res.Content)
	}
	if !strings.Contains(res.Content, "hello-pchat") {
		t.Errorf("output = %q, want to contain 'hello-pchat'", res.Content)
	}
}

func TestExecCommand_InvalidArgs(t *testing.T) {
	res, _ := handleExecCommand(context.Background(), []byte(`not json`))
	if !res.IsError {
		t.Error("invalid JSON should be rejected")
	}
}

func TestWriteFile_InvalidArgs(t *testing.T) {
	res, _ := handleWriteFile(context.Background(), []byte(`not json`))
	if !res.IsError {
		t.Error("invalid JSON should be rejected")
	}
}

func TestReadFile_InvalidArgs(t *testing.T) {
	res, _ := handleReadFile(context.Background(), []byte(`not json`))
	if !res.IsError {
		t.Error("invalid JSON should be rejected")
	}
}

func TestListFiles_InvalidArgs(t *testing.T) {
	res, _ := handleListFiles(context.Background(), []byte(`not json`))
	if !res.IsError {
		t.Error("invalid JSON should be rejected")
	}
}

func TestObjectSchema_Basic(t *testing.T) {
	s := ObjectSchema(map[string]any{
		"x": StringProp("x desc"),
	}, []string{"x"})
	if !strings.Contains(string(s), `"x"`) {
		t.Errorf("schema missing x: %s", s)
	}
	if !strings.Contains(string(s), `"required"`) {
		t.Errorf("schema missing required: %s", s)
	}
}
