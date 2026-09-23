package chat

import (
	"testing"
	"time"
)

func TestNativeHistorySettleDelayBacksOffToCap(t *testing.T) {
	want := []time.Duration{
		100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond,
		800 * time.Millisecond, 1600 * time.Millisecond, 2 * time.Second, 2 * time.Second,
	}
	for refresh, delay := range want {
		if got := nativeHistorySettleDelay(refresh); got != delay {
			t.Fatalf("nativeHistorySettleDelay(%d) = %v, want %v", refresh, got, delay)
		}
	}
	if got := nativeHistorySettleDelay(1000); got != nativeHistorySettlePollMax {
		t.Fatalf("nativeHistorySettleDelay(1000) = %v, want cap %v", got, nativeHistorySettlePollMax)
	}
}
