//go:build !windows

package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestProcessWaitReturnsExitResult(t *testing.T) {
	var stdout bytes.Buffer
	process, err := StartProcess(context.Background(), ProcessConfig{
		Argv:   []string{"sh", "-c", "printf ready; exit 7"},
		Stdout: &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := process.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", result.ExitCode)
	}
	if result.Err == nil {
		t.Fatal("non-zero exit returned no process error")
	}
	if stdout.String() != "ready" {
		t.Fatalf("stdout = %q, want ready", stdout.String())
	}
}

func TestProcessContextCancellationStopsGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	process, err := StartProcess(ctx, ProcessConfig{
		Argv:          []string{"sh", "-c", "sleep 30 & wait"},
		ShutdownGrace: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	result, err := process.Wait(waitCtx)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("cancelled process exited successfully: %#v", result)
	}
}

func TestProcessWaitTimeoutDoesNotStopProcess(t *testing.T) {
	process, err := StartProcess(context.Background(), ProcessConfig{
		Argv: []string{"sh", "-c", "sleep 30"},
	})
	if err != nil {
		t.Fatal(err)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelWait()
	if _, err := process.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() error = %v, want deadline", err)
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := process.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}
