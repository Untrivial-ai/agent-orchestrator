package persistenthost

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Two daemons can observe the same dead descriptor before either starts its
// replacement. A stale observer must never delete ownership published by the
// winner; the host's O_EXCL launch lock is the only replacement authority.
func TestConnectOrStartConcurrentStaleProbeDoesNotStartRivalHost(t *testing.T) {
	dataDir := t.TempDir()
	sessionID := "stale-race"
	dir, _ := hostDir(dataDir, sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	startLog := filepath.Join(t.TempDir(), "providers.log")
	cfg := Config{SessionID: sessionID, DataDir: dataDir, Workdir: t.TempDir(),
		Env:  append(os.Environ(), "AO_START_LOG="+startLog),
		Argv: []string{"/bin/sh", "-c", `echo $$ >> "$AO_START_LOG"; exec cat`}}
	stale := Descriptor{
		Version: ProtocolVersion, SessionID: sessionID, OwnershipFingerprint: cfg.OwnershipFingerprint,
		Address: ln.Addr().String(), Token: "stale", PID: 2147483647, StartedAt: time.Now(),
	}
	if err := writeDescriptor(dataDir, stale); err != nil {
		t.Fatal(err)
	}

	accepted := make(chan net.Conn, 2)
	go func() {
		for i := 0; i < 2; i++ {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			var req hello
			if json.NewDecoder(conn).Decode(&req) != nil {
				_ = conn.Close()
				return
			}
			accepted <- conn
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan *Transport, 2)
	for i := 0; i < 2; i++ {
		go func() {
			tr, connectErr := ConnectOrStart(ctx, cfg)
			if connectErr == nil {
				results <- tr
			}
		}()
	}

	firstProbe, secondProbe := <-accepted, <-accepted
	_ = json.NewEncoder(firstProbe).Encode(helloResponse{Error: ErrUnauthorized.Error()})
	_ = firstProbe.Close()
	var firstHost int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if current, readErr := readDescriptor(dataDir, sessionID); readErr == nil && current.PID != stale.PID {
			firstHost = current.PID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if firstHost == 0 {
		t.Fatal("first recovery never published a replacement host")
	}
	t.Cleanup(func() { _ = syscall.Kill(-firstHost, syscall.SIGKILL) })

	// Release the second stale probe only after the first host owns the lock and
	// has published its descriptor. The old code deleted both files here.
	_ = json.NewEncoder(secondProbe).Encode(helloResponse{Error: ErrUnauthorized.Error()})
	_ = secondProbe.Close()
	time.Sleep(300 * time.Millisecond)
	secondHost := awaitDescriptor(t, dataDir, sessionID).PID
	if secondHost != firstHost {
		t.Cleanup(func() { _ = syscall.Kill(-secondHost, syscall.SIGKILL) })
	}

	cancel()
	for i := 0; i < 2; i++ {
		select {
		case tr := <-results:
			_ = tr.Stdin.Close()
		case <-time.After(100 * time.Millisecond):
		}
	}
	b, readErr := os.ReadFile(startLog)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
	starts := strings.Fields(string(b))
	if firstHost != secondHost || len(starts) != 1 {
		t.Fatalf("stale cleanup launched rival hosts: host_pids=%d,%d provider_starts=%v", firstHost, secondHost, starts)
	}
}
