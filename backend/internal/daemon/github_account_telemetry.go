package daemon

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const githubAccountEvent = "ao.github.account_observed"

var githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// startGitHubAccountTelemetry records a time-stamped account/installation link,
// not a PostHog person profile. Lookups run off the startup path and refresh so
// signing in or switching accounts does not require a daemon restart. The
// resolver must use fresh credentials, not a provider's cached identity.
func startGitHubAccountTelemetry(
	ctx context.Context,
	cfg config.TelemetryConfig,
	sink ports.EventSink,
	resolve func(context.Context) (ports.SCMIdentity, error),
) func() {
	if !cfg.Events || cfg.Remote != config.TelemetryRemotePostHog || strings.TrimSpace(cfg.PostHogKey) == "" || strings.TrimSpace(cfg.PostHogHost) == "" {
		return func() {}
	}
	for _, raw := range cfg.DisabledEvents {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == githubAccountEvent || (strings.HasSuffix(name, "*") && len(name) > 1 && strings.HasPrefix(githubAccountEvent, strings.TrimSuffix(name, "*"))) {
			return func() {}
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		runGitHubAccountTelemetry(ctx, sink, resolve, ticker.C)
	}()
	return func() {
		cancel()
		<-done
	}
}

func runGitHubAccountTelemetry(
	ctx context.Context,
	sink ports.EventSink,
	resolve func(context.Context) (ports.SCMIdentity, error),
	ticks <-chan time.Time,
) {
	for ctx.Err() == nil {
		lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		identity, err := resolve(lookupCtx)
		cancel()
		login := strings.TrimSpace(identity.Login)
		// Never fall back to a repository owner, git user.name, or a stale
		// successful lookup. Failures and bot credentials are not user identity.
		if err == nil && ctx.Err() == nil && identity.Human && githubLoginPattern.MatchString(login) {
			sink.Emit(ctx, ports.TelemetryEvent{
				Name:       githubAccountEvent,
				Source:     "daemon",
				OccurredAt: time.Now().UTC(),
				Level:      ports.TelemetryLevelInfo,
				Payload:    map[string]any{"github_login": login},
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}
