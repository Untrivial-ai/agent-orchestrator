//go:build !windows

package codexappserver

import "os"

// managedHomePrivate reports whether an AO-owned Codex credential home still
// carries the owner-only mode AO created it with.
func managedHomePrivate(_ string, info os.FileInfo) bool {
	return info.Mode().Perm() == 0o700
}
