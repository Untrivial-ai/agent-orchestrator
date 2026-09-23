package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// previewCapture records the request body and path the CLI hit, plus whether
// the daemon was contacted at all.
type previewCapture struct {
	body   string
	path   string
	method string
	called bool
}

// previewServer wires an httptest server expecting POST or DELETE on
// /api/v1/sessions/{id}/preview and captures what the CLI sent.
func previewServer(t *testing.T, status int, respBody string) (*httptest.Server, *previewCapture) {
	t.Helper()
	capture := &previewCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") || !strings.HasSuffix(r.URL.Path, "/preview") {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		capture.called = true
		capture.body = string(body)
		capture.path = r.URL.Path
		capture.method = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

func previewLifecycleServer(t *testing.T, status int, respBody string) (*httptest.Server, *previewCapture) {
	t.Helper()
	t.Setenv("AO_BROWSER_CAPABILITY", "preview-capability")
	capture := &previewCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sessions/aa-47/preview/server" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		capture.called = true
		capture.body = string(body)
		capture.path = r.URL.Path
		capture.method = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

func TestPreview_WithURLArg(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "aa-47")
	cfg := setConfigEnv(t)
	srv, capture := previewServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "preview", "http://localhost:5173")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/sessions/aa-47/preview" {
		t.Errorf("path = %q, want /api/v1/sessions/aa-47/preview", capture.path)
	}
	var req struct {
		Url string `json:"url"`
	}
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	if req.Url != "http://localhost:5173" {
		t.Errorf("captured url = %q, want %q", req.Url, "http://localhost:5173")
	}
}

func TestPreview_NoArgPostsEmptyURL(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "aa-47")
	cfg := setConfigEnv(t)
	srv, capture := previewServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "preview")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.body != `{"url":""}` {
		t.Errorf("captured body = %q, want %q", capture.body, `{"url":""}`)
	}
}

func TestPreviewClear_DeletesSessionPreview(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "aa-47")
	cfg := setConfigEnv(t)
	srv, capture := previewServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "preview", "clear")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", capture.method)
	}
	if capture.path != "/api/v1/sessions/aa-47/preview" {
		t.Errorf("path = %q, want /api/v1/sessions/aa-47/preview", capture.path)
	}
}

func TestPreviewStartUsesNamedConfigurationAndPrintsReadyURL(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "aa-47")
	cfg := setConfigEnv(t)
	srv, capture := previewLifecycleServer(
		t,
		http.StatusOK,
		`{"sessionId":"aa-47","state":"ready","configuration":"web","targetKind":"app","url":"http://127.0.0.1:4173/","port":4173,"logs":[]}`,
	)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "preview", "start", "web")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v1/sessions/aa-47/preview/server" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	if capture.body != `{"configuration":"web"}` {
		t.Fatalf("body = %q", capture.body)
	}
	if !strings.Contains(out, "ready web http://127.0.0.1:4173/") {
		t.Fatalf("output = %q", out)
	}
}

func TestPreviewStatusAndStopUseManagedServerRoute(t *testing.T) {
	for _, test := range []struct {
		name   string
		args   []string
		method string
	}{
		{name: "status", args: []string{"preview", "status", "--json"}, method: http.MethodGet},
		{name: "stop", args: []string{"preview", "stop", "--json"}, method: http.MethodDelete},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "aa-47")
			cfg := setConfigEnv(t)
			srv, capture := previewLifecycleServer(
				t,
				http.StatusOK,
				`{"sessionId":"aa-47","state":"stopped","logs":[]}`,
			)
			writeRunFileFor(t, cfg, srv)

			out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, test.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
			}
			if capture.method != test.method {
				t.Fatalf("method = %q, want %q", capture.method, test.method)
			}
			if !strings.Contains(out, `"state": "stopped"`) {
				t.Fatalf("JSON output = %q", out)
			}
		})
	}
}

func TestPreviewStartMissingSessionIDIsUsageError(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "preview", "start")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2; err=%v", got, err)
	}
}

func TestPreviewClear_MissingSessionIDIsUsageError(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	srv, capture := previewServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "preview", "clear")
	if err == nil {
		t.Fatal("expected usage error when AO_SESSION_ID is unset")
	}
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if capture.called {
		t.Fatal("daemon should not be contacted when AO_SESSION_ID is unset")
	}
}

func TestPreview_MissingSessionIDIsUsageError(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	srv, capture := previewServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "preview", "http://localhost:5173")
	if err == nil {
		t.Fatal("expected usage error when AO_SESSION_ID is unset")
	}
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "AO_SESSION_ID is not set") {
		t.Fatalf("error missing usage message: %v", err)
	}
	if capture.called {
		t.Fatal("daemon should not be contacted when AO_SESSION_ID is unset")
	}
}

func TestPreview_TooManyArgsIsUsageError(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "aa-47")
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "preview", "url1", "url2")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); err=%v", got, err)
	}
}

func TestPreviewClear_TooManyArgsIsUsageError(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "aa-47")
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "preview", "clear", "extra")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); err=%v", got, err)
	}
}

func TestPreview_HelpIncludesExamples(t *testing.T) {
	out, _, err := executeCLI(t, Deps{}, "preview", "--help")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Examples section present.
	if !strings.Contains(out, "EXAMPLES") && !strings.Contains(out, "Examples") {
		t.Errorf("help output missing Examples section:\n%s", out)
	}
	if !strings.Contains(out, "README.md") {
		t.Errorf("help output missing Markdown example:\n%s", out)
	}
	if !strings.Contains(out, "workspace-root-relative") {
		t.Errorf("help output missing workspace-root path guidance:\n%s", out)
	}
	if !strings.Contains(out, "ao preview start") {
		t.Errorf("help output missing managed server example:\n%s", out)
	}
}

func TestPreview_BlankSessionIDIsUsageError(t *testing.T) {
	t.Setenv("AO_SESSION_ID", " \t ")
	cfg := setConfigEnv(t)
	srv, capture := previewServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "preview")
	if err == nil {
		t.Fatal("expected usage error when AO_SESSION_ID is blank")
	}
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if capture.called {
		t.Fatal("daemon should not be contacted when AO_SESSION_ID is blank")
	}
}
