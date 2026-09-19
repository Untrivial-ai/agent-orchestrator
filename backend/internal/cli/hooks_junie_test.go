package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJunieSessionStartOnlyReportsNativeIdentity(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-junie")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)
	stdout, _, err := executeCLI(t, Deps{In: strings.NewReader(`{"session_id":"junie-native","source":"startup"}`), ProcessAlive: func(int) bool { return true }}, "hooks", "junie", "session-start")
	if err != nil || stdout != "" {
		t.Fatalf("hook produced decision output %q, err %v", stdout, err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatal(err)
	}
	assertActivityRequest(t, req, setActivityAPIRequest{Event: "session-start", AgentSessionID: "junie-native"})
}

func TestJunieHookRetriesBusyProjectionWithinBudget(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-junie")
	cfg := setConfigEnv(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"code":"ACTIVITY_PROJECTION_BUSY","message":"retry","requestId":"junie-retry"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	stdout, stderr, err := executeCLI(t, Deps{In: strings.NewReader(`{"session_id":"native"}`), ProcessAlive: func(int) bool { return true }}, "hooks", "junie", "session-start")
	if err != nil || calls.Load() != 4 || stdout != "" || stderr != "" {
		t.Fatalf("busy retry: calls=%d stdout=%q stderr=%q err=%v", calls.Load(), stdout, stderr, err)
	}
}

func TestJunieMalformedIdentityDoesNotReachDaemon(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-junie")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)
	for _, payload := range []string{
		`{"session_id":"native",`, `{"session_id":42}`,
		`{"session_id":"native\u0000id"}`, `{"session_id":"-native"}`,
		`{"session_id":" native "}`, `{"session_id":"native\n"}`,
	} {
		_, _, err := executeCLI(t, Deps{In: strings.NewReader(payload), ProcessAlive: func(int) bool { return true }}, "hooks", "junie", "session-start")
		if err != nil {
			t.Fatal(err)
		}
	}
	if capture.hits != 0 {
		t.Fatalf("invalid native identity was posted: %s", capture.body)
	}
}

func TestJunieReviewContextCannotBypassIdentityValidation(t *testing.T) {
	// Junie is not a reviewer. Even a manually invoked callback with a review
	// context must not persist an identity that normalization would change.
	t.Setenv("AO_REVIEW_SESSION_ID", "review-junie")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)
	_, _, err := executeCLI(t, Deps{In: strings.NewReader(`{"session_id":"native\u0000id"}`), ProcessAlive: func(int) bool { return true }}, "hooks", "junie", "session-start")
	if err != nil {
		t.Fatal(err)
	}
	if capture.hits != 0 {
		t.Fatalf("invalid identity posted via review context: %s", capture.body)
	}
}

func TestJunieHookDeliveryHasBoundedBudget(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-junie")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)
	bounded := false
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/activity") {
			deadline, ok := req.Context().Deadline()
			bounded = ok && time.Until(deadline) <= 5*time.Second
		}
		return http.DefaultTransport.RoundTrip(req)
	})}
	stdout, _, err := executeCLI(t, Deps{HTTPClient: client, In: strings.NewReader(`{"session_id":"native"}`), ProcessAlive: func(int) bool { return true }}, "hooks", "junie", "stop")
	if err != nil || stdout != "" || capture.hits != 1 || !bounded {
		t.Fatalf("delivery: hits=%d bounded=%v output=%q err=%v", capture.hits, bounded, stdout, err)
	}
}
