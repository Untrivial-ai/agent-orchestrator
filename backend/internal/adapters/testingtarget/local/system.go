package local

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"

	processutil "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

func signalProcess(pid int, signal syscall.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer func() { _ = process.Release() }()
	return process.Signal(signal)
}

func startElectron(executable, cwd string, env []string, log *os.File) (int, error) {
	command := processutil.Command(executable, ".")
	command.Dir, command.Env, command.Stdout, command.Stderr = cwd, env, log, log
	if err := command.Start(); err != nil {
		return 0, err
	}
	go func() { _ = command.Wait() }()
	return command.Process.Pid, nil
}

func processSnapshot(ctx context.Context) ([]processInfo, error) {
	command := processutil.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=")
	command.Env = strippedEnv(os.Environ())
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	var result []processInfo
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, errors.New("invalid process inventory")
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, err
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, err
		}
		result = append(result, processInfo{PID: pid, Parent: parent})
	}
	return result, nil
}
