package agentcreds

import (
	"context"
	"os/exec"
)

// commandRunner is the narrow keychain helper seam.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, path, args...).CombinedOutput()
}
