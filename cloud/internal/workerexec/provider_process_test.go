package workerexec

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"testing"
	"time"
)

func TestProviderCancellationStopsDescendantHoldingPipes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	process := exec.CommandContext(ctx, "sh", "-c", "sleep 30 & echo ready; wait")
	configureProviderProcess(process)
	stdout, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	process.Stderr = io.Discard
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stopProviderProcess(process) }()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child did not start: %q, %v", line, err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("provider process and descendant kept the pipes open after cancellation")
	}
}
