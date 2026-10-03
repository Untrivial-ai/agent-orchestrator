package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteHostCLIUsesLocalControlAPI(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	enabled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/mobile/") {
			requests = append(requests, r.Method+" "+r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/mobile/enable-lan-only":
			enabled = true
			_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a","endpoints":[{"kind":"lan","host":"192.168.1.10","port":3011,"secure":false}],"password":"pairing-secret"}`)
		case "/api/v1/mobile/status":
			if enabled {
				_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a","endpoints":[{"kind":"lan","host":"192.168.1.10","port":3011,"secure":false}],"password":"pairing-secret"}`)
			} else {
				_, _ = io.WriteString(w, `{"enabled":false,"hostId":"host-a"}`)
			}
		case "/api/v1/mobile/disable":
			enabled = false
			_, _ = io.WriteString(w, `{"enabled":false,"hostId":"host-a"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	for _, tc := range []struct {
		name, verb string
		requests   []string
		want       []string
		absent     string
	}{
		{"enable", "enable", []string{"POST /api/v1/mobile/enable-lan-only"}, []string{"host-a", "http://192.168.1.10:3011", "pairing-secret"}, ""},
		{"status", "status", []string{"GET /api/v1/mobile/status"}, []string{"host-a", "http://192.168.1.10:3011", "pairing-secret"}, ""},
		{"disable", "disable", []string{"POST /api/v1/mobile/disable"}, []string{"disabled"}, "pairing-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests = nil
			out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", tc.verb)
			if err != nil {
				t.Fatal(err)
			}
			if len(requests) != len(tc.requests) || strings.Join(requests, ",") != strings.Join(tc.requests, ",") {
				t.Fatalf("requests = %v, want %v", requests, tc.requests)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("output %q missing %q", out, want)
				}
			}
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Fatalf("output %q unexpectedly contains %q", out, tc.absent)
			}
		})
	}
}

func TestRemoteHostCLITunnelFromDisabledAndStatus(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	enabled, ready, failed := false, false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/mobile/") {
			requests = append(requests, r.Method+" "+r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/mobile/enable-lan-only":
			enabled = true
			_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a","password":"pairing-secret"}`)
		case "/api/v1/mobile/remote-access":
			_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a","password":"pairing-secret","endpoints":[{"kind":"lan","host":"192.168.1.10","port":3011}],"tunnel":{"supported":true,"running":true,"ready":false}}`)
		case "/api/v1/mobile/status":
			if !enabled {
				_, _ = io.WriteString(w, `{"enabled":false,"hostId":"host-a"}`)
			} else if failed {
				_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a","password":"pairing-secret","endpoints":[{"kind":"lan","host":"192.168.1.10","port":3011}],"tunnel":{"supported":true,"running":false,"ready":false,"hostname":"stale.trycloudflare.com","lastError":"connector exited"}}`)
			} else if ready {
				_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a","password":"pairing-secret","endpoints":[{"kind":"lan","host":"192.168.1.10","port":3011},{"kind":"tunnel","host":"live.trycloudflare.com","port":443,"secure":true}],"tunnel":{"supported":true,"running":true,"ready":true,"hostname":"live.trycloudflare.com"}}`)
			} else {
				_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a","password":"pairing-secret","endpoints":[{"kind":"lan","host":"192.168.1.10","port":3011}],"tunnel":{"supported":true,"running":true,"ready":false,"hostname":"unsettled.trycloudflare.com"}}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", "enable", "--tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(requests, ","); got != "GET /api/v1/mobile/status,POST /api/v1/mobile/enable-lan-only,POST /api/v1/mobile/remote-access" {
		t.Fatalf("requests = %s", got)
	}
	for _, want := range []string{"pairing-secret", "Tunnel: starting", "Cloudflare terminates TLS", "no uptime guarantee"} {
		if !strings.Contains(out, want) {
			t.Fatalf("enable output %q missing %q", out, want)
		}
	}
	if strings.Contains(out, "unsettled.trycloudflare.com") {
		t.Fatalf("enable output advertised unsettled tunnel: %q", out)
	}

	ready = true
	out, _, err = executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", "status")
	if err != nil || !strings.Contains(out, "https://live.trycloudflare.com:443") {
		t.Fatalf("ready tunnel status = %q, %v", out, err)
	}
	failed = true
	out, _, err = executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", "status")
	if err != nil || !strings.Contains(out, "Tunnel unavailable: connector exited") || strings.Contains(out, "stale.trycloudflare.com") {
		t.Fatalf("failed tunnel status = %q, %v", out, err)
	}
}

func TestRemoteHostCLITunnelOnlyUsesLoopbackRoute(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/mobile/") {
			requests = append(requests, r.Method+" "+r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"enabled":true,"loopbackOnly":true,"hostId":"host-a","password":"pairing-secret","tunnel":{"supported":true,"running":true}}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", "enable", "--tunnel-only")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(requests, ","); got != "POST /api/v1/mobile/enable-tunnel-only" {
		t.Fatalf("tunnel-only requests = %s", got)
	}
	if !strings.Contains(out, "Tunnel: starting") || !strings.Contains(out, "pairing-secret") || strings.Contains(out, "http://") {
		t.Fatalf("tunnel-only output = %q", out)
	}
}

