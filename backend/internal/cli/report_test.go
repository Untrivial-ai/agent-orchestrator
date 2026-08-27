package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReportValidation(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "worker-1")
	long := strings.Repeat("x", 1001)
	tests := []struct {
		name string
		args []string
	}{
		{"empty", []string{"report"}},
		{"structured missing note", []string{"report", "--done"}},
		{"mutually exclusive", []string{"report", "--done", "--stuck", "--note", "x"}},
		{"free form with note", []string{"report", "hello", "--note", "x"}},
		{"free form with empty note flag", []string{"report", "hello", "--note="}},
		{"free form with flag", []string{"report", "hello", "--done", "--note", "x"}},
		{"free form too long", []string{"report", long}},
		{"note too long", []string{"report", "--checkpoint", "--note", long}},
		{"invalid pr scheme", []string{"report", "--pr-created", "--note", "git://github.com/o/r/pull/1"}},
		{"invalid pr host", []string{"report", "--pr-created", "--note", "https://example.com/o/r/pull/1"}},
		{"invalid pr path", []string{"report", "--pr-created", "--note", "https://github.com/o/r/issues/1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := executeCLI(t, Deps{}, tc.args...)
			if err == nil || ExitCode(err) != 2 {
				t.Fatalf("err=%v exit=%d", err, ExitCode(err))
			}
		})
	}
}

func TestReportModesAndBoundaries(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "worker-1")
	tests := []struct {
		name      string
		args      []string
		typ, note string
	}{
		{"free form", []string{"report", "hello", "world"}, "free_form", "hello world"},
		{"free form boundary", []string{"report", strings.Repeat("界", 1000)}, "free_form", strings.Repeat("界", 1000)},
		{"pr", []string{"report", "--pr-created", "--note", "https://github.com/o/r/pull/12"}, "pr_created", "https://github.com/o/r/pull/12"},
		{"artifact", []string{"report", "--artifact", "--note", "opaque://anything"}, "artifact", "opaque://anything"},
		{"checkpoint", []string{"report", "--checkpoint", "--note", "x"}, "checkpoint", "x"},
		{"needs input", []string{"report", "--needs-input", "--note", "x"}, "needs_input", "x"},
		{"stuck", []string{"report", "--stuck", "--note", "x"}, "stuck", "x"},
		{"done", []string{"report", "--done", "--note", strings.Repeat("x", 1000)}, "done", strings.Repeat("x", 1000)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			var got reportAPIRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/reports" {
					_ = json.NewDecoder(r.Body).Decode(&got)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"id":"rpt_1"}`)
			}))
			defer srv.Close()
			writeRunFileFor(t, cfg, srv)
			_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if got.SessionID != "worker-1" || got.Type != tc.typ || got.Note != tc.note {
				t.Fatalf("request=%+v", got)
			}
		})
	}
}

func TestReportPreservesAPIEnvelopeAsRuntimeError(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "worker-1")
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"bad report","code":"INVALID_REPORT","requestId":"req-7"}`)
	}))
	defer srv.Close()
	writeRunFileFor(t, cfg, srv)
	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "report", "hello")
	if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), "bad report (INVALID_REPORT) [request req-7]") {
		t.Fatalf("err=%v", err)
	}
}
