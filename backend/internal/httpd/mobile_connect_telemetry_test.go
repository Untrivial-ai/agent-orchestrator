package httpd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type capturingSink struct {
	mu     sync.Mutex
	events []ports.TelemetryEvent
}

func (s *capturingSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *capturingSink) Close(context.Context) error { return nil }

func (s *capturingSink) all() []ports.TelemetryEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ports.TelemetryEvent(nil), s.events...)
}

func TestTransportForSource(t *testing.T) {
	cases := map[string]string{
		"192.168.1.20": "lan",
		"10.0.0.5":     "lan",
		"172.16.4.4":   "lan",
		"fe80::1":      "lan",
		// Tailscale's CGNAT range must not be reported as an ordinary LAN
		// address: which transport works is the reason this dimension exists.
		"100.64.0.1":   "tailscale",
		"100.101.5.20": "tailscale",
		"127.0.0.1":    "loopback",
		"::1":          "loopback",
		"8.8.8.8":      "other",
		"":             "other",
		"not-an-ip":    "other",
	}
	for host, want := range cases {
		if got := transportForSource(host); got != want {
			t.Errorf("transportForSource(%q) = %q, want %q", host, got, want)
		}
	}
}

// 100.64.0.0/10 ends at 100.127.255.255. An address just past it is a public
// address that happens to start with 100, and must not be called tailscale.
func TestTransportForSourceCGNATBoundary(t *testing.T) {
	if got := transportForSource("100.127.255.255"); got != "tailscale" {
		t.Errorf("last CGNAT address = %q, want tailscale", got)
	}
	if got := transportForSource("100.128.0.1"); got != "other" {
		t.Errorf("first address past CGNAT = %q, want other", got)
	}
	if got := transportForSource("100.63.255.255"); got != "other" {
		t.Errorf("address below CGNAT = %q, want other", got)
	}
}

// The reason this type exists. Every request from a paired phone authenticates,
// including a preview page's images and stylesheets, so a per-request emit would
// be thousands of events per minute.
func TestReporterEmitsOncePerWindow(t *testing.T) {
	sink := &capturingSink{}
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	r := newMobileConnectReporter(sink, func() time.Time { return now })

	for range 500 {
		r.report("192.168.1.20")
	}
	if got := len(sink.all()); got != 1 {
		t.Fatalf("500 authenticated requests emitted %d events, want 1", got)
	}

	ev := sink.all()[0]
	if ev.Name != mobileDeviceConnectedEvent {
		t.Errorf("name = %q, want %q", ev.Name, mobileDeviceConnectedEvent)
	}
	if ev.Payload["transport"] != "lan" {
		t.Errorf("transport = %v, want lan", ev.Payload["transport"])
	}
	// No address, no port, no password may reach the payload.
	if len(ev.Payload) != 1 {
		t.Errorf("payload = %v, want transport only", ev.Payload)
	}
}

func TestReporterEmitsAgainAfterWindow(t *testing.T) {
	sink := &capturingSink{}
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	r := newMobileConnectReporter(sink, func() time.Time { return now })

	r.report("192.168.1.20")
	now = now.Add(connectedReportWindow - time.Second)
	r.report("192.168.1.20")
	if got := len(sink.all()); got != 1 {
		t.Fatalf("inside the window emitted %d events, want 1", got)
	}
	now = now.Add(2 * time.Second)
	r.report("192.168.1.20")
	if got := len(sink.all()); got != 2 {
		t.Fatalf("after the window emitted %d events, want 2", got)
	}
}

// Dedup is per transport so switching from LAN to Tailscale is still visible,
// while staying bounded: the vocabulary is closed, so the map cannot grow with
// the number of phones or addresses seen.
func TestReporterDedupesPerTransport(t *testing.T) {
	sink := &capturingSink{}
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	r := newMobileConnectReporter(sink, func() time.Time { return now })

	r.report("192.168.1.20")
	r.report("192.168.1.55") // different phone, same transport
	r.report("100.64.0.9")

	got := []string{}
	for _, ev := range sink.all() {
		got = append(got, ev.Payload["transport"].(string))
	}
	if len(got) != 2 || got[0] != "lan" || got[1] != "tailscale" {
		t.Fatalf("transports = %v, want [lan tailscale]", got)
	}
}

// A nil sink is the telemetry-disabled path, and callers must not have to guard.
func TestNilReporterIsInert(t *testing.T) {
	var r *mobileConnectReporter
	r.report("192.168.1.20") // must not panic
	if newMobileConnectReporter(nil, nil) != nil {
		t.Error("newMobileConnectReporter(nil, nil) should be nil so report() is a no-op")
	}
}

func TestReporterIsConcurrencySafe(t *testing.T) {
	sink := &capturingSink{}
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	r := newMobileConnectReporter(sink, func() time.Time { return now })

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.report("192.168.1.20")
		}()
	}
	wg.Wait()
	if got := len(sink.all()); got != 1 {
		t.Fatalf("concurrent reports emitted %d events, want 1", got)
	}
}

// End-to-end through the real listener: an authenticated request reports a
// connection, an unauthenticated one does not. This is the path a phone takes,
// so it is what proves the wiring rather than the reporter in isolation.
func TestLANListenerReportsOnlyAuthenticatedRequests(t *testing.T) {
	sink := &capturingSink{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(inner, st, LANManagerConfig{DefaultPort: 0}, slog.Default(), sink)
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())

	url := fmt.Sprintf("http://127.0.0.1:%d/api/v1/sessions", port)

	// A wrong password must not look like a connected phone.
	bad, _ := http.NewRequest(http.MethodGet, url, nil)
	bad.Header.Set("Authorization", "Bearer wrongpass")
	if resp, err := http.DefaultClient.Do(bad); err == nil {
		resp.Body.Close()
	}
	if got := len(sink.all()); got != 0 {
		t.Fatalf("failed auth emitted %d events, want 0", got)
	}

	// The pairing handshake: the phone's pingServer call, GET /api/v1/sessions
	// with the connection password as a bearer token.
	ok, _ := http.NewRequest(http.MethodGet, url, nil)
	ok.Header.Set("Authorization", "Bearer secret12")
	resp, err := http.DefaultClient.Do(ok)
	if err != nil {
		t.Fatalf("authenticated request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated request: got %d want 200", resp.StatusCode)
	}

	events := sink.all()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Name != mobileDeviceConnectedEvent {
		t.Errorf("name = %q, want %q", events[0].Name, mobileDeviceConnectedEvent)
	}
	// The request came from 127.0.0.1, so loopback is the honest classification.
	if events[0].Payload["transport"] != "loopback" {
		t.Errorf("transport = %v, want loopback", events[0].Payload["transport"])
	}
}
