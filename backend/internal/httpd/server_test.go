package httpd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthProbes(t *testing.T) {
	router := newTestRouter(config.Config{}, discardLogger(), nil)
	srv := httptest.NewServer(router)
	defer srv.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q, want JSON", path, ct)
		}
	}
}

func TestHealthProbesIncludeDaemonIdentity(t *testing.T) {
	router := newTestRouter(config.Config{StartupWorkingDirectory: "/startup"}, discardLogger(), nil)
	srv := httptest.NewServer(router)
	defer srv.Close()

	wantExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		var body struct {
			ExecutablePath          string `json:"executablePath"`
			WorkingDirectory        string `json:"workingDirectory"`
			StartupWorkingDirectory string `json:"startupWorkingDirectory"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if body.ExecutablePath != wantExe {
			t.Errorf("GET %s executablePath = %q, want %q", path, body.ExecutablePath, wantExe)
		}
		if body.WorkingDirectory != wantCWD {
			t.Errorf("GET %s workingDirectory = %q, want %q", path, body.WorkingDirectory, wantCWD)
		}
		if body.StartupWorkingDirectory != "/startup" {
			t.Errorf("GET %s startupWorkingDirectory = %q, want /startup", path, body.StartupWorkingDirectory)
		}
	}
}

func TestHealthProbesIncludeAppImageIdentity(t *testing.T) {
	client := &http.Client{Timeout: 2 * time.Second}

	t.Run("reports AO_APPIMAGE when set", func(t *testing.T) {
		t.Setenv("AO_APPIMAGE", "/home/user/Apps/agent-orchestrator.AppImage")
		router := newTestRouter(config.Config{}, discardLogger(), nil)
		srv := httptest.NewServer(router)
		defer srv.Close()

		for _, path := range []string{"/healthz", "/readyz"} {
			resp, err := client.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer resp.Body.Close()
			var body struct {
				AppImagePath string `json:"appImagePath"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			if body.AppImagePath != "/home/user/Apps/agent-orchestrator.AppImage" {
				t.Errorf("GET %s appImagePath = %q, want the AO_APPIMAGE value", path, body.AppImagePath)
			}
		}
	})

	t.Run("omits appImagePath when AO_APPIMAGE is unset", func(t *testing.T) {
		t.Setenv("AO_APPIMAGE", "")
		router := newTestRouter(config.Config{}, discardLogger(), nil)
		srv := httptest.NewServer(router)
		defer srv.Close()

		for _, path := range []string{"/healthz", "/readyz"} {
			resp, err := client.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer resp.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			if _, ok := body["appImagePath"]; ok {
				t.Errorf("GET %s appImagePath present, want omitted", path)
			}
		}
	})
}

// TestServerLifecycle exercises the full Run loop: bind an ephemeral port,
// publish running.json, serve a request, then cancel the context and confirm a
// clean shutdown that removes the handshake file.
func TestServerLifecycle(t *testing.T) {
	runPath := filepath.Join(t.TempDir(), "running.json")
	cfg := config.Config{
		Host:            "127.0.0.1",
		Port:            0, // let the OS pick a free port — no conflict with a real daemon
		ShutdownTimeout: 5 * time.Second,
		RunFilePath:     runPath,
	}

	srv, err := NewWithDeps(cfg, discardLogger(), nil, APIDeps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run(ctx) }()

	// Wait for the handshake file to confirm the server is up.
	base := "http://" + srv.Addr().String()
	waitForHealth(t, base)
	readyResp, err := http.Get(base + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz for Run: %v", err)
	}
	readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz for Run = %d, want 200", readyResp.StatusCode)
	}

	info, err := runfile.Read(runPath)
	if err != nil {
		t.Fatalf("read run-file: %v", err)
	}
	if info == nil {
		t.Fatal("run-file not written while server running")
		return
	}
	if info.Port == 0 {
		t.Error("run-file recorded port 0; want the actual bound port")
	}

	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}

	if after, _ := runfile.Read(runPath); after != nil {
		t.Error("run-file still present after shutdown; want it removed")
	}
}

