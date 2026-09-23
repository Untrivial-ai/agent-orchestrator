package mobilebridge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const serveStatusJSON = `{
  "TCP": {"443": {"HTTPS": true}},
  "Web": {"prasads-macbook-pro.tail057d04.ts.net:443": {
    "Handlers": {"/": {"Proxy": "http://127.0.0.1:54014"}}}}
}`

type recordingRunner struct {
	calls [][]string
	out   string
	err   error
}

func (r *recordingRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	return []byte(r.out), r.err
}

func TestServeApplyIssuesExpectedArgv(t *testing.T) {
	r := &recordingRunner{}
	s := &Serve{Run: r.run}
	if err := s.Apply(context.Background(), 54014); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(r.calls))
	}
	got := strings.Join(r.calls[0], " ")
	want := "serve --bg --https=443 http://127.0.0.1:54014"
	if got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestServeClearIssuesOff(t *testing.T) {
	r := &recordingRunner{}
	s := &Serve{Run: r.run}
	if err := s.Clear(context.Background()); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	want := "serve --https=443 off"
	if got := strings.Join(r.calls[0], " "); got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

// serveStatusMultiSiteJSON simulates a tailnet where the user also runs an
// unrelated `tailscale serve --https=8443 ...` alongside AO's :443 proxy.
// Target must key off the ":443" suffix, not just "some Web entry", or Go's
// randomized map iteration could return the unrelated handler's port instead.
const serveStatusMultiSiteJSON = `{
  "TCP": {"443": {"HTTPS": true}, "8443": {"HTTPS": true}},
  "Web": {
    "prasads-macbook-pro.tail057d04.ts.net:443": {
      "Handlers": {"/": {"Proxy": "http://127.0.0.1:54014"}}},
    "prasads-macbook-pro.tail057d04.ts.net:8443": {
      "Handlers": {"/": {"Proxy": "http://127.0.0.1:9999"}}}
  }
}`

func TestServeTargetIgnoresNon443Handlers(t *testing.T) {
	r := &recordingRunner{out: serveStatusMultiSiteJSON}
	s := &Serve{Run: r.run}
	if got := s.Target(context.Background()); got != 54014 {
		t.Errorf("Target = %d, want 54014 (must ignore the :8443 handler)", got)
	}
}

func TestServeTargetParsesProxyPort(t *testing.T) {
	r := &recordingRunner{out: serveStatusJSON}
	s := &Serve{Run: r.run}
	if got := s.Target(context.Background()); got != 54014 {
		t.Errorf("Target = %d, want 54014", got)
	}
}

// An unset proxy, a failed call, and unparseable output are all "no target",
// which the caller renders as "proxy not active" rather than as an error.
func TestServeTargetZeroWhenUnset(t *testing.T) {
	for name, r := range map[string]*recordingRunner{
		"empty config": {out: `{"TCP":{},"Web":{}}`},
		"cli error":    {out: "", err: errors.New("exit status 1")},
		"malformed":    {out: "not json"},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Serve{Run: r.run}
			if got := s.Target(context.Background()); got != 0 {
				t.Errorf("Target = %d, want 0", got)
			}
		})
	}
}

// Apply must surface CLI failure so the caller can report serve_failed rather
// than advertising a proxy that was never created.
func TestServeApplyPropagatesError(t *testing.T) {
	r := &recordingRunner{err: errors.New("exit status 1")}
	s := &Serve{Run: r.run}
	if err := s.Apply(context.Background(), 3011); err == nil {
		t.Error("Apply returned nil, want the CLI error")
	}
}
