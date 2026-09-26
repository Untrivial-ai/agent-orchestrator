//go:build windows

package procmem

import "testing"

func TestReadSystemOnWindows(t *testing.T) {
	sys, err := ReadSystem()
	if err != nil {
		t.Fatal(err)
	}
	if sys.TotalBytes == 0 {
		t.Fatal("total memory must not be zero on a real machine")
	}
	if sys.AvailableBytes > sys.TotalBytes {
		t.Fatalf("available (%d) must not exceed total (%d)", sys.AvailableBytes, sys.TotalBytes)
	}
	if sys.CPUCount <= 0 {
		t.Fatalf("cpu count = %d, want at least 1", sys.CPUCount)
	}
	// The sentinel this platform commits to: never a real load figure.
	if sys.Load1 != -1 {
		t.Fatalf("load1 = %v, want the -1 sentinel on a platform with no load average", sys.Load1)
	}
	if sys.PressureSource != PressureSourceAvailablePct {
		t.Fatalf("pressure source = %q, want the available-percent fallback (no PSI on Windows)", sys.PressureSource)
	}
}

func TestReadSystemCPUTicksGrowBetweenTwoCalls(t *testing.T) {
	first, err := ReadSystem()
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReadSystem()
	if err != nil {
		t.Fatal(err)
	}
	if second.CPUTotalTicks < first.CPUTotalTicks {
		t.Fatalf("total ticks went backwards: %d then %d", first.CPUTotalTicks, second.CPUTotalTicks)
	}
	if second.CPUBusyTicks > second.CPUTotalTicks {
		t.Fatalf("busy (%d) must not exceed total (%d)", second.CPUBusyTicks, second.CPUTotalTicks)
	}
}
