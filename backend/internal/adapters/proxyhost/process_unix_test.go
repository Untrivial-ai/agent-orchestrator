//go:build !windows

package proxyhost

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestHelperProcessFixture is the helper the tests below start: the test
// binary run again through a wrapper script, answering only /ao/status.
func TestHelperProcessFixture(t *testing.T) {
	if os.Getenv("AO_HELPER_FIXTURE") != "1" {
		return
	}
	args := os.Args[len(os.Args)-4:]
	root, port := args[1], args[3]
	if args[0] != "--data-dir" || args[2] != "--port" || len(os.Getenv("AO_PROXY_CONTROL_KEY")) != 64 || len(os.Getenv("AO_PROXY_INFERENCE_KEY")) != 64 {
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(3)
	}
	session, _ := unix.Getsid(0)
	record, _ := json.Marshal(map[string]any{"args": os.Args, "pid": os.Getpid(), "session": session, "writable": os.Getenv("WRITABLE_PATH")})
	if os.WriteFile(filepath.Join(root, "fixture.json"), record, 0o600) != nil {
		os.Exit(4)
	}
	_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+os.Getenv("AO_PROXY_CONTROL_KEY") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"protocol_version":3}`))
	}))
}

type fixtureRecord struct {
	Args     []string
	PID      int
	Session  int
	Writable string
}

// processClient is a Client whose helper binary is the fixture.
func processClient(t *testing.T) *Client {
	t.Helper()
	t.Setenv("AO_HELPER_FIXTURE", "1")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	wrapper := filepath.Join(root, "ao-proxy-host")
	script := "#!/bin/sh\nexec '" + self + "' -test.run='^TestHelperProcessFixture$' -- \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := New(root, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopFixture(t, c.root) })
	return c
}

func readFixture(t *testing.T, root string) (record fixtureRecord, raw string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "fixture.json"))
	if err != nil {
		return record, ""
	}
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record, string(data)
}

func stopFixture(t *testing.T, root string) {
	t.Helper()
	record, _ := readFixture(t, root)
	if record.PID <= 0 {
		return
	}
	_ = syscall.Kill(record.PID, syscall.SIGKILL)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if syscall.Kill(record.PID, 0) != nil {
			return
		}
	}
	t.Errorf("fixture helper %d did not exit", record.PID)
}

func TestHelperStartsDetachedIsReusedAndRestartsOnTheSameIdentity(t *testing.T) {
	c := processClient(t)
	if err := c.ApplyRoutes(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	first, raw := readFixture(t, c.root)
	wantArgs := []string{"--data-dir", c.root, "--port", strconv.Itoa(c.id.Port)}
	if first.PID == os.Getpid() || first.Session != first.PID || first.Writable != c.root || !reflect.DeepEqual(first.Args[len(first.Args)-4:], wantArgs) {
		t.Fatalf("helper was not started detached with its own arguments: %+v", first)
	}
	// The keys travel in the environment; the fixture exits without them.
	for _, key := range []string{c.id.ControlKey, c.id.InferenceKey, c.id.TicketKey} {
		if strings.Contains(raw, key) {
			t.Fatal("a private key appeared in the helper's arguments")
		}
	}
	for _, private := range []string{"run/host.json", "logs/host.log"} {
		if stat, err := os.Stat(filepath.Join(c.root, private)); err != nil || stat.Mode().Perm() != 0o600 {
			t.Fatalf("%s: mode=%v err=%v", private, stat, err)
		}
	}
	// A second daemon finds the helper running and never starts its own.
	second, err := New(c.root, "/must-never-run")
	if err != nil || second.id != c.id {
		t.Fatalf("second daemon identity err=%v", err)
	}
	if err = second.ApplyRoutes(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if again, _ := readFixture(t, c.root); again.PID != first.PID {
		t.Fatal("a second helper was started")
	}
	stopFixture(t, c.root)
	if err = c.ApplyRoutes(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	restarted, _ := readFixture(t, c.root)
	if reloaded, err := New(c.root, ""); err != nil || reloaded.id != c.id || restarted.PID == first.PID || !reflect.DeepEqual(restarted.Args[len(restarted.Args)-4:], wantArgs) {
		t.Fatalf("restart changed the identity or reused a dead helper: err=%v", err)
	}
}

func TestWhateverAnswersOnTheHelpersPortIsNeverReplaced(t *testing.T) {
	for name, answer := range map[string]func(http.ResponseWriter){
		"another protocol": func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"protocol_version":2}`)) },
		"no protocol":      func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"routes":[]}`)) },
		"an error status":  func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
		"junk":             func(w http.ResponseWriter) { _, _ = w.Write([]byte(`<html>`)) },
	} {
		t.Run(name, func(t *testing.T) {
			c := processClient(t)
			listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(c.id.Port))
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { answer(w) }))
			}()
			if err = c.ApplyRoutes(ctx, nil, nil); err == nil {
				t.Fatal("a stranger on the helper's port was used")
			}
			if record, _ := readFixture(t, c.root); record.PID != 0 {
				t.Fatal("a helper was started beside whatever holds the port")
			}
		})
	}
}

func TestHelperThatCannotStartIsAClearError(t *testing.T) {
	c := processClient(t)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := c.ApplyRoutes(cancelled, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err=%v", err)
	}
	if record, _ := readFixture(t, c.root); record.PID != 0 {
		t.Fatal("a cancelled call started the helper")
	}
	c.binary = filepath.Join(c.root, "missing", "ao-proxy-host")
	err := c.ApplyRoutes(ctx, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "start account helper") || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err=%v", err)
	}
	if reloaded, err := New(c.root, ""); err != nil || reloaded.id != c.id {
		t.Fatalf("the identity did not survive a failed start: %v", err)
	}
}
