//go:build windows

package gitworktree

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestIsRetryableRemoveErrorWindows(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "sharing violation", err: &os.PathError{Op: "unlinkat", Path: `C:\\worktree`, Err: windows.ERROR_SHARING_VIOLATION}, want: true},
		{name: "access denied", err: &os.PathError{Op: "unlinkat", Path: `C:\\worktree`, Err: windows.ERROR_ACCESS_DENIED}, want: true},
		{name: "other error", err: errors.New("disk failure"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableRemoveError(tt.err); got != tt.want {
				t.Fatalf("isRetryableRemoveError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
