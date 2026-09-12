package workerexec

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

const outputChunkSize = 8 << 10

type Output struct {
	Stream string
	Text   string
}

type Runner interface {
	Run(context.Context, Command, func(Output) error) error
}

type OSRunner struct{}

func (OSRunner) Run(
	ctx context.Context,
	command Command,
	emit func(Output) error,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var emitErr error
	var emitMu sync.Mutex
	stream := func(name string) writerFunc {
		return func(value []byte) (int, error) {
			written := len(value)
			for len(value) > 0 {
				size := min(len(value), outputChunkSize)
				emitMu.Lock()
				if emitErr == nil {
					emitErr = emit(Output{Stream: name, Text: string(value[:size])})
					if emitErr != nil {
						cancel()
					}
				}
				emitMu.Unlock()
				value = value[size:]
			}
			return written, nil
		}
	}
	process, err := agentruntime.StartProcess(runCtx, agentruntime.ProcessConfig{
		Argv:   append([]string{command.Path}, command.Args...),
		Dir:    command.Dir,
		Env:    mergedEnvironment(command.Env),
		Stdout: stream("stdout"),
		Stderr: stream("stderr"),
	})
	if err != nil {
		return fmt.Errorf("start coding agent: %w", err)
	}
	result, waitErr := process.Wait(context.Background())
	emitMu.Lock()
	defer emitMu.Unlock()
	if emitErr != nil {
		return fmt.Errorf("publish coding-agent output: %w", emitErr)
	}
	if waitErr != nil {
		return fmt.Errorf("wait for coding agent: %w", waitErr)
	}
	if result.Err != nil {
		return fmt.Errorf("coding agent exited: %w", result.Err)
	}
	return nil
}

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(value []byte) (int, error) {
	return write(value)
}

func mergedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		for i := 0; i < len(entry); i++ {
			if entry[i] == '=' {
				values[entry[:i]] = entry[i+1:]
				break
			}
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	environment := make([]string, 0, len(values))
	for key, value := range values {
		environment = append(environment, key+"="+value)
	}
	return environment
}
