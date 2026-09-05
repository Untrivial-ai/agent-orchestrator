//go:build darwin

package conpty

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func legacyListenerPID(ctx context.Context, addr string, _ int) (int, error) {
	return darwinTCPListenerPID(ctx, addr)
}

func legacyProcessIdentityForPID(ctx context.Context, pid int) (legacyProcessIdentity, error) {
	return darwinProcessIdentity(ctx, pid)
}

func legacyProcessIncarnationForPID(ctx context.Context, pid int) (legacyProcessIncarnation, error) {
	if err := ctx.Err(); err != nil {
		return legacyProcessIncarnation{}, err
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return legacyProcessIncarnation{}, err
	}
	return legacyProcessIncarnation{
		pid:         int(info.Proc.P_pid),
		ppid:        int(info.Eproc.Ppid),
		parentKnown: true,
		startedAt: time.Unix(
			info.Proc.P_starttime.Sec,
			int64(info.Proc.P_starttime.Usec)*int64(time.Microsecond),
		),
	}, nil
}

func darwinTCPListenerPID(ctx context.Context, addr string) (int, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		return 0, fmt.Errorf("legacy pty-host address %q is not an exact IPv4 loopback endpoint", addr)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return 0, fmt.Errorf("parse legacy pty-host port %q: %w", port, err)
	}
	// lsof is part of macOS and asks the kernel which process owns this exact
	// listening endpoint. -F emits stable machine-readable fields.
	out, err := exec.CommandContext(
		ctx,
		"/usr/sbin/lsof",
		"-nP",
		"-a",
		"-iTCP@"+host+":"+port,
		"-sTCP:LISTEN",
		"-Fp",
	).Output()
	if err != nil {
		return 0, fmt.Errorf("resolve legacy pty-host listener owner: %w", err)
	}
	owner := 0
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "p") {
			continue
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "p")))
		if parseErr != nil || pid <= 0 {
			return 0, fmt.Errorf("parse lsof listener owner %q", line)
		}
		if owner != 0 && owner != pid {
			return 0, fmt.Errorf("legacy pty-host endpoint has multiple listener owners")
		}
		owner = pid
	}
	if owner == 0 {
		return 0, fmt.Errorf("legacy pty-host endpoint has no listening owner")
	}
	return owner, nil
}

func darwinProcessIdentity(ctx context.Context, pid int) (legacyProcessIdentity, error) {
	incarnation, err := legacyProcessIncarnationForPID(ctx, pid)
	if err != nil {
		return legacyProcessIdentity{}, err
	}
	if incarnation.pid != pid {
		return legacyProcessIdentity{}, fmt.Errorf("kernel returned pid %d", incarnation.pid)
	}
	executable, argv, err := darwinProcessArgs(ctx, pid)
	if err != nil {
		return legacyProcessIdentity{}, err
	}
	return legacyProcessIdentity{
		pid:        pid,
		ppid:       incarnation.ppid,
		startedAt:  incarnation.startedAt,
		executable: executable,
		argv:       argv,
	}, nil
}

func darwinProcessArgs(ctx context.Context, pid int) (string, []string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", nil, err
	}
	if len(raw) < 5 {
		return "", nil, fmt.Errorf("kernel returned a short process-args payload")
	}
	argcValue := binary.LittleEndian.Uint32(raw[:4])
	if argcValue == 0 || argcValue > 4096 {
		return "", nil, fmt.Errorf("kernel returned invalid argc %d", argcValue)
	}
	argc := int(argcValue) // #nosec G115 -- argcValue is explicitly bounded to 1..4096 above
	rest := raw[4:]
	executableEnd := bytes.IndexByte(rest, 0)
	if executableEnd <= 0 {
		return "", nil, fmt.Errorf("kernel process-args payload has no executable path")
	}
	executable := string(rest[:executableEnd])
	rest = rest[executableEnd+1:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	argv := make([]string, 0, argc)
	for len(argv) < argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return "", nil, fmt.Errorf("kernel process-args payload ended after %d/%d argv entries", len(argv), argc)
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return executable, argv, nil
}
