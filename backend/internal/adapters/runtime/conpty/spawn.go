// spawn.go - shared detached pty-host spawn support. Platform files provide
// defaultSpawnHost; tests can inject a hostSpawner through Options.
package conpty

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// hostSpawner starts a detached pty-host for the session and returns its
// loopback address ("127.0.0.1:PORT") and OS pid once it prints READY. size is
// the grid the host's PTY starts at; zero leaves the host's default.
// Injectable for tests: replace this field on Options before calling New.
type hostSpawner func(ctx context.Context, sessionID, cwd string, argv []string, env map[string]string, size ports.TerminalSize) (addr string, pid int, err error)

// hostSizeArgPrefix marks the optional leading pty-host argument carrying the
// PTY's starting grid. Session ids never contain "=", so the flag cannot be
// mistaken for the positional session id.
const hostSizeArgPrefix = "--size="

// ptyHostArgs builds the pty-host subcommand argv:
// pty-host [--size=<cols>x<rows>] <sessionID> <cwd> <shellCmd> <shellArgs...>
func ptyHostArgs(sessionID, cwd string, argv []string, size ports.TerminalSize) []string {
	args := []string{"pty-host"}
	if !size.IsZero() {
		args = append(args, fmt.Sprintf("%s%dx%d", hostSizeArgPrefix, size.Cols, size.Rows))
	}
	args = append(args, sessionID, cwd)
	return append(args, argv...)
}

// splitHostSizeArg consumes the optional leading --size argument from the
// pty-host argv (everything after the subcommand name). A host spawned without
// one, e.g. by a daemon that predates sized creation, starts at defaultHostSize.
func splitHostSizeArg(args []string) (ports.TerminalSize, []string, error) {
	if len(args) == 0 || !strings.HasPrefix(args[0], hostSizeArgPrefix) {
		return defaultHostSize, args, nil
	}
	raw := strings.TrimPrefix(args[0], hostSizeArgPrefix)
	colsText, rowsText, ok := strings.Cut(raw, "x")
	cols, colsErr := strconv.ParseUint(colsText, 10, 16)
	rows, rowsErr := strconv.ParseUint(rowsText, 10, 16)
	if !ok || colsErr != nil || rowsErr != nil || cols == 0 || rows == 0 {
		return ports.TerminalSize{}, nil, fmt.Errorf("invalid size %q: want <cols>x<rows>", raw)
	}
	return ports.TerminalSize{Cols: uint16(cols), Rows: uint16(rows)}, args[1:], nil
}

// cleanupStartedHostFailure preserves evidence of a child that may still own
// the session. A successful kill proves the failed spawn left no runtime;
// otherwise the stable PID and joined cleanup error let Create expose a typed
// partial-effect result instead of incorrectly claiming no effect.
func cleanupStartedHostFailure(pid int, cause error, kill func() error) (int, error) {
	if killErr := kill(); killErr != nil {
		return pid, errors.Join(cause, fmt.Errorf("kill started pty-host pid %d: %w", pid, killErr))
	}
	return 0, cause
}

// stripEnvAssignments splits a launch argv that may begin with a Unix-style
// `env NAME=VALUE ...` prefix into the environment assignments ("NAME=VALUE"
// strings) and the real command argv that follows.
//
// Some agent adapters (e.g. opencode) prepend `env KEY=value` to their launch
// command to inject process env vars the CLI has no flag for. That is portable
// on macOS/Linux, where the tmux runtime runs the argv through a shell and the
// `env` coreutil applies the assignments. Windows has no `env` binary and the
// ConPTY pty-host execs argv[0] directly, so the spawner must apply the
// assignments to the child's environment itself — otherwise the launch fails
// with `env: executable file not found`. This mirrors launchBinary in the
// session manager, which already skips the same prefix to validate the real
// binary.
//
// If argv does not start with `env`, or the prefix consumes the whole argv with
// no command left, assignments is nil and rest is argv unchanged (so the
// caller's normal handling still applies).
func stripEnvAssignments(argv []string) (assignments, rest []string) {
	if len(argv) == 0 || filepath.Base(argv[0]) != "env" {
		return nil, argv
	}
	i := 1
	for i < len(argv) && strings.Contains(argv[i], "=") {
		i++
	}
	if i >= len(argv) {
		// Only assignments, no command to run: leave argv untouched so the
		// existing missing-binary path fires instead of silently dropping it.
		return nil, argv
	}
	return argv[1:i], argv[i:]
}

// interactiveTerminalEnv builds the environment inherited by the detached
// pty-host and, in turn, by the interactive agent process it owns.
//
// AO itself may run under an agent or CI process that sets NO_COLOR for
// captured logs. That ambient preference must not leak into an interactive
// terminal. Projects can still opt out of color explicitly through RuntimeConfig
// or an `env NO_COLOR=...` argv prefix. The native PTY and its xterm clients
// support 24-bit SGR color, so advertise that capability consistently with the
// legacy tmux runtime.
func interactiveTerminalEnv(base []string, configured map[string]string, assignments []string) []string {
	env := make([]string, 0, len(base)+len(configured)+len(assignments)+2)
	appendEntry := func(entry string, explicit bool) {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			env = append(env, entry)
			return
		}
		switch key {
		case "TERM", "COLORTERM":
			return
		case "NO_COLOR":
			if !explicit {
				return
			}
		}
		env = append(env, entry)
	}

	for _, entry := range base {
		appendEntry(entry, false)
	}
	for key, value := range configured {
		appendEntry(key+"="+value, true)
	}
	for _, entry := range assignments {
		appendEntry(entry, true)
	}
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	return env
}
