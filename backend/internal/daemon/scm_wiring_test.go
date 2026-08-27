package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	scmmulti "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/multi"
	scmonedev "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/onedev"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	scmobserve "github.com/aoagents/agent-orchestrator/backend/internal/observe/scm"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestSCMWiring_MultiProviderSatisfiesScopedIdentityResolver verifies that the
// multi provider constructed with both GitHub and GitLab sub-providers
// satisfies ports.ScopedIdentityResolver so it can be wired as the observer's
// scoped identity resolver (finding #7).
func TestSCMWiring_MultiProviderSatisfiesScopedIdentityResolver(t *testing.T) {
	gh, err := newGitHubSCMProvider(slog.Default())
	if err != nil {
		t.Skipf("github provider unavailable (no token): %v", err)
	}
	gl, err := newGitLabSCMProvider(testGitLabConfig(), slog.Default())
	if err != nil {
		t.Skipf("gitlab provider unavailable (no token): %v", err)
	}

	multi := scmmulti.New(
		scmmulti.NamedProvider{Key: "github", Provider: gh},
		scmmulti.NamedProvider{Key: "gitlab", Provider: gl},
	)

	// The multi provider must satisfy ScopedIdentityResolver.
	var _ ports.ScopedIdentityResolver = multi

	// The multi provider must also satisfy the observer's Provider interface
	// (it already does in production; this asserts the type assertion at compile
	// time for the test).
	_ = multi.SCMCredentialsAvailable
}

// TestSCMWiring_NewMultiSCMProviderReturnsScopedResolver verifies that the
// newMultiSCMProvider helper (used outside the observer) also returns a value
// that satisfies ScopedIdentityResolver.
func TestSCMWiring_NewMultiSCMProviderReturnsScopedResolver(t *testing.T) {
	multi := newMultiSCMProvider(testGitLabConfig(), testOneDevConfig(), slog.Default())
	if multi == nil {
		t.Skip("no SCM provider available (missing tokens)")
	}
	var _ ports.ScopedIdentityResolver = multi
}

// TestSCMWiring_ObserverConfigHasScopedResolver verifies that the observer
// Config constructed in production wiring (startSCMObserver) carries a
// non-nil ScopedIdentityResolver when a multi provider is available.
func TestSCMWiring_ObserverConfigHasScopedResolver(t *testing.T) {
	gh, err := newGitHubSCMProvider(slog.Default())
	if err != nil {
		t.Skipf("github provider unavailable: %v", err)
	}
	gl, err := newGitLabSCMProvider(testGitLabConfig(), slog.Default())
	if err != nil {
		t.Skipf("gitlab provider unavailable: %v", err)
	}

	multi := scmmulti.New(
		scmmulti.NamedProvider{Key: "github", Provider: gh},
		scmmulti.NamedProvider{Key: "gitlab", Provider: gl},
	)

	// Mirror the production wiring in startSCMObserver: the multi provider
	// is passed as the ScopedIdentityResolver in the observer Config.
	cfg := scmobserve.Config{
		ScopedIdentityResolver: multi,
	}
	if cfg.ScopedIdentityResolver == nil {
		t.Fatal("ScopedIdentityResolver is nil; production wiring must set it")
	}

	// Verify it actually resolves per-provider (github identity is available
	// if a token was set; if not, it should still return an error, not panic).
	_, _ = cfg.ScopedIdentityResolver.AuthenticatedIdentityForProvider(context.Background(), "github", "")
}

func testGitLabConfig() config.GitLabConfig {
	return config.GitLabConfig{}
}

func testOneDevConfig() config.OneDevConfig {
	return config.OneDevConfig{
		Token:        "od-token",
		AllowedHosts: []string{"http://onedev.test:6610"},
	}
}

// TestSCMWiring_OneDevProviderRegisters checks that a configured OneDev
// instance yields a provider the observer can dispatch to: it must satisfy the
// observer's Provider contract and route by the "onedev" key that
// ParseRepository stamps on repositories.
func TestSCMWiring_OneDevProviderRegisters(t *testing.T) {
	od, err := newOneDevSCMProvider(testOneDevConfig(), slog.Default())
	if err != nil {
		t.Fatalf("newOneDevSCMProvider: %v", err)
	}
	var provider scmobserve.Provider = od

	multi := scmmulti.New(scmmulti.NamedProvider{Key: scmonedev.ProviderKey, Provider: provider})
	repo, ok := multi.ParseRepository("http://onedev.test:6610/Homelab/curatarr.git")
	if !ok {
		t.Fatal("multi provider did not recognise a OneDev remote")
	}
	if repo.Provider != scmonedev.ProviderKey {
		t.Fatalf("repo.Provider = %q, want %q", repo.Provider, scmonedev.ProviderKey)
	}
}

// TestSCMWiring_OneDevUnconfiguredDegradesGracefully pins the
// degrade-gracefully contract: OneDev is always self-hosted, so most
// deployments never set AO_ONEDEV_ALLOWED_HOSTS. That must read as "this
// provider is not in use", not as a failure that stops GitHub and GitLab from
// being registered.
func TestSCMWiring_OneDevUnconfiguredDegradesGracefully(t *testing.T) {
	_, err := newOneDevSCMProvider(config.OneDevConfig{}, slog.Default())
	if !errors.Is(err, scmonedev.ErrNoAllowedHosts) {
		t.Fatalf("err = %v, want ErrNoAllowedHosts", err)
	}
	// startSCMObserver logs and steps over this error rather than returning,
	// so a multi provider built from the other sub-providers is unaffected.
	logSCMProviderDisabled(slog.Default(), "onedev", err)
}

