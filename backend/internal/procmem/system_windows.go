//go:build windows

package procmem

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Neither call is wrapped by x/sys/windows (it covers kernel32 broadly, but
// not these two), so both are hand-bound the same way the psapi call in
// snapshot_windows.go is, and the same way internal/runfile/rename_windows.go
// already binds MoveFileExW.
var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
)

// memoryStatusEx mirrors MEMORYSTATUSEX. ullTotalPageFile/ullAvailPageFile
// are the commit limit (physical RAM plus page file), not the page file
// alone, so SwapUsedBytes below is a cruder figure than Linux's SwapUsed —
// noted the same way system_darwin.go notes its own approximations.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// ReadSystem reads host memory, swap and CPU on Windows. Every figure comes
// from one of two calls Windows already computes for us; unlike macOS this
// needs no page-count arithmetic and no process-table CPU fallback, because
// Windows hands over ready-made system-wide ticks the same shape Linux does.
//
// Load1 is left at -1, a sentinel meaning "not applicable on this platform":
// Windows has no load-average concept, and every current reader only ever
// sets this field to >= 0. Every consumer (backend/internal/httpd/controllers
// and backend/internal/service/usage) only copies it through, so the
// sentinel is safe; the one place that renders it hides that line when it
// sees a negative number.
func ReadSystem() (System, error) {
	sys := System{CPUCount: runtime.NumCPU(), Load1: -1}

	var mem memoryStatusEx
	mem.length = uint32(unsafe.Sizeof(mem))
	ret, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&mem)))
	if ret == 0 {
		return System{}, fmt.Errorf("procmem: GlobalMemoryStatusEx failed")
	}
	sys.TotalBytes = mem.totalPhys
	sys.AvailableBytes = mem.availPhys
	sys.SwapTotalBytes = mem.totalPageFile
	if mem.totalPageFile >= mem.availPageFile {
		sys.SwapUsedBytes = mem.totalPageFile - mem.availPageFile
	}

	// Windows exposes no page-fault counter at vm_stat's granularity without
	// turning on performance counters, so the swap rate stays unknown (zero)
	// rather than a fabricated number; SwapPages is deliberately left at 0.

	var idle, kernel, user windows.Filetime
	ret, _, _ = procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if ret != 0 {
		// Kernel time already includes idle time on Windows, so total is
		// kernel+user and busy is total minus idle — the standard formula
		// task managers use, matching the busy/total shape ParseCPUTicks
		// already produces on Linux so MemoryReader.SystemMemory diffs both
		// the same way.
		idleTicks, kernelTicks, userTicks := filetimeTicks(idle), filetimeTicks(kernel), filetimeTicks(user)
		sys.CPUTotalTicks = kernelTicks + userTicks
		if sys.CPUTotalTicks >= idleTicks {
			sys.CPUBusyTicks = sys.CPUTotalTicks - idleTicks
		}
	}

	// No PSI on Windows, so pressure is how little is available, same
	// fallback macOS uses.
	sys.PressureRaw, sys.PressureSource = availablePressure(sys), PressureSourceAvailablePct
	return sys, nil
}

func filetimeTicks(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}
