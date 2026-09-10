//go:build !windows && !darwin && !linux

package conpty

import "errors"

// newConPTY is a stub on platforms without a detached PTY host. The serve
// engine and tests use a fake ptyConn; this keeps the package buildable on
// Linux.
func newConPTY(cwd, shellCmd string, shellArgs []string) (ptyConn, error) {
	return nil, errors.New("conpty: unsupported on this OS")
}
