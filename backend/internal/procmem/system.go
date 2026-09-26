package procmem

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// System is the host's memory and CPU headroom at one instant. It answers
// "can the machine take more?" rather than "how much does AO hold?".
type System struct {
	TotalBytes     uint64
	AvailableBytes uint64
	// SwapTotalBytes and SwapUsedBytes describe configured swap; a host with
	// none reports zero for both.
	SwapTotalBytes uint64
	SwapUsedBytes  uint64
	// SwapPages counts pages ever swapped in plus out. Its growth between two
	// readings is the swapping that makes a machine feel frozen.
	SwapPages uint64
	// SwapPageBytes is the byte size of one unit counted in SwapPages. A
	// reader that doesn't know its page size leaves this zero.
	SwapPageBytes uint64
	// CPUCount and Load1 give the one-minute load per core: above one, work
	// is queueing for CPU. Load1 is -1 on a platform with no such concept
	// (Windows); every reader that does have one only ever sets it to >= 0.
	CPUCount int
	Load1    float64
	// CPUBusyTicks and CPUTotalTicks are the aggregate "cpu" line of
	// /proc/stat: jiffies spent busy (everything but idle and iowait) and in
	// total across all cores since boot. Two readings apart give the host's
	// CPU use over the gap; one alone says nothing.
	CPUBusyTicks  uint64
	CPUTotalTicks uint64
	// PressureRaw is the kernel's own memory-pressure figure: the share of
	// the last ten seconds some task spent stalled waiting on memory (PSI
	// "some avg10"). It tracks how the machine feels better than any free
	// percentage. PressureSource says which reading produced it: "psi", or
	// "available_pct" when PSI is unavailable and it is 100 minus the
	// available percentage instead.
	PressureRaw    float64
	PressureSource string
}

// Pressure sources.
const (
	PressureSourcePSI          = "psi"
	PressureSourceAvailablePct = "available_pct"
	// PressureSourceMemoryStatus is macOS's own kernel pressure level (the
	// same signal Activity Monitor's memory-pressure gauge uses), read via
	// kern.memorystatus_vm_pressure_level. It is a direct kernel verdict,
	// like PSI, not a derived free-memory percentage: macOS keeps very
	// little RAM "Free" by design (it fills spare RAM with reclaimable
	// cache), so the available-percent fallback reads as chronically tight
	// there even on an idle machine.
	PressureSourceMemoryStatus = "memorystatus"
)

// availablePressure stands in for PSI where the kernel has none (pre-4.20,
// CONFIG_PSI off, some containers): 100 minus the percent of RAM available.
func availablePressure(sys System) float64 {
	if sys.TotalBytes == 0 {
		return 0
	}
	return 100 - float64(sys.AvailableBytes)/float64(sys.TotalBytes)*100
}

// ParsePSISome10 reads "some avg10=N.NN ..." from PSI file contents.
func ParsePSISome10(contents string) (float64, bool) {
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "some" {
			continue
		}
		for _, field := range fields[1:] {
			if val, ok := strings.CutPrefix(field, "avg10="); ok {
				n, err := strconv.ParseFloat(val, 64)
				return n, err == nil
			}
		}
	}
	return 0, false
}

// ParseCPUTicks sums the whole-machine "cpu" line of /proc/stat: user nice
// system idle iowait irq softirq steal. Idle and iowait are the unused part.
func ParseCPUTicks(contents string) (busy, total uint64) {
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		for i, f := range fields[1:] {
			n, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return 0, 0
			}
			total += n
			if i != 3 && i != 4 {
				busy += n
			}
		}
		return busy, total
	}
	return 0, 0
}

// VMStat is the slice of vm_stat's page counts the monitor needs.
type VMStat struct {
	PageSize uint64
	// Free, Inactive, Speculative and Purgeable are the pages the kernel can
	// hand out without swapping anything. macOS publishes no single
	// "available" figure, so this is the nearest honest equivalent of Linux's
	// MemAvailable.
	Free        uint64
	Inactive    uint64
	Speculative uint64
	Purgeable   uint64
	// PageIns and PageOuts are lifetime counters of ordinary file-backed
	// paging, not swap: a Mac reads mapped files through these with no swap
	// traffic at all.
	PageIns  uint64
	PageOuts uint64
	// SwapIns and SwapOuts are the real swap-file counters; their growth
	// between two readings is the swapping that makes a machine feel frozen.
	SwapIns  uint64
	SwapOuts uint64
}

// AvailableBytes is what the kernel could give out right now.
func (v VMStat) AvailableBytes() uint64 {
	return (v.Free + v.Inactive + v.Speculative + v.Purgeable) * v.PageSize
}

// ParseVMStat reads `vm_stat` output: a header naming the page size, then
// "Label: N." lines. Unknown labels are ignored, so a newer macOS adding rows
// changes nothing here.
func ParseVMStat(contents string) (VMStat, error) {
	stat := VMStat{}
	for line := range strings.Lines(contents) {
		line = strings.TrimSpace(line)
		if stat.PageSize == 0 {
			if _, rest, ok := strings.Cut(line, "page size of "); ok {
				size, _, _ := strings.Cut(rest, " ")
				if n, err := strconv.ParseUint(size, 10, 64); err == nil {
					stat.PageSize = n
				}
			}
		}
		label, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(value), "."), 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(label) {
		case "Pages free":
			stat.Free = n
		case "Pages inactive":
			stat.Inactive = n
		case "Pages speculative":
			stat.Speculative = n
		case "Pages purgeable":
			stat.Purgeable = n
		case "Pageins":
			stat.PageIns = n
		case "Pageouts":
			stat.PageOuts = n
		case "Swapins":
			stat.SwapIns = n
		case "Swapouts":
			stat.SwapOuts = n
		}
	}
	if stat.PageSize == 0 {
		return VMStat{}, fmt.Errorf("procmem: vm_stat did not name a page size")
	}
	return stat, nil
}

// parseSwapUsage decodes the xsw_usage struct behind vm.swapusage: three
// 64-bit byte counts (total, available, used) and then fields we ignore.
func parseSwapUsage(raw []byte) (total, used uint64) {
	if len(raw) < 24 {
		return 0, 0
	}
	return binary.LittleEndian.Uint64(raw[0:8]), binary.LittleEndian.Uint64(raw[16:24])
}

// parseLoadavg decodes the loadavg struct behind vm.loadavg: three fixed-point
// averages, then the scale to divide them by. The scale sits at offset 16
// because the 64-bit field is aligned past the three 32-bit ones.
func parseLoadavg(raw []byte) float64 {
	if len(raw) < 24 {
		return 0
	}
	scale := binary.LittleEndian.Uint64(raw[16:24])
	if scale == 0 {
		return 0
	}
	return float64(binary.LittleEndian.Uint32(raw[0:4])) / float64(scale)
}
