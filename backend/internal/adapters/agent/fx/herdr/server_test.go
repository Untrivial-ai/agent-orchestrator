//go:build !windows

package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type channelSink chan signalRecord

func (s channelSink) ApplyActivitySignal(ctx context.Context, id domain.SessionID, signal ports.ActivitySignal) error {
	select {
	case s <- signalRecord{id, signal}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func socketDataDir(t *testing.T) string {
	t.Helper()
	// macOS's sockaddr_un is too short for long testing.T TempDir names.
	dir, err := os.MkdirTemp("/tmp", "ao-herdr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func startTestServer(t *testing.T, dir string) (*Server, context.CancelFunc, channelSink) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sink := make(channelSink, 128)
	server, err := Start(ctx, dir, sink, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); server.Stop() })
	return server, cancel, sink
}

const workingReport = `{"id":"7","method":"pane.report_agent","params":{"pane_id":"ao:1:bWVyLTE:bGF1bmNoLTE","source":"custom:fx","agent":"fx","state":"working"}}`

func socketRequest(path, line string) (response, error) {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return response{}, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return response{}, err
	}
	if _, err := fmt.Fprintln(conn, line); err != nil {
		return response{}, err
	}
	var reply response
	err = json.NewDecoder(conn).Decode(&reply)
	return reply, err
}

func TestSocketPermissionsAndCleanup(t *testing.T) {
	dir := socketDataDir(t)
	server, cancel, signals := startTestServer(t, dir)
	for path, mode := range map[string]os.FileMode{filepath.Join(dir, "run"): 0700, SocketPath(dir): 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: info=%v err=%v, want %o", path, info, err, mode)
		}
	}
	if reply, err := socketRequest(SocketPath(dir), workingReport); err != nil || !reply.OK {
		t.Fatalf("reply=%+v, err=%v", reply, err)
	}
	if got := <-signals; got.id != "mer-1" || got.signal.LaunchID != "launch-1" {
		t.Fatalf("signal=%+v", got)
	}
	cancel()
	server.Stop()
	server.Stop()
	if _, err := os.Lstat(SocketPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("socket survived shutdown: %v", err)
	}
}

func TestSocketReplacesOnlyStaleSockets(t *testing.T) {
	for _, kind := range []string{"stale", "live", "file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := socketDataDir(t)
			if err := os.Mkdir(filepath.Join(dir, "run"), 0755); err != nil {
				t.Fatal(err)
			}
			path := SocketPath(dir)
			switch kind {
			case "stale", "live":
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				t.Cleanup(func() { _ = listener.Close() })
				if kind == "stale" {
					_ = listener.Close()
				}
			case "file":
				if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(dir, "target"), path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server, err := Start(ctx, dir, make(channelSink, 1), slog.Default())
			if kind == "stale" {
				if err != nil {
					t.Fatalf("cannot restart with a stale socket: %v", err)
				}
				defer server.Stop()
				if reply, err := socketRequest(path, workingReport); err != nil || !reply.OK {
					t.Fatalf("restarted socket: %+v %v", reply, err)
				}
			} else {
				if err == nil {
					server.Stop()
					t.Fatal("replaced an occupied non-stale socket path")
				}
				after, err := os.Lstat(path)
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("existing path replaced: %v", err)
				}
			}
		})
	}
}

func TestSocketRejectsSymlinkRunDirectory(t *testing.T) {
	dir := socketDataDir(t)
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, "run")); err != nil {
		t.Fatal(err)
	}
	server, err := Start(context.Background(), dir, make(channelSink, 1), slog.Default())
	if err == nil {
		server.Stop()
		t.Fatal("followed a symlink run directory")
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("wrote outside AO data directory: %v %v", entries, err)
	}
}

func TestSocketMalformedAndOversizedMessagesAreNonfatal(t *testing.T) {
	dir := socketDataDir(t)
	_, _, signals := startTestServer(t, dir)
	conn, err := net.Dial("unix", SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(conn)
	for _, line := range []string{`{`, `{"id":"x","method":"future"}`, workingReport} {
		if _, err := fmt.Fprintln(conn, line); err != nil {
			t.Fatal(err)
		}
		data, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var reply response
		if err := json.Unmarshal(data, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.OK != (line == workingReport) {
			t.Fatalf("line=%s: %+v", line, reply)
		}
	}
	if _, err := socketRequest(SocketPath(dir), strings.Repeat("x", 64*1024)); err == nil {
		t.Fatal("oversized message was not disconnected")
	}
	if reply, err := socketRequest(SocketPath(dir), workingReport); err != nil || !reply.OK {
		t.Fatalf("listener did not survive malformed traffic: %+v %v", reply, err)
	}
	if len(signals) != 2 {
		t.Fatalf("signals = %d; malformed reports must not reach lifecycle", len(signals))
	}
}

func TestSocketConcurrentClients(t *testing.T) {
	dir := socketDataDir(t)
	_, _, signals := startTestServer(t, dir)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reply, err := socketRequest(SocketPath(dir), workingReport)
			if err == nil && !reply.OK {
				err = fmt.Errorf("rejected: %+v", reply)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(signals) != 16 {
		t.Fatalf("signals = %d, want 16", len(signals))
	}
}

func TestSocketBoundsIdleClientsAndStopsPromptly(t *testing.T) {
	dir := socketDataDir(t)
	server, cancel, _ := startTestServer(t, dir)
	conn, err := net.Dial("unix", SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := fmt.Fprint(conn, `{"unfinished":`); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	var timeout net.Error
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("idle partial message not closed")
	} else if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("server did not bound its read duration")
	}
	active, err := net.Dial("unix", SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	start := time.Now()
	cancel()
	server.Stop()
	if time.Since(start) > time.Second {
		t.Fatal("shutdown waited for client deadline")
	}
}

func TestSocketCleanupPreservesReplacementPath(t *testing.T) {
	dir := socketDataDir(t)
	server, _, _ := startTestServer(t, dir)
	path := SocketPath(dir)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	server.Stop()
	if data, err := os.ReadFile(path); err != nil || string(data) != "replacement" {
		t.Fatalf("removed replacement path: %q %v", data, err)
	}
}

func TestSocketBoundsConnectionCount(t *testing.T) {
	dir := socketDataDir(t)
	server, _, _ := startTestServer(t, dir)
	// Synchronize on accepted idle clients so kernel backlog scheduling cannot
	// make the overload assertion accidentally target one of the first clients.
	for i := 0; i < 32; i++ {
		conn, err := net.Dial("unix", SocketPath(dir))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
	}
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		count := len(server.clients)
		server.mu.Unlock()
		if count == 32 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d clients accepted", count)
		}
		time.Sleep(time.Millisecond)
	}
	conn, err := net.Dial("unix", SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	var b [1]byte
	var timeout net.Error
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("overload client was not closed")
	} else if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("connection limit was not enforced")
	}
}
