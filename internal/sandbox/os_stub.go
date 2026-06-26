package sandbox

import (
	"os"
	"path/filepath"
)

func osGetenv(k string) string         { return os.Getenv(k) }
func osUserHomeDir() (string, error)    { return os.UserHomeDir() }
func filepathJoin(elem ...string) string { return filepath.Join(elem...) }
