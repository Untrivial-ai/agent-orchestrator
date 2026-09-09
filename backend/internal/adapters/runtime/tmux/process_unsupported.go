//go:build !linux && !darwin

package tmux

import (
	"context"
	"errors"
)

func readOwnedProcesses(context.Context) ([]ownedProcess, error) {
	return nil, errors.New("verified tmux process cleanup is unsupported on this platform")
}

func signalOwnedProcess(context.Context, ownedProcess, bool) error {
	return errors.New("verified tmux process signalling is unsupported on this platform")
}
