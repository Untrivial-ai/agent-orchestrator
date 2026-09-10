//go:build windows

package persistenthost

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
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
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn detached chat host: %w", err)
	}
	return cmd.Process.Release()
}
