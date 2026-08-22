package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/daemonmeta"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

// healthzBody returns a handler that answers /healthz with the given service
// name and pid, mimicking the daemon's real liveness probe.
func healthzBody(service string, pid int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","service":%q,"pid":%d}`, service, pid)
	}
}

func hostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port from %q: %v", rawURL, err)
	}
	return u.Hostname(), port
}

func TestRunFileOwnerServing(t *testing.T) {
	const pid = 4242

	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    bool
	}{
		{
			name:    "matching service and pid is the live owner",
			handler: healthzBody(daemonmeta.ServiceName, pid),
			want:    true,
		},
		{
			name:    "reused pid: same port, different process pid",
			handler: healthzBody(daemonmeta.ServiceName, pid+1),
			want:    false,
		},
		{
			name:    "foreign service occupying the port",
			handler: healthzBody("some-other-service", pid),
			want:    false,
		},
		{
			name: "non-2xx response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			want: false,
		},
		{
			name: "unparseable body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("not json"))
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			host, port := hostPort(t, srv.URL)

			got := runFileOwnerServing(srv.Client(), host, &runfile.Info{PID: pid, Port: port})
			if got != tc.want {
				t.Errorf("runFileOwnerServing = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunFileOwnerServingNoListener(t *testing.T) {
	// Bind then immediately close to obtain a port nothing is listening on, so
	// the probe hits a refused connection — the leaked-run-file case.
	srv := httptest.NewServer(http.NotFoundHandler())
	host, port := hostPort(t, srv.URL)
	srv.Close()

	client := &http.Client{Timeout: time.Second}
	if runFileOwnerServing(client, host, &runfile.Info{PID: 4242, Port: port}) {
		t.Error("runFileOwnerServing on a dead port = true, want false (stale, safe to overwrite)")
	}
}

func TestRunFileOwnerServingNilOrZeroPort(t *testing.T) {
	client := &http.Client{Timeout: time.Second}
	if runFileOwnerServing(client, "127.0.0.1", nil) {
		t.Error("runFileOwnerServing(nil) = true, want false")
	}
	if runFileOwnerServing(client, "127.0.0.1", &runfile.Info{PID: 1, Port: 0}) {
		t.Error("runFileOwnerServing(port 0) = true, want false")
	}
}

func TestConventionalDataDirOwnerFindsPreLockDaemon(t *testing.T) {
	pid := os.Getpid()
	srv := httptest.NewServer(healthzBody(daemonmeta.ServiceName, pid))
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	ownerRunFile := filepath.Join(root, "running.json")
	configuredRunFile := filepath.Join(root, "dev", "running.json")
	if err := runfile.Write(ownerRunFile, runfile.Info{PID: pid, Port: port}); err != nil {
		t.Fatalf("write owner run-file: %v", err)
	}

	got, err := conventionalDataDirOwner(srv.Client(), host, dataDir, configuredRunFile)
	if err != nil {
		t.Fatalf("conventionalDataDirOwner: %v", err)
	}
	if got == nil || got.PID != pid || got.Port != port {
		t.Fatalf("owner = %+v, want pid %d port %d", got, pid, port)
	}

	got, err = conventionalDataDirOwner(srv.Client(), host, dataDir, ownerRunFile)
	if err != nil {
		t.Fatalf("same configured run-file: %v", err)
	}
	if got != nil {
		t.Fatalf("same configured run-file owner = %+v, want nil (already checked by caller)", got)
	}
}
