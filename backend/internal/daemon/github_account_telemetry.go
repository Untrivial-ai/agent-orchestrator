package daemon

import (
	"context"
	"regexp"
	"strings"
	"time"

	telemetryadapter "github.com/aoagents/agent-orchestrator/backend/internal/adapters/telemetry"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Independent of the agent-switch production gate; requires an explicit v3 grant.
const githubIdentityTelemetryEnabled = domain.GitHubIdentityTelemetryEnabled

const githubAccountRefreshInterval = 24 * time.Hour

// Poll only local policy so a new grant need not wait for the daily refresh.
const githubAccountPolicyPollInterval = time.Second

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
	enabled func() bool,
	resolve func(context.Context) (ports.SCMIdentity, error),
	authority ports.AgentSwitchFailureAuthorityReader,
) func() {
	if cfg.Remote != config.TelemetryRemotePostHog || strings.TrimSpace(cfg.PostHogKey) == "" || strings.TrimSpace(cfg.PostHogHost) == "" {
		return func() {}
	}
	if telemetryadapter.NewEventDenylist(cfg.DisabledEvents).Blocks(githubAccountEvent) {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(githubAccountPolicyPollInterval)
		defer ticker.Stop()
		runGitHubAccountTelemetry(ctx, sink, enabled, resolve, authority, ticker.C)
	}()
	return func() {
		cancel()
		<-done
	}
}

func runGitHubAccountTelemetry(
	ctx context.Context,
	sink ports.EventSink,
	enabled func() bool,
	resolve func(context.Context) (ports.SCMIdentity, error),
	authority ports.AgentSwitchFailureAuthorityReader,
	ticks <-chan time.Time,
) {
	now := time.Now()
	var nextRefresh time.Time
	var lastGrant ports.AgentSwitchFailureAuthoritySnapshot
	for ctx.Err() == nil {
		observe := func() {
			if !enabled() {
				nextRefresh = time.Time{}
				lastGrant = ports.AgentSwitchFailureAuthoritySnapshot{}
				return
			}
			before, consentErr := authority.ReadAgentSwitchFailureAuthority(ctx)
			if !githubIdentityTelemetryEnabled || consentErr != nil || !before.Present || !before.EventsEnabled || !before.ConsentIdentityEnabled || before.ConsentGeneration == "" {
				nextRefresh = time.Time{}
				lastGrant = ports.AgentSwitchFailureAuthoritySnapshot{}
				return
			}
			if before == lastGrant && now.Before(nextRefresh) {
				return
			}
			lastGrant = before
			// Failed lookups also wait a day; a new grant may trigger sooner.
			nextRefresh = now.Add(githubAccountRefreshInterval)
			lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			identity, err := resolve(lookupCtx)
			cancel()
			after, consentErr := authority.ReadAgentSwitchFailureAuthority(ctx)
			login := strings.TrimSpace(identity.Login)
			// Never fall back to a repository owner, git user.name, or a stale
			// successful lookup. Failures and bot credentials are not user identity.
			if err == nil && consentErr == nil && after == before && enabled() && ctx.Err() == nil && identity.Human && githubLoginPattern.MatchString(login) {
				sink.Emit(ctx, ports.TelemetryEvent{
					Name:       githubAccountEvent,
					Source:     "daemon",
					OccurredAt: time.Now().UTC(),
					Level:      ports.TelemetryLevelInfo,
					Payload:    map[string]any{"github_login": login},
				})
			}
		}
		observe()
		select {
		case <-ctx.Done():
			return
		case tick, ok := <-ticks:
			if !ok {
				return
			}
			now = tick
		}
	}
}
