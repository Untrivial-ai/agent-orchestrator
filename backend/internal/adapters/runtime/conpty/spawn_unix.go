//go:build darwin || linux

package conpty

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// defaultSpawnHost launches the current AO executable as a detached pty-host.
// Setsid keeps the host alive when the daemon exits or Electron updates, while
// the registry lets the replacement daemon adopt it without touching the PTY.
func defaultSpawnHost(ctx context.Context, sessionID, cwd string, argv []string, env map[string]string) (string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", 0, fmt.Errorf("%s pty spawn: resolve executable: %w", runtime.GOOS, err)
	}

	envAssignments, argv := stripEnvAssignments(argv)
	args := append([]string{"pty-host", sessionID, cwd}, argv...)
	merged := interactiveTerminalEnv(os.Environ(), env, envAssignments)

	// Deliberately do not use CommandContext: once READY is received the host
	// must survive cancellation of the request that created it.
	cmd := exec.Command(exe, args...)
	cmd.Dir = cwd
	cmd.Env = merged
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	// Use explicit files instead of Cmd.StdoutPipe or a non-file Stderr writer.
	// Those helpers are finalized by Cmd.Wait, while a detached host may outlive
	// the daemon. The unlinked stderr file remains a valid, non-blocking sink in
	// the host after the parent closes its copy, and retains startup diagnostics
	// without leaving a pathname behind.
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		return "", 0, fmt.Errorf("%s pty spawn: stdout pipe: %w", runtime.GOOS, err)
	}
	stderrFile, err := os.CreateTemp("", "ao-pty-host-stderr-*.log")
	if err != nil {
		_ = stdout.Close()
		_ = childStdout.Close()
		return "", 0, fmt.Errorf("%s pty spawn: stderr file: %w", runtime.GOOS, err)
	}
	_ = os.Remove(stderrFile.Name())
	cmd.Stdout = childStdout
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = childStdout.Close()
		_ = stderrFile.Close()
		return "", 0, fmt.Errorf("%s pty spawn: start: %w", runtime.GOOS, err)
	}
	_ = childStdout.Close()

	type readyResult struct {
		addr string
		pid  int
		err  error
	}
	readyC := make(chan readyResult, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			match := readyRE.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
			if match == nil {
				continue
			}
			pid, _ := strconv.Atoi(match[1])
			port, _ := strconv.Atoi(match[2])
			readyC <- readyResult{addr: "127.0.0.1:" + strconv.Itoa(port), pid: pid}
			return
		}
		msg := fmt.Sprintf("%s pty spawn: pty-host exited without printing READY", runtime.GOOS)
		if diag := strings.TrimSpace(readUnixSpawnDiagnostics(stderrFile)); diag != "" {
			msg += ": " + diag
		}
		readyC <- readyResult{err: fmt.Errorf("%s", msg)}
	}()

	timer := time.NewTimer(spawnReadyTimeout)
	defer timer.Stop()
	select {
	case result := <-readyC:
		if result.err != nil {
			stopFailedUnixSpawn(cmd, stdout, stderrFile)
			return "", 0, result.err
		}
		pid := cmd.Process.Pid
		_ = stdout.Close()
		_ = stderrFile.Close()
		// Reap the host if it exits while this daemon is still running. Waiting
		// in a goroutine does not tie the child's lifetime to the parent; Setsid
		// still lets it survive daemon exit, after which init/launchd adopts it.
		go func() { _ = cmd.Wait() }()
		return result.addr, pid, nil
	case <-timer.C:
		stopFailedUnixSpawn(cmd, stdout, stderrFile)
		return "", 0, fmt.Errorf("%s pty spawn: pty-host startup timeout (%s)", runtime.GOOS, spawnReadyTimeout)
	case <-ctx.Done():
		stopFailedUnixSpawn(cmd, stdout, stderrFile)
		return "", 0, ctx.Err()
	}
}

func readUnixSpawnDiagnostics(stderr *os.File) string {
	if _, err := stderr.Seek(0, 0); err != nil {
		return ""
	}
	buf := make([]byte, maxCapturedStderr)
	n, _ := stderr.Read(buf)
	return string(buf[:n])
}

func stopFailedUnixSpawn(cmd *exec.Cmd, stdout, stderr *os.File) {
	_ = stdout.Close()
	_ = stderr.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}
