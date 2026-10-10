//go:build !darwin

package process

import (
	"errors"
	"time"
)

// StartTime is unavailable outside macOS; callers must refuse unknown identity.
func StartTime(_ int) (time.Time, error) {
	return time.Time{}, errors.New("process start-time observation is supported only on macOS")
}
