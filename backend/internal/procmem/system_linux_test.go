//go:build linux

package procmem

import "testing"

// TestReadSystemDerivesSwapPageBytesFromTheHostsRealPageSize guards against
// hardcoding 4 KiB: pswpin/pswpout in /proc/vmstat are page counts, and some
// architectures use a larger page (16 KiB, 64 KiB). SwapPageBytes must follow
// whatever the kernel actually reports, not a fixed constant.
func TestReadSystemDerivesSwapPageBytesFromTheHostsRealPageSize(t *testing.T) {
	orig := pageSize
	defer func() { pageSize = orig }()
	pageSize = func() int { return 16384 }

	sys, err := ReadSystem()
	if err != nil {
		t.Fatal(err)
	}
	if sys.SwapPageBytes != 16384 {
		t.Fatalf("SwapPageBytes = %d, want 16384 (a non-4 KiB page size)", sys.SwapPageBytes)
	}
}
