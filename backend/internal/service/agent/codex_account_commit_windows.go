//go:build windows

package agent

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Windows can keep a just-exited Codex app-server handle alive for a short
// time after Process.Wait returns. During that window moving the completed
// credential home fails with a sharing/access violation. Retry only those
// transient lock errors; every structural or permission failure still fails
// closed immediately.
func renameCodexAccountDirectory(source, target string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		err = os.Rename(source, target)
		if err == nil {
			return nil
		}
		if !isTransientCodexAccountRenameError(err) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}

	for _, delay := range []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
	} {
		time.Sleep(delay)
		err = os.Rename(source, target)
		if err == nil {
			return nil
		}
		if !isTransientCodexAccountRenameError(err) {
			return err
		}
	}

	return err
}

func isTransientCodexAccountRenameError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
