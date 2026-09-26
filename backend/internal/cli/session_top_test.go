package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sessionTopServer(t *testing.T, memoryStatus int, memoryBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions":
			_, _ = io.WriteString(w, `{"sessions":[`+
				sessionJSON("demo-1", "demo", "worker", "working", false)+`,`+
				sessionJSON("demo-2", "demo", "worker", "idle", false)+`,`+
				sessionJSON("demo-3", "demo", "worker", "idle", false)+`]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/usage/sessions/memory":
			w.WriteHeader(memoryStatus)
			_, _ = io.WriteString(w, memoryBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const topMemoryBody = `{"sessions":[
	{"sessionId":"demo-1","rssBytes":641728512,"processCount":5,"sampledAt":"2026-09-18T00:00:00Z","processes":[]},
	{"sessionId":"demo-2","rssBytes":2254857830,"processCount":9,"sampledAt":"2026-09-18T00:00:00Z","processes":[]}]}`

func TestSessionTop_SortsByMemoryAndDashesUnsampled(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := sessionTopServer(t, http.StatusOK, topMemoryBody)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "top")
	if err != nil {
		t.Fatalf("session top failed: %v\nstderr=%s", err, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected header, 3 rows and total, got:\n%s", out)
	}
	if !strings.HasPrefix(lines[1], "demo-2") || !strings.Contains(lines[1], "2.1 GB") || !strings.Contains(lines[1], "9") {
		t.Fatalf("largest session should lead:\n%s", out)
	}
	if !strings.HasPrefix(lines[2], "demo-1") || !strings.Contains(lines[2], "612.0 MB") {
		t.Fatalf("second row wrong:\n%s", out)
	}
	if !strings.HasPrefix(lines[3], "demo-3") || !strings.Contains(lines[3], "  -  ") {
		t.Fatalf("unsampled session should show dashes, not zero:\n%s", out)
	}
	if !strings.HasPrefix(lines[4], "TOTAL") || !strings.Contains(lines[4], "2.7 GB") {
		t.Fatalf("total row wrong:\n%s", out)
	}
}

func TestSessionTop_JSONIncludesTotal(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := sessionTopServer(t, http.StatusOK, topMemoryBody)
	writeRunFileFor(t, cfg, srv)

	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "top", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"totalRssBytes": 2896586342`) {
		t.Fatalf("json missing total:\n%s", out)
	}
}

func TestSessionTop_SurfacesDaemonError(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := sessionTopServer(t, http.StatusNotImplemented, `{"error":"not_implemented","code":"MEMORY_UNSUPPORTED","message":"procmem: process memory is not supported on windows"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "top")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(errOut+err.Error(), "MEMORY_UNSUPPORTED") && !strings.Contains(errOut+err.Error(), "not supported") {
		t.Fatalf("error should carry daemon envelope: %v\n%s", err, errOut)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[uint64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 641728512: "612.0 MB", 2254857830: "2.1 GB"}
	for in, want := range cases {
		if got := formatBytes(in); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