func TestServerRunWithReadyPublishesBeforeCallback(t *testing.T) {
	runPath := filepath.Join(t.TempDir(), "running.json")
	cfg := config.Config{
		Host:            "127.0.0.1",
		Port:            0,
		ShutdownTimeout: 5 * time.Second,
		RunFilePath:     runPath,
	}
	srv, err := NewWithDeps(cfg, discardLogger(), nil, APIDeps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		runErr <- srv.RunWithReady(ctx, func(context.Context) error {
			close(ready)
			return nil
		})
	}()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("ready callback was not called")
	}
	if info, err := runfile.Read(runPath); err != nil || info == nil {
		t.Fatalf("run-file unavailable in ready callback: info=%v err=%v", info, err)
	}
	waitForHealth(t, "http://"+srv.Addr().String())

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("RunWithReady returned error on graceful shutdown: %v", err)
	}
}

func TestServerRunWithReadyGatesReadinessUntilStartupRecoveryCompletes(t *testing.T) {
	runPath := filepath.Join(t.TempDir(), "running.json")
	cfg := config.Config{
		Host:            "127.0.0.1",
		Port:            0,
		ShutdownTimeout: 5 * time.Second,
		RunFilePath:     runPath,
	}
	srv, err := NewWithDeps(cfg, discardLogger(), nil, APIDeps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recoveryStarted := make(chan struct{})
	releaseRecovery := make(chan struct{})
	recoveryReleased := false
	defer func() {
		if !recoveryReleased {
			close(releaseRecovery)
		}
	}()
	runErr := make(chan error, 1)
	go func() {
		runErr <- srv.RunWithReady(ctx, func(context.Context) error {
			close(recoveryStarted)
			<-releaseRecovery
			return nil
		})
	}()

	select {
	case <-recoveryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("startup recovery callback was not called")
	}

	base := "http://" + srv.Addr().String()
	waitForHealth(t, base)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(base + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz while startup recovery is blocked: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz while startup recovery is blocked = %d, want 503", resp.StatusCode)
	}

	close(releaseRecovery)
	recoveryReleased = true
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err = client.Get(base + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /readyz did not become 200 after startup recovery completed: last response=%v err=%v", resp, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("RunWithReady returned error on graceful shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunWithReady did not return after context cancel")
	}
}

func TestServerRunWithReadyRetriesTransientStartupRecoveryFailure(t *testing.T) {
	runPath := filepath.Join(t.TempDir(), "running.json")
	srv, err := NewWithDeps(config.Config{
		Host:            "127.0.0.1",
		Port:            0,
		ShutdownTimeout: 5 * time.Second,
		RunFilePath:     runPath,
	}, discardLogger(), nil, APIDeps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	runErr := make(chan error, 1)
	go func() {
		runErr <- srv.RunWithReady(ctx, func(context.Context) error {
			if attempts.Add(1) < 3 {
				return errors.New("temporary runtime probe failure")
			}
			return nil
		})
	}()

	base := "http://" + srv.Addr().String()
	waitForHealth(t, base)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, requestErr := client.Get(base + "/readyz")
		if requestErr == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /readyz did not become ready after transient recovery failures: attempts=%d err=%v", attempts.Load(), requestErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("startup recovery attempts = %d, want 3", got)
	}

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("RunWithReady returned error after recovery succeeded: %v", err)
	}
}

