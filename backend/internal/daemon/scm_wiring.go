package daemon

import (
	"context"
	"errors"
	"log/slog"

	scmgithub "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/github"
	scmgitlab "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/gitlab"
	scmmulti "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/multi"
	scmonedev "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/onedev"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	scmobserve "github.com/aoagents/agent-orchestrator/backend/internal/observe/scm"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// startSCMObserver wires the provider-neutral SCM observer with the GitHub,
// GitLab and OneDev providers via a multi Provider dispatcher. Missing
// credentials for one provider do not prevent the others from starting; the
// observer is disabled only when no provider has usable credentials.
func startSCMObserver(ctx context.Context, store *sqlite.Store, lcm *lifecycle.Manager, gitlabCfg config.GitLabConfig, onedevCfg config.OneDevConfig, logger *slog.Logger) <-chan struct{} {
	// This is the only construction path that logs. The other two run at the
	// same daemon boot from the same configuration, so logging there would
	// repeat every "provider disabled" line three times.
	subs := scmSubProviders(gitlabCfg, onedevCfg, logger, func(key string, err error) {
		logSCMProviderDisabled(logger, key, err)
	})
	if len(subs) == 0 {
		logger.Warn("scm observer disabled: no usable SCM provider")
		return closedDone()
	}
	provider := scmmulti.New(namedSCMProviders(subs)...)
	observer := scmobserve.New(provider, store, lcm, scmobserve.Config{Logger: logger, ScopedIdentityResolver: provider})
	return observer.Start(ctx)
}

// scmSubProvider is the capability set every AO SCM adapter satisfies: the
// observer's Provider contract, plus merge dispatch for `ao pr merge`. A
// provider that cannot merge (OneDev) still satisfies ports.SCMMerger by
// returning ports.ErrSCMUnsupported, so "cannot merge" reaches the caller as a
// capability answer rather than as an unknown-provider failure.
type scmSubProvider interface {
	scmobserve.Provider
	ports.SCMMerger
}

// namedSCMSubProvider pairs an SCM adapter with the routing key its
// ParseRepository stamps on repositories.
type namedSCMSubProvider struct {
	Key      string
	Provider scmSubProvider
}

// scmSubProviders builds every configured SCM adapter, in a stable order.
//
// All three multi-provider constructors go through here so they cannot drift
// apart: OneDev shipped registered in the observer but missing from the
// session PR claimer and the merger, which surfaced live as `ao session
// claim-pr` failing with SCM_UNAVAILABLE on a OneDev pull request. Adding a
// provider is now one edit, not three.
//
// A provider whose credentials or configuration are missing is reported to
// onDisabled (which may be nil) and omitted; it never blocks the others.
func scmSubProviders(gitlabCfg config.GitLabConfig, onedevCfg config.OneDevConfig, logger *slog.Logger, onDisabled func(key string, err error)) []namedSCMSubProvider {
	disabled := func(key string, err error) {
		if onDisabled != nil {
			onDisabled(key, err)
		}
	}

	var subs []namedSCMSubProvider
	if gh, err := newGitHubSCMProvider(logger); err != nil {
		disabled("github", err)
	} else {
		subs = append(subs, namedSCMSubProvider{Key: "github", Provider: gh})
	}
	if gl, err := newGitLabSCMProvider(gitlabCfg, logger); err != nil {
		disabled("gitlab", err)
	} else {
		subs = append(subs, namedSCMSubProvider{Key: "gitlab", Provider: gl})
	}
	if od, err := newOneDevSCMProvider(onedevCfg, logger); err != nil {
		disabled(scmonedev.ProviderKey, err)
	} else {
		subs = append(subs, namedSCMSubProvider{Key: scmonedev.ProviderKey, Provider: od})
	}
	return subs
}

func namedSCMProviders(subs []namedSCMSubProvider) []scmmulti.NamedProvider {
	named := make([]scmmulti.NamedProvider, 0, len(subs))
	for _, sub := range subs {
		named = append(named, scmmulti.NamedProvider{Key: sub.Key, Provider: sub.Provider})
	}
	return named
}

func namedSCMMergers(subs []namedSCMSubProvider) []scmmulti.NamedMerger {
	named := make([]scmmulti.NamedMerger, 0, len(subs))
	for _, sub := range subs {
		named = append(named, scmmulti.NamedMerger{Key: sub.Key, Merger: sub.Provider})
	}
	return named
}

func newGitHubSCMProvider(logger *slog.Logger) (*scmgithub.Provider, error) {
	tokens := scmgithub.FallbackTokenSource{
		scmgithub.EnvTokenSource{EnvVars: []string{"AO_GITHUB_TOKEN"}},
		&scmgithub.GHTokenSource{},
	}
	return scmgithub.NewProvider(scmgithub.ProviderOptions{Token: tokens, SkipTokenPreflight: true, Logger: logger})
}

func newGitLabSCMProvider(gitlabCfg config.GitLabConfig, logger *slog.Logger) (*scmgitlab.Provider, error) {
	tokens := scmgitlab.FallbackTokenSource{
		scmgitlab.EnvTokenSource{EnvVars: []string{"AO_GITLAB_TOKEN"}},
		&scmgitlab.GLabTokenSource{},
	}
	hostTokens := make(map[string]scmgitlab.TokenSource, len(gitlabCfg.HostTokens))
	for host, token := range gitlabCfg.HostTokens {
		hostTokens[host] = scmgitlab.StaticTokenSource(token)
	}
	return scmgitlab.NewProvider(scmgitlab.ProviderOptions{
		Token:              tokens,
		SkipTokenPreflight: true,
		Logger:             logger,
		AllowedHosts:       gitlabCfg.AllowedHosts,
		HostTokens:         hostTokens,
	})
}

// newOneDevSCMProvider builds the OneDev provider from its boot configuration.
//
// OneDev is always self-hosted, so an operator who has not set
// AO_ONEDEV_ALLOWED_HOSTS has no OneDev instance for AO to observe. That is
// the common case, and NewProvider reports it as ErrNoAllowedHosts — which the
// caller logs and steps over, exactly as it does a missing GitHub or GitLab
// token, so an unconfigured OneDev never blocks the other providers.
//
// SkipTokenPreflight matches the GitHub and GitLab wiring: the credential is
// resolved lazily on first use rather than at construction, so a credential
// helper that is momentarily unavailable at daemon boot does not disable the
// provider for the life of the process.
func newOneDevSCMProvider(onedevCfg config.OneDevConfig, logger *slog.Logger) (*scmonedev.Provider, error) {
	tokens := scmonedev.FallbackTokenSource{
		scmonedev.StaticTokenSource(onedevCfg.Token),
		scmonedev.EnvTokenSource{EnvVars: []string{"AO_ONEDEV_TOKEN"}},
	}
	hostTokens := make(map[string]scmonedev.TokenSource, len(onedevCfg.HostTokens))
	for host, token := range onedevCfg.HostTokens {
		hostTokens[host] = scmonedev.StaticTokenSource(token)
	}
	return scmonedev.NewProvider(scmonedev.ProviderOptions{
		Token:              tokens,
		SkipTokenPreflight: true,
		Logger:             logger,
		AllowedHosts:       onedevCfg.AllowedHosts,
		HostTokens:         hostTokens,
	})
}

func logSCMProviderDisabled(logger *slog.Logger, provider string, err error) {
	switch {
	case errors.Is(err, scmgithub.ErrNoToken) || errors.Is(err, scmgithub.ErrAuthFailed) ||
		errors.Is(err, scmgitlab.ErrNoToken) || errors.Is(err, scmgitlab.ErrAuthFailed) ||
		errors.Is(err, scmonedev.ErrNoToken) || errors.Is(err, scmonedev.ErrAuthFailed):
		logger.Warn("scm provider disabled: no usable token", "provider", provider, "err", err)
	case errors.Is(err, scmonedev.ErrNoAllowedHosts):
		// Not a misconfiguration: OneDev has no public instance, so a
		// deployment that does not use OneDev simply never sets the allowlist.
		logger.Debug("scm provider disabled: no hosts configured", "provider", provider, "err", err)
	default:
		logger.Warn("scm provider disabled: setup failed", "provider", provider, "err", err)
	}
}

// newMultiSCMProvider builds a multi-provider for use outside the polling
// observer (e.g. session service PR claiming), registering the same providers
// the observer gets. Returns nil when no provider has usable credentials —
// callers must tolerate a nil SCM.
func newMultiSCMProvider(gitlabCfg config.GitLabConfig, onedevCfg config.OneDevConfig, logger *slog.Logger) *scmmulti.Provider {
	subs := scmSubProviders(gitlabCfg, onedevCfg, logger, nil)
	if len(subs) == 0 {
		return nil
	}
	return scmmulti.New(namedSCMProviders(subs)...)
}

// newMultiSCMMerger builds a multi-merger for PR merge actions, registering
// the same providers the observer gets. When one provider is unavailable
// (missing token), the multi-merger still routes to the healthy one — same
// degrade-gracefully pattern as newMultiSCMProvider. Returns nil when no
// provider has usable credentials.
//
// OneDev is registered here even though it cannot merge. Its
// MergePullRequest returns ports.ErrSCMUnsupported, so a merge attempt on a
// OneDev pull request reports that OneDev does not support the operation
// rather than "unknown provider" — see the OneDev adapter's merge.go for why
// it cannot merge safely.
func newMultiSCMMerger(gitlabCfg config.GitLabConfig, onedevCfg config.OneDevConfig, logger *slog.Logger) *scmmulti.Merger {
	subs := scmSubProviders(gitlabCfg, onedevCfg, logger, nil)
	if len(subs) == 0 {
		return nil
	}
	return scmmulti.NewMerger(namedSCMMergers(subs)...)
}

func closedDone() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
