//go:build !windows

package agent

import "os"

func replaceCodexFile(source, target string) error {
	return os.Rename(source, target)
}
