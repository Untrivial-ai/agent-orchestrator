//go:build !windows

package agent

import "os"

func renameCodexAccountDirectory(source, target string) error {
	return os.Rename(source, target)
}
