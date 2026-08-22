package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAPIErrorString covers how the CLI renders the daemon's error envelope,
// including the requestId it now surfaces for log correlation.
func TestAPIErrorString(t *testing.T) {
	cases := []struct {
		name string
		in   apiError
		want string
	}{
		{"message only", apiError{Message: "boom"}, "boom"},
		{"message and code", apiError{Message: "boom", Code: "X"}, "boom (X)"},
		{"with request id", apiError{Message: "boom", Code: "X", RequestID: "req-1"}, "boom (X) [request req-1]"},
		{"message and request id", apiError{Message: "boom", RequestID: "req-1"}, "boom [request req-1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCloudCoordinatorURLBypassesLocalRunFile(t *testing.T) {
	var path string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"sessionId":"worker-1","message":"hello"}`)
	}))
	defer server.Close()
	t.Setenv("AO_CLOUD_COORDINATOR_URL", server.URL)
	t.Setenv("AO_SESSION_ID", "")
	setConfigEnv(t)
	_, stderr, err := executeCLI(t, Deps{HTTPClient: server.Client(), ProcessAlive: func(int) bool { return false }}, "send", "--session", "worker-1", "--message", "hello")
	if err != nil {
		t.Fatalf("send through coordinator: %v stderr=%s", err, stderr)
	}
	if path != "/api/v1/sessions/worker-1/send" {
		t.Fatalf("path = %q", path)
	}
}

func TestCloudCoordinatorURLRequiresHTTPS(t *testing.T) {
	t.Setenv("AO_CLOUD_COORDINATOR_URL", "http://cloud.example")
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "send", "--session", "worker-1", "--message", "hello")
	if err == nil {
		t.Fatal("insecure coordinator URL accepted")
	}
}
