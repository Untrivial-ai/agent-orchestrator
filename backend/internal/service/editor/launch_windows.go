//go:build windows

package editor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// launchCommand routes a resolved editor launcher through cmd.exe when it is
// a .cmd or .bat shim — the layout every VS Code fork's CLI installs as (e.g.
// "code.cmd"). CreateProcess cannot execute a batch file directly; handing
// one to it fails with "%1 is not a valid Win32 application" even though
// exec.LookPath happily resolved it. Native executables (JetBrains IDEs, Zed,
// Sublime) go through the ordinary hidden-window launch untouched.
func launchCommand(name string, args ...string) *exec.Cmd {
	if !isWindowsBatchFile(name) {
		return aoprocess.Command(name, args...)
	}
	shell := strings.TrimSpace(os.Getenv("COMSPEC"))
	if shell == "" {
		shell = "cmd.exe"
	}
	cmd := exec.Command(shell) //nolint:gosec // COMSPEC is the OS-selected command interpreter for .cmd/.bat shims
	cmd.Args = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `/d /s /c "` + windowsBatchCommandLine(name, args) + `"`,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	return cmd
}

func isWindowsBatchFile(path string) bool {
	extension := filepath.Ext(path)
	return strings.EqualFold(extension, ".cmd") || strings.EqualFold(extension, ".bat")
}

// cmd.exe uses different quoting rules from native Windows executables. The
// outer quotes are consumed by /s /c; each token remains quoted for paths and
// arguments containing spaces. Doubling embedded quotes preserves them for the
// batch shim instead of allowing them to terminate the command early.
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
