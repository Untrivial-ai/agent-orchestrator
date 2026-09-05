//go:build windows

package tmux

import "context"

// Windows selects ConPTY rather than tmux. Keep the adapter buildable for
// cross-platform compile checks without adding Unix alias behavior here.
func socketAddress(ctx context.Context, socketPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return socketPath, nil
}
