//go:build windows

package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureHidden(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

func commandContextForPath(ctx context.Context, path string, args ...string) *exec.Cmd {
	if !isWindowsBatchFile(path) {
		return CommandContext(ctx, path, args...)
	}

	shell := strings.TrimSpace(os.Getenv("COMSPEC"))
	if shell == "" {
		shell = "cmd.exe"
	}
	cmd := exec.CommandContext(ctx, shell) //nolint:gosec // COMSPEC is the OS-selected command interpreter.
	configureHidden(cmd)
	cmd.Args = nil
	cmd.SysProcAttr.CmdLine = `/d /s /c "` + windowsBatchCommandLine(path, args) + `"`
	return cmd
}

func isWindowsBatchFile(path string) bool {
	extension := filepath.Ext(path)
	return strings.EqualFold(extension, ".cmd") || strings.EqualFold(extension, ".bat")
}

// cmd.exe uses different quoting rules from native Windows executables. The
// outer quotes are consumed by /s /c; each token remains quoted so spaces and
// command metacharacters cannot turn an argument into another command.
func windowsBatchCommandLine(executable string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteWindowsBatchArg(executable))
	for _, arg := range args {
		parts = append(parts, quoteWindowsBatchArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteWindowsBatchArg(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
