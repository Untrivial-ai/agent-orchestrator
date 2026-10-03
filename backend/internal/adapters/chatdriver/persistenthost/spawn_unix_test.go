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

func TestDetachedChatHostsAreReapedAfterRepeatedShutdown(t *testing.T) {
	cfg := Config{
		SessionID: "reaped-host", DataDir: t.TempDir(), Workdir: t.TempDir(),
		Env:  append(os.Environ(), "AO_CHAT_HOST_PROVIDER_HELPER=1"),
		Argv: []string{os.Args[0], "-test.run=TestProviderHelper"},
	}
	t.Cleanup(func() { _ = Shutdown(context.Background(), cfg.DataDir, cfg.SessionID) })
	for range 3 {
		transport, err := ConnectOrStart(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		d, err := readDescriptor(cfg.DataDir, cfg.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		_ = transport.Stdin.Close()
		if err := Shutdown(context.Background(), cfg.DataDir, cfg.SessionID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			if err := syscall.Kill(d.PID, 0); errors.Is(err, syscall.ESRCH) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("detached chat host %d remained a child after shutdown", d.PID)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
