package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type accountTelemetrySink struct{ events chan ports.TelemetryEvent }

func (s accountTelemetrySink) Emit(_ context.Context, ev ports.TelemetryEvent) { s.events <- ev }
func (s accountTelemetrySink) Close(context.Context) error                     { return nil }

func TestGitHubAccountTelemetryGatesLookup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*config.TelemetryConfig)
	}{
		{"events off", func(c *config.TelemetryConfig) { c.Events = false }},
		{"remote off", func(c *config.TelemetryConfig) { c.Remote = config.TelemetryRemoteOff }},
		{"missing key", func(c *config.TelemetryConfig) { c.PostHogKey = " " }},
		{"missing host", func(c *config.TelemetryConfig) { c.PostHogHost = "" }},
		{"denied event", func(c *config.TelemetryConfig) { c.DisabledEvents = []string{" AO.GITHUB.ACCOUNT_OBSERVED "} }},
		{"denied prefix", func(c *config.TelemetryConfig) { c.DisabledEvents = []string{"ao.github.*"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.TelemetryConfig{Events: true, Remote: config.TelemetryRemotePostHog, PostHogKey: "phc_test", PostHogHost: "https://example.test"}
			tc.modify(&cfg)
			stop := startGitHubAccountTelemetry(t.Context(), cfg, nil, func(context.Context) (ports.SCMIdentity, error) {
				t.Error("disabled telemetry must not resolve an account")
				return ports.SCMIdentity{}, nil
			})
			stop()
		})
	}
}

func TestGitHubAccountTelemetryRefreshesWithoutStaleIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ticks := make(chan time.Time)
	sink := accountTelemetrySink{events: make(chan ports.TelemetryEvent, 10)}
	observations := []struct {
		identity ports.SCMIdentity
		err      error
	}{
		{ports.SCMIdentity{Login: " octocat ", Human: true}, nil},
		{ports.SCMIdentity{Login: "stale", Human: true}, errors.New("lookup failed")},
		{ports.SCMIdentity{Login: "robot", Human: false}, nil},
		{ports.SCMIdentity{Login: "alice@example.com", Human: true}, nil},
		{ports.SCMIdentity{Login: "", Human: true}, nil},
		{ports.SCMIdentity{Login: strings.Repeat("a", 40), Human: true}, nil},
		{ports.SCMIdentity{Login: "new-account", Human: true}, nil},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		i := 0
		runGitHubAccountTelemetry(ctx, sink, func(ctx context.Context) (ports.SCMIdentity, error) {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
				t.Error("lookup must have a bounded deadline")
			}
			observation := observations[i]
			i++
			return observation.identity, observation.err
		}, ticks)
	}()
	for i := 1; i < len(observations); i++ {
		select {
		case ticks <- time.Now():
		case <-time.After(2 * time.Second):
			t.Fatal("observation did not finish")
		}
	}
	for _, login := range []string{"octocat", "new-account"} {
		select {
		case ev := <-sink.events:
			if ev.Name != githubAccountEvent || ev.Payload["github_login"] != login || len(ev.Payload) != 1 || ev.OccurredAt.IsZero() {
				t.Fatalf("unexpected account observation: %#v", ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("missing account observation")
		}
	}
	cancel()
	<-done
	if len(sink.events) != 0 {
		t.Fatal("failed, bot, or invalid identity was recorded")
	}
}

func TestGitHubAccountTelemetryStopsInFlightLookup(t *testing.T) {
	started := make(chan struct{})
	sink := accountTelemetrySink{events: make(chan ports.TelemetryEvent, 1)}
	cfg := config.TelemetryConfig{Events: true, Remote: config.TelemetryRemotePostHog, PostHogKey: "phc_test", PostHogHost: "https://example.test"}
	stop := startGitHubAccountTelemetry(t.Context(), cfg, sink, func(ctx context.Context) (ports.SCMIdentity, error) {
		close(started)
		<-ctx.Done()
		return ports.SCMIdentity{Login: "octocat", Human: true}, nil
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("lookup did not start")
	}
	stop()
	if len(sink.events) != 0 {
		t.Fatal("cancelled lookup emitted an identity")
	}
}
