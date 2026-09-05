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
	args := make([]string, 0, 6+len(cfg.Argv))
	args = append(args, "chat-host", cfg.SessionID, cfg.DataDir, cfg.Workdir)
	if cfg.Protocol != ProtocolRaw {
		args = append(args, string(cfg.Protocol), cfg.LaunchFingerprint)
	}
	args = append(args, "--")
	args = append(args, cfg.Argv...)
	cmd := exec.Command(exe, args...)
	cmd.Dir = cfg.Workdir
	cmd.Env = cfg.Env
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn detached chat host: %w", err)
	}
	return cmd.Process.Release()
}
