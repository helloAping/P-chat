//go:build !windows

package server

import (
	"os"
	"strconv"
	"strings"
)

// processRSSMB returns the current process's resident set size in MB on Linux
// (from /proc/self/statm) and 0 on platforms without a statm procfs.
func processRSSMB() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize()) >> 20
}