func TestRunStartupRecoveryStopsBlockedAttemptWhenContextIsCancelled(t *testing.T) {
	srv := &Server{
		log:               discardLogger(),
		shutdownRequested: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	recoveryStarted := make(chan struct{})
	releaseRecovery := make(chan struct{})
	recoveryErr := make(chan error, 1)
	go func() {
		recoveryErr <- srv.runStartupRecovery(ctx, func(context.Context) error {
			close(recoveryStarted)
			<-releaseRecovery
			return nil
		})
	}()

	select {
	case <-recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("startup recovery callback was not called")
	}
	cancel()

	select {
	case <-recoveryErr:
		t.Fatal("startup recovery returned before its callback had unwound")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseRecovery)
	select {
	case err := <-recoveryErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("startup recovery error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("startup recovery did not return after its callback unwound")
	}
}

func TestRunStartupRecoveryWarnsButStillDrainsCallbackThatIgnoresCancellation(t *testing.T) {
	srv := &Server{
		log:                           discardLogger(),
		shutdownRequested:             make(chan struct{}),
		startupRecoveryAttemptTimeout: 10 * time.Millisecond,
		startupRecoveryDrainWarning:   15 * time.Millisecond,
	}
	recoveryStarted := make(chan struct{})
	releaseRecovery := make(chan struct{})
	recoveryErr := make(chan error, 1)
	go func() {
		recoveryErr <- srv.runStartupRecovery(context.Background(), func(context.Context) error {
			close(recoveryStarted)
			<-releaseRecovery
			return nil
		})
	}()

	select {
	case <-recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("startup recovery callback was not called")
	}
	deadline := time.Now().Add(time.Second)
	for !srv.startupFailed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !srv.startupFailed.Load() {
		t.Fatal("non-cooperative recovery did not publish terminal startup failure")
	}
	select {
	case <-recoveryErr:
		t.Fatal("startup recovery returned while its callback could still mutate dependencies")
	default:
	}

	close(releaseRecovery)
	select {
	case err := <-recoveryErr:
		if err == nil || !strings.Contains(err.Error(), "ignored cancellation") ||
			!errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("startup recovery error = %v, want timeout plus cancellation-contract failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("startup recovery did not return after non-cooperative callback unwound")
	}
}

func TestServerRunWithReadyTimesOutBlockedRecoveryAndReportsTerminalFailure(t *testing.T) {
	runPath := filepath.Join(t.TempDir(), "running.json")
	srv, err := NewWithDeps(config.Config{
		Host:            "127.0.0.1",
		Port:            0,
		ShutdownTimeout: 5 * time.Second,
		RunFilePath:     runPath,
	}, discardLogger(), nil, APIDeps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.startupRecoveryAttemptTimeout = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	var missingDeadline atomic.Bool
	runErr := make(chan error, 1)
	go func() {
		runErr <- srv.RunWithReady(ctx, func(attemptCtx context.Context) error {
			attempts.Add(1)
			if _, ok := attemptCtx.Deadline(); !ok {
				missingDeadline.Store(true)
			}
			<-attemptCtx.Done()
			return attemptCtx.Err()
		})
	}()

	base := "http://" + srv.Addr().String()
	waitForHealth(t, base)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	health, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz during blocked startup recovery: %v", err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz during blocked startup recovery = %d, want 200", health.StatusCode)
	}

	var failure struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, requestErr := client.Get(base + "/readyz")
		if requestErr == nil {
			failure = struct {
				Status string `json:"status"`
				Code   string `json:"code"`
			}{}
			decodeErr := json.NewDecoder(resp.Body).Decode(&failure)
			resp.Body.Close()
			if decodeErr == nil && failure.Status == "error" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /readyz stayed in starting state: attempts=%d body=%+v err=%v", attempts.Load(), failure, requestErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("timed-out startup recovery attempts = %d, want 1 (no overlapping retry)", got)
	}
	if missingDeadline.Load() {
		t.Fatal("startup recovery callback received a context without an attempt deadline")
	}
	if failure.Code != startupRecoveryFailureCode {
		t.Fatalf("terminal readiness code = %q, want %q", failure.Code, startupRecoveryFailureCode)
	}

	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RunWithReady error = %v, want attempt deadline error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunWithReady did not return after terminal startup failure")
	}
}

func TestServerRunWithReadyShutdownStopsBlockedRecovery(t *testing.T) {
	runPath := filepath.Join(t.TempDir(), "running.json")
	srv, err := NewWithDeps(config.Config{
		Host:            "127.0.0.1",
		Port:            0,
		ShutdownTimeout: 5 * time.Second,
		RunFilePath:     runPath,
	}, discardLogger(), nil, APIDeps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.startupRecoveryAttemptTimeout = time.Hour

	recoveryStarted := make(chan struct{})
	releaseRecovery := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		runErr <- srv.RunWithReady(context.Background(), func(context.Context) error {
			close(recoveryStarted)
			<-releaseRecovery
			return nil
		})
	}()

	select {
	case <-recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("startup recovery callback was not called")
	}
	base := "http://" + srv.Addr().String()
	waitForHealth(t, base)
	resp, err := http.Post(base+"/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /shutdown during startup recovery: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /shutdown during startup recovery = %d, want 202", resp.StatusCode)
	}

	select {
	case err := <-runErr:
		t.Fatalf("RunWithReady returned before startup recovery drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseRecovery)
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("RunWithReady returned error on graceful shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunWithReady did not stop after startup recovery drained")
	}
}

func TestServerRunWithReadyFailureKeepsLivenessAndReportsTerminalReadinessError(t *testing.T) {
	runPath := filepath.Join(t.TempDir(), "running.json")
	srv, err := NewWithDeps(config.Config{
		Host:            "127.0.0.1",
		Port:            0,
		ShutdownTimeout: 5 * time.Second,
		RunFilePath:     runPath,
	}, discardLogger(), nil, APIDeps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startupErr := errors.New("session reconciliation unresolved")
	var attempts atomic.Int32
	runErr := make(chan error, 1)
	go func() {
		runErr <- srv.RunWithReady(ctx, func(context.Context) error {
			attempts.Add(1)
			return startupErr
		})
	}()

	base := "http://" + srv.Addr().String()
	waitForHealth(t, base)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	health, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz after startup failure: %v", err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz after startup failure = %d, want 200", health.StatusCode)
	}

	type readinessFailure struct {
		Status  string `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	var failure readinessFailure
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, requestErr := client.Get(base + "/readyz")
		if requestErr == nil {
			failure = readinessFailure{}
			decodeErr := json.NewDecoder(resp.Body).Decode(&failure)
			resp.Body.Close()
			if decodeErr == nil && failure.Status == "error" {
				if resp.StatusCode != http.StatusServiceUnavailable {
					t.Fatalf("GET /readyz after terminal startup failure = %d, want 503", resp.StatusCode)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /readyz did not report terminal startup failure: attempts=%d body=%+v err=%v", attempts.Load(), failure, requestErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := attempts.Load(); got != startupRecoveryMaxAttempts {
		t.Fatalf("startup recovery attempts = %d, want %d", got, startupRecoveryMaxAttempts)
	}
	if failure.Code != "startup_recovery_failed" {
		t.Fatalf("terminal readiness code = %q, want startup_recovery_failed", failure.Code)
	}
	if failure.Message != "AO could not recover existing sessions. Restart AO and check the daemon log for details." {
		t.Fatalf("terminal readiness message = %q", failure.Message)
	}

	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, startupErr) {
			t.Fatalf("RunWithReady error = %v, want startup error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunWithReady did not return after context cancel")
	}
}

func TestServerShutdownEndpoint(t *testing.T) {
	runPath := filepath.Join(t.TempDir(), "running.json")
	cfg := config.Config{
		Host:            "127.0.0.1",
		Port:            0,
		ShutdownTimeout: 5 * time.Second,
		RunFilePath:     runPath,
	}

	srv, err := NewWithDeps(cfg, discardLogger(), nil, APIDeps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run(context.Background()) }()

	base := "http://" + srv.Addr().String()
	waitForHealth(t, base)

	resp, err := http.Post(base+"/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /shutdown: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /shutdown = %d, want 202", resp.StatusCode)
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error on shutdown endpoint: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after shutdown endpoint")
	}

	if after, _ := runfile.Read(runPath); after != nil {
		t.Error("run-file still present after shutdown endpoint; want it removed")
	}
}

func waitForHealth(t *testing.T, base string) {
	t.Helper()
	// Per-request timeout so a stalled connect or hung handshake doesn't park
	// the test for the full Go test timeout; the outer deadline only bounds
	// the polling loop, not any single GET.
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become healthy within timeout")
}

// TestNewFallsBackOnPortConflict confirms that when the configured port is
// already held, the constructor binds an ephemeral port instead of failing, so
// the desktop supervisor never gets stuck on "daemon not ready".
func TestNewFallsBackOnPortConflict(t *testing.T) {
	cfg := config.Config{Host: "127.0.0.1", Port: 0, RunFilePath: filepath.Join(t.TempDir(), "r.json")}

	first, err := NewWithDeps(cfg, discardLogger(), nil, APIDeps{})
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	defer first.listen.Close()

	// Request the exact port the first server took; the second server should
	// fall back to a different, ephemeral port rather than error out.
	conflict := config.Config{Host: "127.0.0.1", Port: first.boundPort(), RunFilePath: cfg.RunFilePath}
	second, err := NewWithDeps(conflict, discardLogger(), nil, APIDeps{})
	if err != nil {
		t.Fatalf("New on an already-bound port = %v, want ephemeral fallback", err)
	}
	defer second.listen.Close()

	if second.boundPort() == first.boundPort() {
		t.Fatalf("second server bound the same port %d; want a fallback port", second.boundPort())
	}
	if second.boundPort() == 0 {
		t.Fatal("second server bound port 0; want a real fallback port")
	}
}
