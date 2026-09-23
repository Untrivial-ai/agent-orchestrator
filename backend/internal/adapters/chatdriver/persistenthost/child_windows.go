//go:build windows

package persistenthost

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func configureProviderProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

func killProviderProcess(ctx context.Context, cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	kill := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
