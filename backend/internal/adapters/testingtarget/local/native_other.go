//go:build !darwin || !cgo

package local

import (
	"context"
	"errors"
	"time"
)

func nativeStartTime(int) (time.Time, error) {
	return time.Time{}, errors.New("native target requires macOS with cgo")
}

func nativeWindows(context.Context, int) ([]string, error) {
	return nil, errors.New("native target requires macOS with cgo")
}

func unusedPort() (int, error) {
	return 0, errors.New("native target requires macOS with cgo")
}
