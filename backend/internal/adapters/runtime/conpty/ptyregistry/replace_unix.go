//go:build !windows

package ptyregistry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// replaceRegistryFile publishes the synced temporary file and then syncs its
// parent directory. The latter makes the rename itself durable across a crash,
// rather than merely making the temporary file's contents durable.
func replaceRegistryFile(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}
