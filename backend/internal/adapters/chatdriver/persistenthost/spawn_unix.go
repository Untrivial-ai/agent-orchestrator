//go:build !windows

package persistenthost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func spawnDetached(ctx context.Context, cfg Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, hostArgs(cfg)...)
	cmd.Dir = cfg.Workdir
	cmd.Env = cfg.Env
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn detached chat host: %w", err)
	}
	// The chat host is intentionally detached from the daemon process group,
	// but it is still this process's child while the daemon remains alive.
	// Reap it asynchronously when it exits; Process.Release would discard the
	// handle without waiting and leave a zombie owned by the long-lived daemon.
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}
