package daemon

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
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
			stop := startGitHubAccountTelemetry(t.Context(), cfg, nil, func() bool { return cfg.Events }, func(context.Context) (ports.SCMIdentity, error) {
				t.Error("disabled telemetry must not resolve an account")
				return ports.SCMIdentity{}, nil
			}, accountAuthority{})
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
		runGitHubAccountTelemetry(ctx, sink, func() bool { return true }, func(ctx context.Context) (ports.SCMIdentity, error) {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
				t.Error("lookup must have a bounded deadline")
			}
			observation := observations[i]
			i++
			return observation.identity, observation.err
		}, accountAuthority{}, ticks)
	}()
	for i := 1; i < len(observations); i++ {
		select {
		case ticks <- time.Now().Add(time.Minute + time.Duration(i)*24*time.Hour):
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
	stop := startGitHubAccountTelemetry(t.Context(), cfg, sink, func() bool { return cfg.Events }, func(ctx context.Context) (ports.SCMIdentity, error) {
		close(started)
		<-ctx.Done()
		return ports.SCMIdentity{Login: "octocat", Human: true}, nil
	}, accountAuthority{})
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

// The happy-path fixture stands for a validated durable v3 affirmative grant.
type accountAuthority struct{}

func (accountAuthority) ReadAgentSwitchFailureAuthority(context.Context) (ports.AgentSwitchFailureAuthoritySnapshot, error) {
	return ports.AgentSwitchFailureAuthoritySnapshot{Present: true, EventsEnabled: true, ConsentIdentityEnabled: true, ConsentGeneration: "grant"}, nil
}

type identityAuthorityFunc func(context.Context) (ports.AgentSwitchFailureAuthoritySnapshot, error)

func (f identityAuthorityFunc) ReadAgentSwitchFailureAuthority(ctx context.Context) (ports.AgentSwitchFailureAuthoritySnapshot, error) {
	return f(ctx)
}

func TestIdentityRequiresCurrentConsentBeforeLookupAndCapture(t *testing.T) {
	grant, _ := (accountAuthority{}).ReadAgentSwitchFailureAuthority(t.Context())
	for _, scenario := range []string{"missing", "legacy", "revoked", "invalid", "revoked during lookup", "changed generation", "invalid after lookup", "granted"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ticks := make(chan time.Time)
			sink := accountTelemetrySink{events: make(chan ports.TelemetryEvent, 1)}
			calls, reads := 0, 0
			reader := identityAuthorityFunc(func(context.Context) (ports.AgentSwitchFailureAuthoritySnapshot, error) {
				reads++
				if reads > 2 {
					cancel()
					return grant, context.Canceled
				}
				value := grant
				switch scenario {
				case "missing":
					value.Present = false
				case "legacy":
					value.ConsentIdentityEnabled = false
				case "revoked":
					value.EventsEnabled = false
				case "invalid":
					return value, errors.New("invalid authority")
				case "revoked during lookup":
					if reads > 1 {
						value.ConsentIdentityEnabled = false
					}
				case "changed generation":
					if reads > 1 {
						value.ConsentGeneration = "different"
					}
				case "invalid after lookup":
					if reads > 1 {
						return value, errors.New("invalid authority")
					}
				}
				return value, nil
			})
			done := make(chan struct{})
			go func() {
				defer close(done)
				runGitHubAccountTelemetry(ctx, sink, func() bool { return true }, func(context.Context) (ports.SCMIdentity, error) {
					calls++
					return ports.SCMIdentity{Login: "octocat", Human: true}, nil
				}, reader, ticks)
			}()
			// An unbuffered tick cannot be received until this observation completed.
			// Cancel in the authority reader on the next iteration to avoid another lookup.
			select {
			case ticks <- time.Now():
				cancel()
			case <-time.After(2 * time.Second):
				t.Fatal("observation did not complete")
			}
			<-done
			if scenario == "missing" || scenario == "legacy" || scenario == "revoked" || scenario == "invalid" {
				if calls != 0 {
					t.Fatalf("lookup occurred without consent: %d", calls)
				}
			}
			want := 0
			if scenario == "granted" {
				want = 1
			}
			if len(sink.events) != want {
				t.Fatalf("captured %d events, want %d", len(sink.events), want)
			}
		})
	}
}

func TestIdentityConsentRenewedWithoutRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	checked := make(chan struct{}, 4)
	grant := make(chan struct{})
	authority := identityAuthorityFunc(func(context.Context) (ports.AgentSwitchFailureAuthoritySnapshot, error) {
		snapshot, err := (accountAuthority{}).ReadAgentSwitchFailureAuthority(ctx)
		select {
		case <-grant:
		default:
			snapshot.ConsentIdentityEnabled = false
		}
		checked <- struct{}{}
		return snapshot, err
	})
	sink := &accountTelemetrySink{events: make(chan ports.TelemetryEvent, 2)}
	calls := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		runGitHubAccountTelemetry(ctx, sink, func() bool { return true }, func(context.Context) (ports.SCMIdentity, error) {
			calls++
			return ports.SCMIdentity{Login: "octocat", Human: true}, nil
		}, authority, ticks)
	}()
	<-checked // Startup does not grant consent.
	close(grant)
	ticks <- time.Now()
	<-checked     // Before lookup.
	<-checked     // Before capture.
	<-sink.events // Capture completed.
	cancel()
	<-done
	if calls != 1 {
		t.Fatal("renewed consent was not picked up without restart")
	}
}

func TestGitHubAccountTelemetryRefreshInterval(t *testing.T) {
	if githubAccountRefreshInterval != 24*time.Hour {
		t.Fatalf("refresh interval = %s, want daily", githubAccountRefreshInterval)
	}
}

func TestGitHubAccountTelemetryDailyAttemptsAndPromptRenewal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ticks := make(chan time.Time)
	done := make(chan struct{})
	var calls atomic.Int32
	var enabled atomic.Bool
	enabled.Store(true)
	go func() {
		defer close(done)
		runGitHubAccountTelemetry(ctx, accountTelemetrySink{events: make(chan ports.TelemetryEvent, 4)}, enabled.Load, func(context.Context) (ports.SCMIdentity, error) {
			calls.Add(1)
			return ports.SCMIdentity{}, errors.New("offline") // Failed attempts are daily too.
		}, accountAuthority{}, ticks)
	}()
	base := time.Now().Add(time.Minute)
	ticks <- base // Startup attempt has completed.
	if calls.Load() != 1 {
		t.Fatalf("startup calls=%d", calls.Load())
	}
	ticks <- base.Add(time.Second) // Previous local poll completed, no retry.
	if calls.Load() != 1 {
		t.Fatal("retried on policy poll")
	}
	ticks <- base.Add(24 * time.Hour)
	ticks <- base.Add(24*time.Hour + time.Second)
	if calls.Load() != 2 {
		t.Fatalf("daily calls=%d", calls.Load())
	}
	enabled.Store(false)
	ticks <- base.Add(24*time.Hour + 2*time.Second)
	ticks <- base.Add(24*time.Hour + 3*time.Second)
	if calls.Load() != 2 {
		t.Fatal("lookup while revoked")
	}
	enabled.Store(true)
	ticks <- base.Add(24*time.Hour + 4*time.Second)
	ticks <- base.Add(24*time.Hour + 5*time.Second)
	if calls.Load() != 3 {
		t.Fatalf("grant not observed promptly: %d", calls.Load())
	}
	cancel()
	<-done
}

func TestGitHubAccountTelemetryDisabledPolicyPollStops(t *testing.T) {
	polls := make(chan struct{}, 4)
	cfg := config.TelemetryConfig{Events: true, Remote: config.TelemetryRemotePostHog, PostHogKey: "test-key", PostHogHost: "https://example.test"}
	stop := startGitHubAccountTelemetry(t.Context(), cfg, nil, func() bool {
		polls <- struct{}{}
		return false
	}, func(context.Context) (ports.SCMIdentity, error) {
		t.Error("disabled poll must not look up GitHub")
		return ports.SCMIdentity{}, nil
	}, identityAuthorityFunc(func(context.Context) (ports.AgentSwitchFailureAuthoritySnapshot, error) {
		t.Error("effective disabled policy needs no authority lookup")
		return ports.AgentSwitchFailureAuthoritySnapshot{}, nil
	}))
	defer stop()
	for range 2 { // Startup and a real one-second local-policy tick.
		select {
		case <-polls:
		case <-time.After(5 * time.Second):
			t.Fatal("policy poll did not run")
		}
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled poll did not cancel promptly")
	}
}
