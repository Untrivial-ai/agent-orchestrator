//go:build windows

package ptyregistry

import (
	"context"

	"golang.org/x/sys/windows"
)

// replaceRegistryFile uses MOVEFILE_WRITE_THROUGH so Windows does not report
// successful registration before the replacement reaches durable storage.
func replaceRegistryFile(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fromPtr, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		fromPtr,
		toPtr,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}
