//go:build !windows

package agent

import "context"

func (m *codexAccountManager) prepareLoginTerminalForCommit(_ context.Context, _ string) error {
	return nil
}