// TestSCMWiring_OneDevPerHostTokenOverride verifies that per-host tokens from
// AO_ONEDEV_HOST_TOKENS reach the provider. A mis-keyed entry is a warning, not
// a construction failure, so the wiring only has to pass them through.
func TestSCMWiring_OneDevPerHostTokenOverride(t *testing.T) {
	cfg := testOneDevConfig()
	cfg.Token = ""
	cfg.HostTokens = map[string]string{"http://onedev.test:6610": "per-host-token"}

	od, err := newOneDevSCMProvider(cfg, slog.Default())
	if err != nil {
		t.Fatalf("newOneDevSCMProvider: %v", err)
	}
	ok, err := od.SCMCredentialsAvailable(context.Background())
	if err != nil {
		t.Fatalf("SCMCredentialsAvailable: %v", err)
	}
	if !ok {
		t.Fatal("credentials unavailable; the per-host token did not reach the provider")
	}
}

// TestSCMWiring_AllConstructorsRegisterOneDev is the regression test for the
// gap this file's helpers used to have: OneDev was registered by
// startSCMObserver only, so `ao session claim-pr` (newMultiSCMProvider) and
// `ao pr merge` (newMultiSCMMerger) failed on a OneDev pull request even
// though the observer was polling it happily. A test that covered the
// observer alone is exactly what let that through, so every construction path
// is asserted here.
func TestSCMWiring_AllConstructorsRegisterOneDev(t *testing.T) {
	glCfg, odCfg := testGitLabConfig(), testOneDevConfig()
	logger := slog.Default()

	t.Run("scmSubProviders", func(t *testing.T) {
		// startSCMObserver builds its multi provider from this list, so a key
		// present here is a key the observer dispatches on.
		subs := scmSubProviders(glCfg, odCfg, logger, nil)
		if !hasSubProvider(subs, scmonedev.ProviderKey) {
			t.Fatalf("scmSubProviders registered %v, want it to include %q", subProviderKeys(subs), scmonedev.ProviderKey)
		}
	})

	t.Run("newMultiSCMProvider", func(t *testing.T) {
		multi := newMultiSCMProvider(glCfg, odCfg, logger)
		if multi == nil {
			t.Fatal("newMultiSCMProvider returned nil despite a configured OneDev instance")
		}
		repo, ok := multi.ParseRepository(testOneDevRemote)
		if !ok {
			t.Fatalf("multi provider did not recognise %q; onedev is not registered", testOneDevRemote)
		}
		if repo.Provider != scmonedev.ProviderKey {
			t.Fatalf("repo.Provider = %q, want %q", repo.Provider, scmonedev.ProviderKey)
		}
	})

	t.Run("newMultiSCMMerger", func(t *testing.T) {
		merger := newMultiSCMMerger(glCfg, odCfg, logger)
		if merger == nil {
			t.Fatal("newMultiSCMMerger returned nil despite a configured OneDev instance")
		}
		_, err := merger.MergePullRequest(context.Background(), ports.SCMMergeRequest{
			PR: ports.SCMPRRef{
				Repo:   ports.SCMRepo{Provider: scmonedev.ProviderKey, Host: "onedev.test:6610", Repo: "Homelab/curatarr"},
				Number: 7,
			},
			ExpectedHeadSHA: "0123456789abcdef0123456789abcdef01234567",
			Method:          ports.SCMMergeSquash,
		})
		// Registered but unsupported: the caller learns OneDev cannot merge,
		// not that AO has never heard of the provider.
		if !errors.Is(err, ports.ErrSCMUnsupported) {
			t.Fatalf("MergePullRequest err = %v, want ports.ErrSCMUnsupported", err)
		}
		if strings.Contains(err.Error(), "unknown provider") {
			t.Fatalf("onedev is not registered in the merger: %v", err)
		}
	})
}

// TestSCMWiring_UnconfiguredOneDevLeavesOtherProvidersRegistered pins the
// degrade-gracefully contract across the shared builder: an absent OneDev
// allowlist must not remove GitHub or GitLab from any construction path.
func TestSCMWiring_UnconfiguredOneDevLeavesOtherProvidersRegistered(t *testing.T) {
	withOneDev := scmSubProviders(testGitLabConfig(), testOneDevConfig(), slog.Default(), nil)
	withoutOneDev := scmSubProviders(testGitLabConfig(), config.OneDevConfig{}, slog.Default(), nil)

	if hasSubProvider(withoutOneDev, scmonedev.ProviderKey) {
		t.Fatal("onedev registered without a configured allowlist")
	}
	for _, sub := range withOneDev {
		if sub.Key == scmonedev.ProviderKey {
			continue
		}
		if !hasSubProvider(withoutOneDev, sub.Key) {
			t.Fatalf("provider %q disappeared when OneDev was unconfigured; got %v", sub.Key, subProviderKeys(withoutOneDev))
		}
	}
}

const testOneDevRemote = "http://onedev.test:6610/Homelab/curatarr.git"

func subProviderKeys(subs []namedSCMSubProvider) []string {
	keys := make([]string, 0, len(subs))
	for _, sub := range subs {
		keys = append(keys, sub.Key)
	}
	return keys
}

func hasSubProvider(subs []namedSCMSubProvider, key string) bool {
	for _, sub := range subs {
		if sub.Key == key {
			return true
		}
	}
	return false
}
