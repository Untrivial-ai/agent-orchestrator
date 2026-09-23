package previewserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestPreviewServerHelper(t *testing.T) {
	if os.Getenv("AO_PREVIEW_CRASH_HELPER") == "1" {
		serveUntilFirstRequestThenExit(t)
		return
	}
	if os.Getenv("AO_PREVIEW_TEST_HELPER") == "1" {
		servePreviewHelper(t)
		return
	}
}

// serveUntilFirstRequestThenExit binds the loopback port passed via PORT,
// answers HTTP requests until AO_PREVIEW_CRASH_AFTER_MS milliseconds have
// passed (default 200ms), then exits with a non-zero status. That window is
// long enough for the manager's readiness probe to flip the run to
// StateReady, and short enough that the subsequent waitForExit goroutine
// observes a crash rather than a user-initiated stop (issue #4500).
func serveUntilFirstRequestThenExit(t *testing.T) {
	t.Helper()
	port, err := strconv.Atoi(os.Getenv("PORT"))
	if err != nil || port <= 0 {
		t.Fatalf("invalid helper PORT %q", os.Getenv("PORT"))
	}
	delay := 2 * time.Second
	if raw := os.Getenv("AO_PREVIEW_CRASH_AFTER_MS"); raw != "" {
		if n, errConv := strconv.Atoi(raw); errConv == nil && n > 0 {
			delay = time.Duration(n) * time.Millisecond
		}
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stderr, "crash helper listening")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><title>crash helper</title>")
	})}
	go func() {
		time.Sleep(delay)
		_ = srv.Close()
		fmt.Fprintln(os.Stderr, "crash helper exiting")
		os.Exit(7)
	}()
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

func servePreviewHelper(t *testing.T) {
	t.Helper()
	port, err := strconv.Atoi(os.Getenv("PORT"))
	if err != nil || port <= 0 {
		t.Fatalf("invalid helper PORT %q", os.Getenv("PORT"))
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("preview helper ready")
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><title>managed preview</title>")
	})}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

