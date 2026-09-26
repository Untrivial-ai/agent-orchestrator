//go:build linux

package procmem

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// pageSize is os.Getpagesize, indirected so a test can simulate a host with
// a larger page size (16 KiB, 64 KiB) without needing one to actually run on.
var pageSize = os.Getpagesize

// pageSizeBytes converts pageSize()'s int to the uint64 System.SwapPageBytes
// wants. os.Getpagesize() never returns negative in practice, but the
// conversion is guarded rather than bare so it can't wrap a stray negative
// into a huge unsigned value.
func pageSizeBytes() uint64 {
	n := pageSize()
	if n <= 0 {
		return 0
	}
	return uint64(n)
}

// ReadSystem reads host memory, swap, load and CPU from /proc.
func ReadSystem() (System, error) {
	sys := System{CPUCount: runtime.NumCPU()}
	if err := readMeminfo(&sys); err != nil {
		return System{}, err
	}
	// Swap activity and load are refinements; a host that hides them still
	// gets a memory reading.
	sys.SwapPages = readVMStatSwapPages()
	// pswpin/pswpout in /proc/vmstat are counted in pages, not bytes, and
	// Linux's page size is not always 4 KiB (some arches use 16 KiB or 64
	// KiB) — read the kernel's own answer rather than assuming.
	sys.SwapPageBytes = pageSizeBytes()
	sys.Load1 = readLoad1()
	sys.CPUBusyTicks, sys.CPUTotalTicks = readCPUTicks()
	if some, ok := readPSISome10("/proc/pressure/memory"); ok {
		sys.PressureRaw, sys.PressureSource = some, PressureSourcePSI
	} else {
		sys.PressureRaw, sys.PressureSource = availablePressure(sys), PressureSourceAvailablePct
	}
	return sys, nil
}

// readPSISome10 parses the "some avg10=" field of a PSI file.
func readPSISome10(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	return ParsePSISome10(string(data))
}

func readMeminfo(sys *System) error {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return fmt.Errorf("procmem: open /proc/meminfo: %w", err)
	}
	defer func() { _ = f.Close() }()
	var swapFree uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, kib, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(kib)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			sys.TotalBytes = n * 1024
		case "MemAvailable":
			sys.AvailableBytes = n * 1024
		case "SwapTotal":
			sys.SwapTotalBytes = n * 1024
		case "SwapFree":
			swapFree = n * 1024
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("procmem: read /proc/meminfo: %w", err)
	}
	if sys.TotalBytes == 0 {
		return fmt.Errorf("procmem: /proc/meminfo missing MemTotal")
	}
	if sys.SwapTotalBytes >= swapFree {
		sys.SwapUsedBytes = sys.SwapTotalBytes - swapFree
	}
	return nil
}

// readVMStatSwapPages sums pswpin and pswpout from /proc/vmstat; zero when
// unreadable.
func readVMStatSwapPages() uint64 {
	f, err := os.Open("/proc/vmstat")
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	var pages uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), " ")
		if !ok || (key != "pswpin" && key != "pswpout") {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		if err == nil {
			pages += n
		}
	}
	return pages
}

// readCPUTicks reads the aggregate cpu line of /proc/stat; zeros when
// unreadable.
func readCPUTicks() (busy, total uint64) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	return ParseCPUTicks(string(data))
}

// readLoad1 is the one-minute load average from /proc/loadavg; zero when
// unreadable.
func readLoad1() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return load
}
