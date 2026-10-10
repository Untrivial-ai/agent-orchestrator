package cua

import (
	"bytes"
	"context"
	"os"
	"strings"

	processutil "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// Output contains command diagnostics. Tool stdout is structured JSON.
type Output struct {
	Stdout []byte
	Stderr []byte
}

// Runner is the command boundary used by the adapter and lifecycle tests.
type Runner interface {
	Run(ctx context.Context, executable string, args, env []string) (Output, error)
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, executable string, args, env []string) (Output, error) {
	cmd := processutil.CommandContext(ctx, executable, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
}

func cleanEnvironment() []string {
	values := os.Environ()
	result := make([]string, 0, len(values))
	for _, entry := range values {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "AO_") || strings.HasPrefix(name, "CUA_DRIVER_") || name == "TMPDIR" {
			continue
		}
		result = append(result, entry)
	}
	return result
}
