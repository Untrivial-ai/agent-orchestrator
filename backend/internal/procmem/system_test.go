package procmem

import (
	"encoding/binary"
	"testing"
)

func TestParsePSISome10(t *testing.T) {
	some, ok := ParsePSISome10("some avg10=12.34 avg60=0.37 avg300=0.20 total=1068494611\nfull avg10=0.01 avg60=0.29 avg300=0.16 total=971321063\n")
	if !ok || some != 12.34 {
		t.Fatalf("some = %v ok=%v, want 12.34", some, ok)
	}
	if _, ok := ParsePSISome10("full avg10=0.01\n"); ok {
		t.Fatal("no some line must report not ok")
	}
}

func TestParseCPUTicksSkipsIdleAndIOWait(t *testing.T) {
	busy, total := ParseCPUTicks("cpu  100 20 50 800 30 5 5 0 0 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n")
	if busy != 180 || total != 1010 {
		t.Fatalf("busy=%d total=%d, want 180/1010", busy, total)
	}
	if b, tt := ParseCPUTicks("intr 1 2 3\n"); b != 0 || tt != 0 {
		t.Fatal("missing cpu line must read as zero")
	}
}

func TestAvailablePressureIsUsedShare(t *testing.T) {
	if got := availablePressure(System{TotalBytes: 16 << 30, AvailableBytes: 4 << 30}); got != 75 {
		t.Fatalf("pressure = %v, want 75", got)
	}
}

func TestParseVMStatComputesAvailableFromReclaimablePages(t *testing.T) {
	// A real vm_stat header plus the six lines the reader uses; other rows are
	// present in the real tool and must be ignored, not misparsed.
	out := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                             10000.
Pages active:                          200000.
Pages inactive:                          5000.
Pages speculative:                       2000.
Pages throttled:                            0.
Pages wired down:                       80000.
Pages purgeable:                         3000.
"Translation faults":               500000000.
Pages copy-on-write:                  1000000.
Pages zero filled:                  200000000.
Pages reactivated:                      10000.
Pages purged:                            5000.
File-backed pages:                     100000.
Anonymous pages:                       100000.
Pages stored in compressor:              1000.
Pages occupied by compressor:             500.
Decompressions:                          2000.
Compressions:                            3000.
Pageins:                             900000.
Pageouts:                              1234.
Swapins:                                   0.
Swapouts:                                  0.
`
	stat, err := ParseVMStat(out)
	if err != nil {
		t.Fatal(err)
	}
	if stat.PageSize != 16384 {
		t.Fatalf("page size = %d, want 16384", stat.PageSize)
	}
	if stat.Free != 10000 || stat.Inactive != 5000 || stat.Speculative != 2000 || stat.Purgeable != 3000 {
		t.Fatalf("stat = %+v", stat)
	}
	if stat.PageIns != 900000 || stat.PageOuts != 1234 {
		t.Fatalf("pageins/outs = %d/%d", stat.PageIns, stat.PageOuts)
	}
	if stat.SwapIns != 0 || stat.SwapOuts != 0 {
		t.Fatalf("swapins/outs = %d/%d, want 0/0 from the fixture", stat.SwapIns, stat.SwapOuts)
	}
	wantAvailable := uint64(10000+5000+2000+3000) * 16384
	if got := stat.AvailableBytes(); got != wantAvailable {
		t.Fatalf("available = %d, want %d", got, wantAvailable)
	}
}

// TestParseVMStatDistinguishesSwapFromOrdinaryPaging guards against reading
// Pageins/Pageouts (routine file-backed paging) as if they were swap
// activity: only Swapins/Swapouts should feed SwapIns/SwapOuts.
func TestParseVMStatDistinguishesSwapFromOrdinaryPaging(t *testing.T) {
	out := `Mach Virtual Memory Statistics: (page size of 4096 bytes)
Pages free:                             10000.
Pageins:                             900000.
Pageouts:                              1234.
Swapins:                                   7.
Swapouts:                                  3.
`
	stat, err := ParseVMStat(out)
	if err != nil {
		t.Fatal(err)
	}
	if stat.SwapIns != 7 || stat.SwapOuts != 3 {
		t.Fatalf("swapins/outs = %d/%d, want 7/3", stat.SwapIns, stat.SwapOuts)
	}
}

func TestParseVMStatRejectsOutputWithNoPageSize(t *testing.T) {
	if _, err := ParseVMStat("Pages free: 100.\n"); err == nil {
		t.Fatal("missing page-size header must error, not silently read zero pages")
	}
}

func TestParseSwapUsageReadsTotalAndUsedFromXswUsage(t *testing.T) {
	// xsw_usage: uint64 total, uint64 avail, uint64 used, then fields ignored here.
	raw := make([]byte, 32)
	binary.LittleEndian.PutUint64(raw[0:8], 2<<30)   // total: 2 GiB
	binary.LittleEndian.PutUint64(raw[16:24], 1<<30) // used: 1 GiB
	total, used := parseSwapUsage(raw)
	if total != 2<<30 || used != 1<<30 {
		t.Fatalf("total=%d used=%d", total, used)
	}
	if total, used := parseSwapUsage([]byte{1, 2, 3}); total != 0 || used != 0 {
		t.Fatal("a short buffer must read as zero, not panic")
	}
}

func TestParseLoadavgDividesByItsOwnScale(t *testing.T) {
	// struct loadavg: uint32[3] ldavg, then long fscale at offset 16.
	raw := make([]byte, 24)
	binary.LittleEndian.PutUint32(raw[0:4], 150)
	binary.LittleEndian.PutUint64(raw[16:24], 100)
	if got := parseLoadavg(raw); got != 1.5 {
		t.Fatalf("load1 = %v, want 1.5", got)
	}
	if got := parseLoadavg([]byte{1, 2, 3}); got != 0 {
		t.Fatalf("a short buffer must read as zero, got %v", got)
	}
}
