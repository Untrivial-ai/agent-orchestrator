//go:build windows

// spawn_windows.go - real detached pty-host spawner for Windows using
// CREATE_NEW_PROCESS_GROUP + DETACHED_PROCESS so the host survives daemon exit.
package conpty

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// readyRE matches the "READY:<pid> <port>" line printed by RunHost.
var readyRE = regexp.MustCompile(`READY:(\d+) (\d+)`)

const spawnReadyTimeout = 10 * time.Second

// defaultSpawnHost resolves the current executable, builds the pty-host argv,
// and spawns it detached on Windows. It reads stdout for "READY:<pid> <port>"
// with a 10s timeout. Returns the loopback address and the pty-host OS PID.
func defaultSpawnHost(ctx context.Context, sessionID, cwd string, argv []string, env map[string]string) (string, int, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", 0, fmt.Errorf("conpty spawn: resolve executable: %w", err)
	}

	// Translate a leading `env NAME=VALUE ...` prefix into real child env vars.
	// Windows has no `env` binary and the pty-host execs argv[0] directly, so an
	// adapter that emits `env KEY=value <bin>` (e.g. opencode, to set
	// OPENCODE_CONFIG) would otherwise fail with "env: executable file not
	// found". The assignments are added to the pty-host environment below, which
	// the ConPTY child inherits (host_conpty_windows.go passes os.Environ()).
	envAssignments, argv := stripEnvAssignments(argv)

	// Build: <exe> pty-host <sessionID> <cwd> <shellCmd> <shellArgs...>
	args := append([]string{"pty-host", sessionID, cwd}, argv...)

	// Merge env: inherit parent, overlay caller-provided vars, then apply the
	// assignments stripped from the argv prefix.
	merged := os.Environ()
	for k, v := range env {
		merged = append(merged, k+"="+v)
	}
	merged = append(merged, envAssignments...)

	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	// Do not bind the long-lived pty-host to ctx with exec.CommandContext. The
	// caller's startup/request context is canceled after the spawn response is
	// written; after READY, the host must keep running until the session exits.
	cmd := exec.Command(exe, args...)
	cmd.Dir = cwd
	cmd.Env = merged

	// Windows process-creation flags: detached + hidden console.
	// ponytail: DETACHED_PROCESS puts the child in its own console; without it
	// the child is killed when the parent's console closes. CREATE_NEW_PROCESS_GROUP
	// insulates it from Ctrl+C sent to the parent. windowsHide suppresses the flash.
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}

	// Capture a bounded copy of the pty-host's stderr. It writes its startup
	// diagnostics there (listen/newConPTY failures) before exiting without
	// printing READY; retaining them lets us report the real cause below.
	stderr := &boundedBuffer{max: maxCapturedStderr}
	proc, err := startManagedHostProcess(cmd, stderr)
	if err != nil {
		return "", 0, fmt.Errorf("conpty spawn: start: %w", err)
	}

	// Read READY line with a timeout.
	readyC := make(chan struct {
		addr string
		pid  int
		err  error
	}, 1)

	go func() {
		defer proc.closeStdout()
		scanner := bufio.NewScanner(proc.stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			m := readyRE.FindStringSubmatch(line)
			if m != nil {
				pid, _ := strconv.Atoi(m[1])
				port, _ := strconv.Atoi(m[2])
				readyC <- struct {
					addr string
					pid  int
					err  error
				}{"127.0.0.1:" + strconv.Itoa(port), pid, nil}
				return
			}
		}
		msg := "conpty spawn: pty-host exited without printing READY"
		if diag := strings.TrimSpace(stderr.String()); diag != "" {
			msg += ": " + diag
		}
		readyC <- struct {
			addr string
			pid  int
			err  error
		}{"", 0, fmt.Errorf("%s", msg)}
	}()

	timer := time.NewTimer(spawnReadyTimeout)
	defer timer.Stop()

	select {
	case r := <-readyC:
		if r.err != nil {
			_ = proc.killAndWait()
			return "", 0, r.err
		}
		// Detach stdout so the child is not blocked. The process handle remains
		// owned by proc's waiter goroutine so cmd.Wait runs when pty-host exits.
		proc.closeStdout()
		return r.addr, cmd.Process.Pid, nil
	case <-timer.C:
		_ = proc.killAndWait()
		return "", 0, fmt.Errorf("conpty spawn: pty-host startup timeout (%s)", spawnReadyTimeout)
	case <-ctx.Done():
		_ = proc.killAndWait()
		return "", 0, ctx.Err()
	}
}