func TestRemoteHostCLITunnelRefusesLoopbackOnlyListener(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/mobile/") {
			requests = append(requests, r.Method+" "+r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"enabled":true,"loopbackOnly":true,"hostId":"host-a"}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", "enable", "--tunnel")
	if err == nil || !strings.Contains(err.Error(), "disable") {
		t.Fatalf("wrong-mode CLI error = %v, want disable-first guidance", err)
	}
	if got := strings.Join(requests, ","); got != "GET /api/v1/mobile/status" {
		t.Fatalf("wrong-mode CLI requests = %s, want no mutation", got)
	}
}

func TestRemoteHostCLITunnelExistingListenerKeepsPassword(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/mobile/") {
			requests = append(requests, r.Method+" "+r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a","password":"existing-secret","tunnel":{"supported":false}}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", "enable", "--tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(requests, ","); got != "GET /api/v1/mobile/status,POST /api/v1/mobile/remote-access" {
		t.Fatalf("requests = %s, want no listener restart", got)
	}
	if !strings.Contains(out, "existing-secret") || !strings.Contains(out, "Tunnel unavailable: install cloudflared") {
		t.Fatalf("tunnel output = %q", out)
	}
}

func TestRemoteHostCLIRejectsArgsAndPreservesDaemonError(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	statusError := false
	tunnelError := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/mobile/") {
			requests = append(requests, r.Method+" "+r.URL.Path)
		}
		switch {
		case r.URL.Path == "/api/v1/mobile/status" && statusError:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"status failed","code":"MOBILE_STATUS","requestId":"req-456"}`)
		case r.URL.Path == "/api/v1/mobile/status" && tunnelError:
			_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a"}`)
		case r.URL.Path == "/api/v1/mobile/status":
			_, _ = io.WriteString(w, `{"enabled":false,"hostId":"host-a"}`)
		case r.URL.Path == "/api/v1/mobile/enable-lan-only":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"listener failed","code":"MOBILE_ENABLE","requestId":"req-123"}`)
		case r.URL.Path == "/api/v1/mobile/remote-access":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"tunnel failed","code":"MOBILE_REMOTE_ACCESS","requestId":"req-789"}`)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", "enable", "extra")
	if ExitCode(err) != 2 {
		t.Fatalf("extra argument exit code = %d, want 2: %v", ExitCode(err), err)
	}
	_, _, err = executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", "enable")
	if err == nil || !strings.Contains(err.Error(), "listener failed (MOBILE_ENABLE) [request req-123]") {
		t.Fatalf("daemon error = %v, want request ID and code", err)
	}
	if got := strings.Join(requests, ","); got != "POST /api/v1/mobile/enable-lan-only" {
		t.Fatalf("requests = %v, want LAN-only enable", requests)
	}
	requests = nil
	statusError = true
	_, _, err = executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", "status")
	if err == nil || !strings.Contains(err.Error(), "status failed (MOBILE_STATUS) [request req-456]") {
		t.Fatalf("status error = %v, want request ID and code", err)
	}
	if got := strings.Join(requests, ","); got != "GET /api/v1/mobile/status" {
		t.Fatalf("requests = %v, want status only", requests)
	}
	requests = nil
	statusError = false
	tunnelError = true
	_, _, err = executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "remote-host", "enable", "--tunnel")
	if err == nil || !strings.Contains(err.Error(), "tunnel failed (MOBILE_REMOTE_ACCESS) [request req-789]") {
		t.Fatalf("tunnel error = %v, want request ID and code", err)
	}
	if got := strings.Join(requests, ","); got != "GET /api/v1/mobile/status,POST /api/v1/mobile/remote-access" {
		t.Fatalf("requests = %v, want existing listener and tunnel start", requests)
	}
}

func TestRemoteHostCLIHeadlessStartupHint(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "remote-host", "enable")
	if err == nil || !strings.Contains(err.Error(), "ao daemon") {
		t.Fatalf("missing daemon error = %v, want headless startup hint", err)
	}
}
