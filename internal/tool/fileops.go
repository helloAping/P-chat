package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func listDir(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		size := info.Size()
		mod := info.ModTime().Format("2006-01-02 15:04")
		kind := "f"
		if e.IsDir() {
			kind = "d"
		}
		fmt.Fprintf(&sb, "%s  %8d  %s  %s\n", kind, size, mod, e.Name())
	}
	return sb.String(), nil
}
