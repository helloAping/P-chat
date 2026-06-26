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
func TestReadFile_RefusesImageByExtension(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "image.png")
	if err := os.WriteFile(p, []byte("fake png content"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := handleReadFile(context.Background(), []byte(`{"path":"`+filepath.ToSlash(p)+`"}`))
	if !res.IsError {
		t.Fatalf("expected error, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "image") {
		t.Errorf("error should mention image, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "vision") {
		t.Errorf("error should hint at vision, got %q", res.Content)
	}
}

func TestReadFile_RefusesAudioByExtension(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(p, []byte("fake mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := handleReadFile(context.Background(), []byte(`{"path":"`+filepath.ToSlash(p)+`"}`))
	if !res.IsError {
		t.Fatalf("expected error, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "audio") {
		t.Errorf("error should mention audio, got %q", res.Content)
	}
}

func TestReadFile_RefusesBinaryByContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.bin")
	// PNG signature: 89 50 4E 47 0D 0A 1A 0A + garbage.
	binary := append([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, []byte("not really a png")...)
	if err := os.WriteFile(p, binary, 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := handleReadFile(context.Background(), []byte(`{"path":"`+filepath.ToSlash(p)+`"}`))
	if !res.IsError {
		t.Fatalf("expected error, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "binary") {
		t.Errorf("error should mention binary, got %q", res.Content)
	}
}

func TestReadFile_AllowsText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(p, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := handleReadFile(context.Background(), []byte(`{"path":"`+filepath.ToSlash(p)+`"}`))
	if res.IsError {
		t.Fatalf("text file should not error, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "hello world") {
		t.Errorf("text content not returned, got %q", res.Content)
	}
}

func TestReadFile_AllowsUnknownExtensionWithText(t *testing.T) {
	// .xyz is not in the binary list. Even with an unknown
	// extension, a text-content file should be readable.
	dir := t.TempDir()
	p := filepath.Join(dir, "weird.xyz")
	if err := os.WriteFile(p, []byte("just text"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := handleReadFile(context.Background(), []byte(`{"path":"`+filepath.ToSlash(p)+`"}`))
	if res.IsError {
		t.Fatalf("unknown-ext text should not error, got %q", res.Content)
	}
}

func TestIsBinary(t *testing.T) {
	if isBinary([]byte("plain text")) {
		t.Error("plain text shouldn't be binary")
	}
	if !isBinary([]byte{0x00, 0x01, 0x02}) {
		t.Error("a NUL byte should make it binary")
	}
	if isBinary(nil) {
		t.Error("empty should not be binary")
	}
}