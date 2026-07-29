package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/p-chat/pchat/internal/paths"
)

func withProjectHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	paths.SetHomeForTest(home)
	t.Cleanup(func() { paths.SetHomeForTest("") })
	return home
}

func TestAddValidatesRequiredFields(t *testing.T) {
	withProjectHome(t)
	dir := t.TempDir()

	if _, err := Add("", dir); !errors.Is(err, ErrRequired) {
		t.Fatalf("empty name error = %v, want ErrRequired", err)
	}
	if _, err := Add("demo", ""); !errors.Is(err, ErrRequired) {
		t.Fatalf("empty path error = %v, want ErrRequired", err)
	}
}

func TestAddValidatesAbsoluteExistingDirectory(t *testing.T) {
	withProjectHome(t)

	if _, err := Add("demo", "relative/path"); !errors.Is(err, ErrPathNotAbsolute) {
		t.Fatalf("relative path error = %v, want ErrPathNotAbsolute", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Add("demo", missing); !errors.Is(err, ErrPathNotDirectory) {
		t.Fatalf("missing path error = %v, want ErrPathNotDirectory", err)
	}
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Add("demo", file); !errors.Is(err, ErrPathNotDirectory) {
		t.Fatalf("file path error = %v, want ErrPathNotDirectory", err)
	}
}

func TestAddCleansAndPersistsUnicodePath(t *testing.T) {
	withProjectHome(t)
	dir := filepath.Join(t.TempDir(), "中文项目")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	projects, err := Add("  我的项目  ", filepath.Join(dir, "."))
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects = %+v, want one", projects)
	}
	if projects[0].Name != "我的项目" {
		t.Fatalf("name = %q, want trimmed unicode name", projects[0].Name)
	}
	if projects[0].Path != filepath.Clean(dir) {
		t.Fatalf("path = %q, want %q", projects[0].Path, filepath.Clean(dir))
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Path != filepath.Clean(dir) {
		t.Fatalf("loaded = %+v, want persisted unicode path", loaded)
	}
}

func TestAddRejectsDuplicatePath(t *testing.T) {
	withProjectHome(t)
	dir := t.TempDir()
	if _, err := Add("demo", dir); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := Add("demo again", filepath.Join(dir, ".")); !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("duplicate error = %v, want ErrDuplicatePath", err)
	}
}
