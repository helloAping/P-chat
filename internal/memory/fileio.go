package memory

import (
	"os"
)

// readFileOS reads a file; returns nil + error if missing.
func readFileOS(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// renameFile renames src to dst. Errors are returned but callers usually ignore.
func renameFile(src, dst string) error {
	return os.Rename(src, dst)
}
