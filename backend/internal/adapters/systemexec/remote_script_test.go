package systemexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRunInstallScriptDownloadsExecutesAndCleansUp(t *testing.T) {
	t.Parallel()
	const body = "#!/bin/sh\nprintf 'vendor-script-ran'"
	server := httptest.NewTLSServer(httpHandler(body))
	t.Cleanup(server.Close)
	dataDir := t.TempDir()
	adapter := newAdapter(dataDir, server.Client())
	var output bytes.Buffer

	result, err := adapter.RunInstallScript(context.Background(), ports.InstallScriptCommand{
		URL: server.URL, Interpreter: []string{"sh"}, Env: []string{"CI=1"},
	}, &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "vendor-script-ran" {
		t.Fatalf("output = %q", output.String())
	}
	sum := sha256.Sum256([]byte(body))
	if result.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %q", result.SHA256)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "installers", "tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary scripts remain: %v, %v", entries, err)
	}
}

func TestRunInstallScriptCleansUpAfterExecutionFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(httpHandler("#!/bin/sh\nexit 7"))
	t.Cleanup(server.Close)
	dataDir := t.TempDir()

	_, err := newAdapter(dataDir, server.Client()).RunInstallScript(context.Background(), ports.InstallScriptCommand{
		URL: server.URL, Interpreter: []string{"sh"},
	}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected execution failure")
	}
	entries, readErr := os.ReadDir(filepath.Join(dataDir, "installers", "tmp"))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("temporary scripts remain after failure: %v, %v", entries, readErr)
	}
}

func TestRunInstallScriptCancellationCleansUp(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(httpHandler("#!/bin/sh\nsleep 5"))
	t.Cleanup(server.Close)
	dataDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := newAdapter(dataDir, server.Client()).RunInstallScript(ctx, ports.InstallScriptCommand{
		URL: server.URL, Interpreter: []string{"sh"},
	}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected cancellation")
	}
	entries, readErr := os.ReadDir(filepath.Join(dataDir, "installers", "tmp"))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("temporary scripts remain after cancellation: %v, %v", entries, readErr)
	}
}

func TestRunInstallScriptAllowsFiveHTTPSRedirects(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step, _ := strconv.Atoi(r.URL.Query().Get("step"))
		if step < 5 {
			http.Redirect(w, r, "/?step="+strconv.Itoa(step+1), http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "#!/bin/sh\nprintf redirected")
	}))
	t.Cleanup(server.Close)
	var output bytes.Buffer

	_, err := newAdapter(t.TempDir(), server.Client()).RunInstallScript(context.Background(), ports.InstallScriptCommand{
		URL: server.URL + "/?step=0", Interpreter: []string{"sh"},
	}, &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "redirected" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunInstallScriptRejectsTooManyOrInsecureRedirects(t *testing.T) {
	t.Parallel()
	t.Run("more than five redirects", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			step, _ := strconv.Atoi(r.URL.Query().Get("step"))
			http.Redirect(w, r, "/?step="+strconv.Itoa(step+1), http.StatusFound)
		}))
		t.Cleanup(server.Close)
		_, err := newAdapter(t.TempDir(), server.Client()).RunInstallScript(context.Background(), ports.InstallScriptCommand{
			URL: server.URL + "/?step=0", Interpreter: []string{"sh"},
		}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "redirect limit") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("HTTPS redirect to HTTP", func(t *testing.T) {
		plain := httptest.NewServer(httpHandler("#!/bin/sh\nexit 0"))
		t.Cleanup(plain.Close)
		secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL, http.StatusFound)
		}))
		t.Cleanup(secure.Close)
		_, err := newAdapter(t.TempDir(), secure.Client()).RunInstallScript(context.Background(), ports.InstallScriptCommand{
			URL: secure.URL, Interpreter: []string{"sh"},
		}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "redirect must remain HTTPS") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRunInstallScriptRejectsUnsafeDownloads(t *testing.T) {
	t.Parallel()
	t.Run("plain HTTP", func(t *testing.T) {
		server := httptest.NewServer(httpHandler("#!/bin/sh\nexit 0"))
		t.Cleanup(server.Close)
		_, err := newAdapter(t.TempDir(), server.Client()).RunInstallScript(context.Background(), ports.InstallScriptCommand{
			URL: server.URL, Interpreter: []string{"sh"},
		}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "absolute HTTPS") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("oversized response", func(t *testing.T) {
		server := httptest.NewTLSServer(httpHandler(strings.Repeat("x", remoteScriptMaxBytes+1)))
		t.Cleanup(server.Close)
		_, err := newAdapter(t.TempDir(), server.Client()).RunInstallScript(context.Background(), ports.InstallScriptCommand{
			URL: server.URL, Interpreter: []string{"sh"},
		}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "download limit") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("non-success status", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no", http.StatusBadGateway)
		}))
		t.Cleanup(server.Close)
		_, err := newAdapter(t.TempDir(), server.Client()).RunInstallScript(context.Background(), ports.InstallScriptCommand{
			URL: server.URL, Interpreter: []string{"sh"},
		}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "502 Bad Gateway") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestNewUsesAODataDir(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	if got, want := New(dataDir).installerRoot, filepath.Join(dataDir, "installers", "tmp"); got != want {
		t.Fatalf("installerRoot = %q, want %q", got, want)
	}
}

func httpHandler(body string) *staticHTTPHandler {
	return &staticHTTPHandler{body: body}
}

type staticHTTPHandler struct {
	body string
}

func (h *staticHTTPHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(w, h.body)
}
