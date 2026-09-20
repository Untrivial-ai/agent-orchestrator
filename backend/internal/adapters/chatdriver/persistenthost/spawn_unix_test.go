//go:build !windows

package persistenthost

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestDetachedChatHostIsReapedAfterShutdown(t *testing.T) {
	dataDir := t.TempDir()
	cfg := Config{
		SessionID: "reaped-detached-host",
		DataDir:   dataDir,
		Workdir:   t.TempDir(),
		Env:       append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv:      []string{os.Args[0], "-test.run=TestProviderHelper"},
	}
	transport, err := ConnectOrStart(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ConnectOrStart: %v", err)
	}
	d := awaitDescriptor(t, dataDir, cfg.SessionID)
	_ = transport.Stdin.Close()
	if err := Shutdown(context.Background(), dataDir, cfg.SessionID); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for processEntryExists(d.PID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processEntryExists(d.PID) {
		t.Fatalf("detached chat host pid %d remains in the process table after shutdown", d.PID)
	}
}

func processEntryExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
