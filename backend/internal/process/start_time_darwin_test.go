//go:build darwin

package process

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestStartTimeCurrentProcess(t *testing.T) {
	first, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	second, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if first.IsZero() || first.After(time.Now()) || !first.Equal(second) || first.Location() != time.UTC || first.Nanosecond()%1000 != 0 {
		t.Fatalf("invalid or unstable process start times: %s, %s", first, second)
	}
	for _, pid := range []int{0, -1, 1 << 30} {
		if _, err := StartTime(pid); err == nil {
			t.Errorf("invalid PID %d admitted", pid)
		}
	}
	if _, err := StartTime(1 << 30); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("missing process has no typed sentinel: %v", err)
	}
}
