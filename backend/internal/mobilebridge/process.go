package mobilebridge

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// processProbeTimeout bounds the shell-out used to identify a process. A hung
// probe must not stall daemon startup.
const processProbeTimeout = 3 * time.Second

// IsLiveCloudflared reports whether pid is a running cloudflared.
//
// Both halves matter. Liveness alone is not enough because pids are reused, and
// ReapStaleTunnel would then kill whatever inherited the number — so this
// confirms the process's own name before anything is signalled. When identity
// cannot be established it returns false: leaking a tunnel until reboot is a
// far smaller harm than killing an unrelated process.
func IsLiveCloudflared(pid int) bool {
	if pid <= 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), processProbeTimeout)
	defer cancel()

	if runtime.GOOS == "windows" {
		out, err := exec.CommandContext(ctx, "tasklist",
			"/FI", "PID eq "+strconv.Itoa(pid), "/NH", "/FO", "CSV").Output()
		if err != nil {
			return false
		}
		return strings.Contains(strings.ToLower(string(out)), "cloudflared")
	}

	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false // no such process, or ps unavailable
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(string(out))), "cloudflared")
}

// KillProcess terminates pid. Callers must have confirmed the process's
// identity first — see IsLiveCloudflared.
func KillProcess(pid int) error {
	if runtime.GOOS == "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), processProbeTimeout)
		defer cancel()
		// Windows has no signals; /T takes the child tree with it, which
		// matters because cloudflared may have spawned helpers.
		return exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
