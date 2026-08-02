//go:build windows

package server

import (
	"syscall"
	"unsafe"
)

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS from psapi.h.
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

var (
	procGetCurrentProcess    = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentProcess")
	procGetProcessMemoryInfo = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
)

// processRSSMB returns the current process's resident set size in MB (the
// working set on Windows). 0 when the call fails.
func processRSSMB() int64 {
	var pmc processMemoryCounters
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	h, _, _ := procGetCurrentProcess.Call()
	r, _, _ := procGetProcessMemoryInfo.Call(h, uintptr(unsafe.Pointer(&pmc)), uintptr(unsafe.Sizeof(pmc)))
	if r == 0 {
		return 0
	}
	return int64(pmc.WorkingSetSize) >> 20
}
