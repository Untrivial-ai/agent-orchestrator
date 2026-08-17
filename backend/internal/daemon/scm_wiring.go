package daemon

import (
	"context"
	"errors"
	"log/slog"

	scmgithub "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/github"
	scmgitlab "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/gitlab"
	scmmulti "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/multi"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	scmobserve "github.com/aoagents/agent-orchestrator/backend/internal/observe/scm"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// startSCMObserver wires the provider-neutral SCM observer with both GitHub
// and GitLab providers via a multi Provider dispatcher. Missing credentials
// for one provider do not prevent the other from starting; the observer is
// disabled only when no provider has usable credentials.
func startSCMObserver(ctx context.Context, store *sqlite.Store, lcm *lifecycle.Manager, gitlabCfg config.GitLabConfig, logger *slog.Logger) <-chan struct{} {
	var named []scmmulti.NamedProvider

	ghProvider, ghErr := newGitHubSCMProvider(logger)
	if ghErr != nil {
		logSCMProviderDisabled(logger, "github", ghErr)
	} else {
		named = append(named, scmmulti.NamedProvider{Key: "github", Provider: ghProvider})
	}

	glProvider, glErr := newGitLabSCMProvider(gitlabCfg, logger)
	if glErr != nil {
		logSCMProviderDisabled(logger, "gitlab", glErr)
	} else {
		named = append(named, scmmulti.NamedProvider{Key: "gitlab", Provider: glProvider})
	}

	if len(named) == 0 {
		logger.Warn("scm observer disabled: no usable SCM provider")
		return closedDone()
	}
	provider := scmmulti.New(named...)
	observer := scmobserve.New(provider, store, lcm, scmobserve.Config{Logger: logger, ScopedIdentityResolver: provider})
	return observer.Start(ctx)
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

func logSCMProviderDisabled(logger *slog.Logger, provider string, err error) {
	if errors.Is(err, scmgithub.ErrNoToken) || errors.Is(err, scmgithub.ErrAuthFailed) ||
		errors.Is(err, scmgitlab.ErrNoToken) || errors.Is(err, scmgitlab.ErrAuthFailed) {
		logger.Warn("scm provider disabled: no usable token", "provider", provider, "err", err)
	} else {
		logger.Warn("scm provider disabled: setup failed", "provider", provider, "err", err)
	}
}

// newMultiSCMProvider builds a multi-provider for use outside the polling
// observer (e.g. session service PR claiming). Returns nil when no provider
// has usable credentials — callers must tolerate a nil SCM.
func newMultiSCMProvider(gitlabCfg config.GitLabConfig, logger *slog.Logger) *scmmulti.Provider {
	var named []scmmulti.NamedProvider
	if gh, err := newGitHubSCMProvider(logger); err == nil {
		named = append(named, scmmulti.NamedProvider{Key: "github", Provider: gh})
	}
	if gl, err := newGitLabSCMProvider(gitlabCfg, logger); err == nil {
		named = append(named, scmmulti.NamedProvider{Key: "gitlab", Provider: gl})
	}
	if len(named) == 0 {
		return nil
	}
	return scmmulti.New(named...)
}

// newMultiSCMMerger builds a multi-merger for PR merge actions, registering
// both GitHub and GitLab providers. When one provider is unavailable (missing
// token), the multi-merger still routes to the healthy one — same
// degrade-gracefully pattern as newMultiSCMProvider. Returns nil when no
// provider has usable credentials.
func newMultiSCMMerger(gitlabCfg config.GitLabConfig, logger *slog.Logger) *scmmulti.Merger {
	var named []scmmulti.NamedMerger
	if gh, err := newGitHubSCMProvider(logger); err == nil {
		named = append(named, scmmulti.NamedMerger{Key: "github", Merger: gh})
	}
	if gl, err := newGitLabSCMProvider(gitlabCfg, logger); err == nil {
		named = append(named, scmmulti.NamedMerger{Key: "gitlab", Merger: gl})
	}
	if len(named) == 0 {
		return nil
	}
	return scmmulti.NewMerger(named...)
}

func closedDone() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
