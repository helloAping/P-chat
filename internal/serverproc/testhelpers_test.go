package serverproc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// commandGo returns an exec.Cmd for `go <args...>`. Inherits the
// parent process env so PATH and GOROOT are correct.
func commandGo(args ...string) *exec.Cmd {
	return exec.Command("go", args...)
}

// repoRoot returns the module root (where go.mod lives) for the
// current package. Uses `go list -m` so it works regardless of
// where the test was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("go list returned empty module dir")
	}
	return root
}

// Abs returns the absolute path of the package's source dir
// (internal/serverproc), used to verify the test process.
func Abs() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Clean(wd), nil
}
