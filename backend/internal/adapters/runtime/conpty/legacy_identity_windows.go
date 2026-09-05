//go:build windows

package conpty

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getExtendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

func legacyListenerPID(ctx context.Context, addr string, _ int) (int, error) {
	return windowsTCP4ListenerPID(ctx, addr)
}

func legacyProcessIdentityForPID(ctx context.Context, pid int) (legacyProcessIdentity, error) {
	return windowsProcessIdentity(ctx, pid)
}

func legacyProcessIncarnationForPID(ctx context.Context, pid int) (legacyProcessIncarnation, error) {
	startedAt, err := windowsProcessStartTime(ctx, pid)
	if err != nil {
		return legacyProcessIncarnation{}, err
	}
	return legacyProcessIncarnation{pid: pid, startedAt: startedAt}, nil
}

func windowsTCP4ListenerPID(ctx context.Context, addr string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		return 0, fmt.Errorf("legacy pty-host address %q is not an exact IPv4 loopback endpoint", addr)
	}
	portValue, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || portValue == 0 {
		return 0, fmt.Errorf("parse legacy pty-host port %q", portText)
	}

	var size uint32
	const tcpTableOwnerPIDListener = 3
	result, _, _ := getExtendedTCPTable.Call(
		0,
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(windows.AF_INET),
		tcpTableOwnerPIDListener,
		0,
	)
	if syscall.Errno(result) != windows.ERROR_INSUFFICIENT_BUFFER {
		return 0, fmt.Errorf("size Windows TCP owner table: %w", syscall.Errno(result))
	}
	if size < 4 {
		return 0, fmt.Errorf("Windows TCP owner table has invalid size %d", size)
	}
	table := make([]byte, size)
	result, _, _ = getExtendedTCPTable.Call(
		uintptr(unsafe.Pointer(&table[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(windows.AF_INET),
		tcpTableOwnerPIDListener,
		0,
	)
	if result != 0 {
		return 0, fmt.Errorf("read Windows TCP owner table: %w", syscall.Errno(result))
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	const tcpOwnerPIDRowSize = 24
	rowCount := binary.LittleEndian.Uint32(table[:4])
	if uint64(4)+uint64(rowCount)*tcpOwnerPIDRowSize > uint64(len(table)) {
		return 0, fmt.Errorf("Windows TCP owner table is truncated")
	}
	owner := 0
	for i := 0; i < int(rowCount); i++ {
		row := table[4+i*tcpOwnerPIDRowSize : 4+(i+1)*tcpOwnerPIDRowSize]
		if !net.IP(row[4:8]).Equal(net.IPv4(127, 0, 0, 1)) ||
			uint64(binary.BigEndian.Uint16(row[8:10])) != portValue {
			continue
		}
		pid := int(binary.LittleEndian.Uint32(row[20:24]))
		if pid <= 0 {
			return 0, fmt.Errorf("Windows TCP owner table returned invalid pid %d", pid)
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

type windowsCIMProcess struct {
	PID         int    `json:"pid"`
	PPID        int    `json:"ppid"`
	Executable  string `json:"executable"`
	CommandLine string `json:"commandLine"`
}

func windowsProcessIdentity(ctx context.Context, pid int) (legacyProcessIdentity, error) {
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if systemRoot == "" {
		return legacyProcessIdentity{}, fmt.Errorf("SystemRoot is unavailable")
	}
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	script := fmt.Sprintf(
		`$ErrorActionPreference='Stop'; $p=Get-CimInstance Win32_Process -Filter "ProcessId = %d"; if ($null -eq $p) { throw 'process not found' }; [pscustomobject]@{pid=[int]$p.ProcessId;ppid=[int]$p.ParentProcessId;executable=[string]$p.ExecutablePath;commandLine=[string]$p.CommandLine} | ConvertTo-Json -Compress`,
		pid,
	)
	out, err := exec.CommandContext(
		ctx,
		powershell,
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		script,
	).Output()
	if err != nil {
		return legacyProcessIdentity{}, err
	}
	var process windowsCIMProcess
	if err := json.Unmarshal(out, &process); err != nil {
		return legacyProcessIdentity{}, fmt.Errorf("decode PowerShell process identity: %w", err)
	}
	if process.PID != pid {
		return legacyProcessIdentity{}, fmt.Errorf("PowerShell returned pid %d", process.PID)
	}
	startedAt, err := windowsProcessStartTime(ctx, pid)
	if err != nil {
		return legacyProcessIdentity{}, err
	}
	argv, err := windows.DecomposeCommandLine(process.CommandLine)
	if err != nil {
		return legacyProcessIdentity{}, fmt.Errorf("parse process command line: %w", err)
	}
	return legacyProcessIdentity{
		pid:        process.PID,
		ppid:       process.PPID,
		startedAt:  startedAt,
		executable: process.Executable,
		argv:       argv,
	}, nil
}

func windowsProcessStartTime(ctx context.Context, pid int) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // read-only process probe
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, err
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, creation.Nanoseconds()), nil
}
