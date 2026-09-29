//go:build !windows

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/fx/herdr"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/tmux"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestWiringLifecycleOwnsHerdrSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ao-herdr-wiring-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	store, err := sqlitetest.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stack := startLifecycle(ctx, dir, store, tmux.New(tmux.Options{}), nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { cancel(); stack.Stop() }()
	conn, err := net.DialTimeout("unix", herdr.SocketPath(dir), time.Second)
	if err != nil {
		t.Fatalf("lifecycle did not start Herdr listener: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, err = fmt.Fprintln(conn, `{"id":"7","method":"pane.report_agent","params":{"pane_id":"ao:1:bWVyLTE:bGF1bmNoLTE","source":"custom:fx","agent":"fx","state":"working"}}`)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		ID    string `json:"id"`
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	// A missing session must reach the real reducer and return a failed reply.
	if reply.ID != "7" || reply.OK || reply.Error == "" {
		t.Fatalf("report bypassed lifecycle: %+v", reply)
	}
	cancel()
	stack.Stop()
	if _, err := os.Lstat(herdr.SocketPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("shutdown retained Herdr socket: %v", err)
	}
}

func TestWiringHerdrFailureDoesNotPreventLifecycleStartup(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ao-herdr-wiring-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	store, err := sqlitetest.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := os.Mkdir(filepath.Join(dir, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(herdr.SocketPath(dir), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stack := startLifecycle(ctx, dir, store, tmux.New(tmux.Options{}), nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { cancel(); stack.Stop() }()
	if stack.LCM == nil {
		t.Fatal("listener failure disabled lifecycle")
	}
	if data, err := os.ReadFile(herdr.SocketPath(dir)); err != nil || string(data) != "occupied" {
		t.Fatalf("occupied path changed: %q %v", data, err)
	}
}
