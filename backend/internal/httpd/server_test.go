package httpd

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