func TestManagerStartsIsolatedConfiguredServerAndStopsIt(t *testing.T) {
	workspace := writeLaunchFile(t, []Configuration{helperConfiguration("web", TargetApp)})
	manager := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(manager.Close)

	status, err := manager.Start(context.Background(), "ao-1", workspace, "")
	if err != nil {
		t.Fatalf("Start: %v\nstatus=%+v", err, status)
	}
	if status.State != StateReady || status.Configuration != "web" || status.TargetKind != TargetApp {
		t.Fatalf("status = %+v, want ready web app", status)
	}
	if status.Port <= 0 || !strings.Contains(status.URL, strconv.Itoa(status.Port)) {
		t.Fatalf("status URL/port = %q/%d", status.URL, status.Port)
	}
	resp, err := http.Get(status.URL)
	if err != nil {
		t.Fatalf("GET managed preview: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}

	stopped, err := manager.Stop(context.Background(), "ao-1")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped.State != StateStopped {
		t.Fatalf("stopped state = %q", stopped.State)
	}
}

func TestManagerKeepsConcurrentSessionServersIsolated(t *testing.T) {
	workspace := writeLaunchFile(t, []Configuration{helperConfiguration("web", TargetApp)})
	manager := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(manager.Close)

	first, err := manager.Start(context.Background(), domain.SessionID("ao-1"), workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Start(context.Background(), domain.SessionID("ao-2"), workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Port == second.Port || first.URL == second.URL {
		t.Fatalf("session previews collided: first=%+v second=%+v", first, second)
	}
	if manager.Status("ao-1").State != StateReady || manager.Status("ao-2").State != StateReady {
		t.Fatalf("both session servers should remain ready")
	}
}

func TestManagerFiresOnExitWhenServerCrashesAfterLaunch(t *testing.T) {
	workspace := writeLaunchFile(t, []Configuration{crashConfiguration("crashy", TargetApp)})
	manager := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(manager.Close)

	gotStatus := make(chan Status, 1)
	// Register OnExit BEFORE Start so the readiness-to-crash window cannot
	// outrun the registration. The daemon wires the same way, after building
	// the preview manager but before any user request can reach Start.
	manager.SetOnExit(func(_ context.Context, _ domain.SessionID, s Status) {
		gotStatus <- s
	})

	status, err := manager.Start(context.Background(), domain.SessionID("ao-1"), workspace, "")
	if err != nil {
		t.Fatalf("Start: %v\nstatus=%+v", err, status)
	}
	if status.State != StateReady {
		t.Fatalf("status = %+v, want ready", status)
	}

	// Wait for the process to die on its own. The crash helper exits with a
	// non-zero code a few hundred ms after starting; waitForExit then flips
	// the run to StateFailed and fires the OnExit callback.
	select {
	case s := <-gotStatus:
		if s.State != StateFailed {
			t.Fatalf("onExit state = %q, want %q", s.State, StateFailed)
		}
		if s.Error == "" {
			t.Fatalf("onExit error is empty, want a non-empty message")
		}
		if s.URL != status.URL {
			t.Fatalf("onExit URL = %q, want %q", s.URL, status.URL)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OnExit callback never fired; manager did not notice the server crashed")
	}

	// Status must reflect the crash immediately, before the
	// failedStatusRetention window expires (issue #4500).
	post := manager.Status("ao-1")
	if post.State != StateFailed {
		t.Fatalf("post-crash state = %q, want %q", post.State, StateFailed)
	}
	if post.Error == "" {
		t.Fatalf("post-crash error is empty")
	}
}

func TestManagerDoesNotFireOnExitWhenStoppedByUser(t *testing.T) {
	workspace := writeLaunchFile(t, []Configuration{helperConfiguration("web", TargetApp)})
	manager := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(manager.Close)

	status, err := manager.Start(context.Background(), domain.SessionID("ao-1"), workspace, "")
	if err != nil {
		t.Fatalf("Start: %v\nstatus=%+v", err, status)
	}

	gotStatus := make(chan Status, 1)
	manager.SetOnExit(func(_ context.Context, _ domain.SessionID, s Status) {
		gotStatus <- s
	})

	if _, err := manager.Stop(context.Background(), "ao-1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case s := <-gotStatus:
		t.Fatalf("OnExit fired for a user-initiated stop: state=%q error=%q", s.State, s.Error)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestManagerRequiresNameWhenConfigurationsAreAmbiguous(t *testing.T) {
	workspace := writeLaunchFile(t, []Configuration{
		helperConfiguration("web", TargetApp),
		helperConfiguration("api", TargetAPI),
	})
	manager := New(nil)
	t.Cleanup(manager.Close)

	_, err := manager.Start(context.Background(), "ao-1", workspace, "")
	var serviceErr Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "PREVIEW_CONFIGURATION_REQUIRED" {
		t.Fatalf("error = %#v, want PREVIEW_CONFIGURATION_REQUIRED", err)
	}

	status, err := manager.Start(context.Background(), "ao-1", workspace, "api")
	if err != nil {
		t.Fatalf("named Start: %v", err)
	}
	if status.TargetKind != TargetAPI {
		t.Fatalf("targetKind = %q, want api", status.TargetKind)
	}
	_, _ = manager.Stop(context.Background(), "ao-1")
}

func TestManagerRejectsMissingConfigAndNonLoopbackURL(t *testing.T) {
	manager := New(nil)
	t.Cleanup(manager.Close)
	_, err := manager.Start(context.Background(), "ao-1", t.TempDir(), "")
	assertPreviewErrorCode(t, err, "PREVIEW_CONFIG_NOT_FOUND")

	cfg := helperConfiguration("web", TargetApp)
	cfg.URL = "https://example.com:${PORT}/"
	workspace := writeLaunchFile(t, []Configuration{cfg})
	_, err = manager.Start(context.Background(), "ao-1", workspace, "")
	assertPreviewErrorCode(t, err, "PREVIEW_CONFIG_INVALID")
}

func TestSelectPortRejectsOccupiedFixedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port

	_, reservation, err := reservePort(port, false)
	if reservation != nil {
		_ = reservation.Close()
	}
	assertPreviewErrorCode(t, err, "PREVIEW_PORT_IN_USE")
}

func TestReservePortHoldsSelectionUntilLaunch(t *testing.T) {
	port, reservation, err := reservePort(0, true)
	if err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if competing, err := net.Listen("tcp", address); err == nil {
		_ = competing.Close()
		t.Fatalf("selected port %d was not reserved", port)
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("released port %d remained unavailable: %v", port, err)
	}
	_ = listener.Close()
}

func TestPreviewEnvironmentDoesNotInheritDaemonCredentials(t *testing.T) {
	env := previewEnvironment(
		[]string{
			"PATH=/usr/bin",
			"HOME=/home/test",
			"GITHUB_TOKEN=secret",
			"AO_BROWSER_RUNTIME_TOKEN=runtime-secret",
		},
		map[string]string{"PUBLIC_FLAG": "enabled"},
		"session-1",
		4173,
	)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "GITHUB_TOKEN") || strings.Contains(joined, "AO_BROWSER_RUNTIME_TOKEN") {
		t.Fatalf("preview inherited daemon credentials: %v", env)
	}
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/test", "PUBLIC_FLAG=enabled", "AO_SESSION_ID=session-1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("preview env missing %q: %v", want, env)
		}
	}
}

func helperConfiguration(name string, kind TargetKind) Configuration {
	return Configuration{
		Name:               name,
		RuntimeExecutable:  os.Args[0],
		RuntimeArgs:        []string{"-test.run=^TestPreviewServerHelper$"},
		Port:               0,
		AutoPort:           true,
		URL:                "http://127.0.0.1:${PORT}/",
		TargetKind:         kind,
		Env:                map[string]string{"AO_PREVIEW_TEST_HELPER": "1"},
		ReadyTimeoutMillis: 5000,
	}
}

// crashConfiguration returns a configuration whose process boots the
// readiness probe on the same loopback port and then exits on its own with a
// non-zero status. The manager should treat this as StateFailed and fire
// OnExit (issue #4500).
func crashConfiguration(name string, kind TargetKind) Configuration {
	return Configuration{
		Name:               name,
		RuntimeExecutable:  os.Args[0],
		RuntimeArgs:        []string{"-test.run=^TestPreviewServerHelper$"},
		Port:               0,
		AutoPort:           true,
		URL:                "http://127.0.0.1:${PORT}/",
		TargetKind:         kind,
		ReadyTimeoutMillis: 5000,
		Env: map[string]string{
			"AO_PREVIEW_CRASH_HELPER": "1",
			"AO_PREVIEW_TEST_HELPER":  "1",
		},
	}
}

func writeLaunchFile(t *testing.T, configurations []Configuration) string {
	t.Helper()
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".ao")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(launchFile{Version: 1, Configurations: configurations})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "launch.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func assertPreviewErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var serviceErr Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != code {
		t.Fatalf("error = %#v, want %s", err, code)
	}
}
