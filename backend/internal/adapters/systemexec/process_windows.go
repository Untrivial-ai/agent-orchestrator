//go:build windows

package systemexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func commandContext(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	resolved, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	extension := filepath.Ext(resolved)
	if !strings.EqualFold(extension, ".cmd") && !strings.EqualFold(extension, ".bat") {
		return exec.CommandContext(ctx, resolved, args...), nil //nolint:gosec // Callers supply server-owned argv.
	}

	shell := strings.TrimSpace(os.Getenv("ComSpec"))
	if shell == "" {
		shell = "cmd.exe"
	}
	cmd := exec.CommandContext(ctx, shell) //nolint:gosec // ComSpec is Windows' configured batch interpreter.
	cmd.Args = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/d /s /c "` + windowsBatchCommandLine(resolved, args) + `"`}
	return cmd, nil
}

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

func refreshExecutablePath() {
	paths := []string{os.Getenv("Path")}
	paths = append(paths,
		registryPath(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`),
		registryPath(registry.CURRENT_USER, `Environment`),
	)
	if merged := mergeWindowsPath(paths...); merged != "" {
		_ = os.Setenv("Path", merged)
	}
}

func registryPath(root registry.Key, keyPath string) string {
	key, err := registry.OpenKey(root, keyPath, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer func() { _ = key.Close() }()
	value, valueType, err := key.GetStringValue("Path")
	if err != nil {
		return ""
	}
	if valueType == registry.EXPAND_SZ {
		if expanded, expandErr := registry.ExpandString(value); expandErr == nil {
			value = expanded
		}
	}
	return value
}

func mergeWindowsPath(values ...string) string {
	seen := make(map[string]struct{})
	merged := make([]string, 0)
	for _, value := range values {
		for _, entry := range strings.Split(value, ";") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			key := strings.ToLower(strings.TrimRight(entry, `\/`))
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, entry)
		}
	}
	return strings.Join(merged, ";")
}

func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr.HideWindow = true
}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
