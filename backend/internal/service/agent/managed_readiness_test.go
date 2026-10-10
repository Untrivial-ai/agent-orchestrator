package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// managedProviderFake is an Account Manager owning Codex and Claude, except in
// cloud credential scopes.
type managedProviderFake struct {
	mu             sync.Mutex // a background revalidation reads while a test edits
	auth           domain.AgentAuthenticationState
	catalog        ports.AgentModelCatalog
	fingerprintErr error
	harness        domain.AgentHarness
	purpose        domain.AgentReadinessPurpose
	scope          string
	checks, calls  atomic.Int32
}

func (f *managedProviderFake) owns(harness domain.AgentHarness, scope string) bool {
	return domain.AccountProvider(harness) != "" && !strings.HasPrefix(scope, "@cred:")
}

func (f *managedProviderFake) AuthenticationReadiness(_ context.Context, harness domain.AgentHarness, purpose domain.AgentReadinessPurpose) (domain.AgentAuthenticationObservation, bool) {
	if !f.owns(harness, "") {
		return domain.AgentAuthenticationObservation{}, false
	}
	f.checks.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.harness, f.purpose = harness, purpose
	return successfulAuthentication(time.Now(), f.auth, domain.AgentReadinessReasonAuthorized, "managed"), true
}

func (f *managedProviderFake) ModelsFingerprint(_ context.Context, harness domain.AgentHarness, scope string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := ""
	for _, model := range f.catalog.Models {
		ids += model.ID + ","
	}
	return ids, f.owns(harness, scope), f.fingerprintErr
}

func (f *managedProviderFake) DiscoverModels(_ context.Context, harness domain.AgentHarness, scope string) (ports.AgentModelCatalog, bool, error) {
	if !f.owns(harness, scope) {
		return ports.AgentModelCatalog{}, false, nil
	}
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.harness, f.scope = harness, scope
	return f.catalog, true, nil
}

// change edits or reads the fake while nothing in the background can touch it.
func (f *managedProviderFake) change(edit func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit()
}

func managedReadinessService(id string, native *readinessTestAgent, managed ports.ManagedProvider) *Service {
	agents := []agentregistry.HarnessAgent{readinessHarness(id, id, native)}
	svc := newService(agents, nil, nil, nil)
	svc.managed = managed
	svc.readiness = newReadinessCoordinator(readinessCoordinatorConfig{Agents: agents, AuthenticationCheck: svc.managedAuthenticationCheck})
	return svc
}

func TestManagedProviderReadinessReplacesTheNativeAuthCheckForCodexAndClaudeOnly(t *testing.T) {
	for _, harness := range []domain.AgentHarness{domain.HarnessCodex, domain.HarnessClaudeCode, domain.HarnessCursor} {
		for _, purpose := range []domain.AgentReadinessPurpose{domain.AgentReadinessPurposeDisplay, domain.AgentReadinessPurposeLaunch} {
			for _, authorized := range []bool{false, true} {
				// Native state deliberately contradicts managed state.
				nativeStatus, managed := ports.AgentAuthStatusAuthorized, &managedProviderFake{auth: domain.AgentAuthenticationUnauthorized}
				if authorized {
					nativeStatus, managed.auth = ports.AgentAuthStatusUnauthorized, domain.AgentAuthenticationAuthorized
				}
				native := &readinessTestAgent{
					resolve: func(context.Context) (string, error) { return "/fake/agent", nil },
					auth:    func(context.Context) (ports.AgentAuthStatus, error) { return nativeStatus, nil },
				}
				got, err := managedReadinessService(string(harness), native, managed).EnsureAgentReadiness(context.Background(), string(harness), purpose)
				if err != nil {
					t.Fatal(err)
				}
				owned := harness != domain.HarnessCursor
				if owned != (got.Authentication.State == managed.auth) || owned != (managed.checks.Load() == 1) || owned == (native.authCalls.Load() == 1) {
					t.Fatalf("%s/%s authorized=%v: readiness=%+v managed=%d native=%d", harness, purpose, authorized, got.Authentication, managed.checks.Load(), native.authCalls.Load())
				}
				if owned && (managed.harness != harness || managed.purpose != purpose || (got.EffectiveReadiness == domain.AgentReadinessReady) != authorized) {
					t.Fatalf("%s/%s authorized=%v: asked %s/%s, readiness=%s", harness, purpose, authorized, managed.harness, managed.purpose, got.EffectiveReadiness)
				}
			}
		}
	}
}

func TestManagedAccountDoesNotBypassMissingInstallation(t *testing.T) {
	native := &readinessTestAgent{resolve: func(context.Context) (string, error) { return "", errors.New("binary missing") }}
	managed := &managedProviderFake{auth: domain.AgentAuthenticationAuthorized}
	got, err := managedReadinessService("claude-code", native, managed).EnsureAgentReadiness(context.Background(), "claude-code", domain.AgentReadinessPurposeLaunch)
	if err != nil || got.EffectiveReadiness == domain.AgentReadinessReady || managed.checks.Load() != 0 {
		t.Fatalf("readiness=%+v checks=%d err=%v", got, managed.checks.Load(), err)
	}
}

func managedCatalogService(managed *managedProviderFake, agentID string, models ...string) (*Service, *fakeModelDiscoverer) {
	managed.catalog = ports.AgentModelCatalog{AgentID: agentID, SelectionMode: ports.ModelSelectionCatalog, Source: ports.ModelCatalogSourceManagedAccount}
	for _, id := range models {
		managed.catalog.Models = append(managed.catalog.Models, ports.AgentModelInfo{ID: id, Label: id})
	}
	native := successfulModelDiscoverer()
	return NewWithDeps(Deps{Cache: &fakeModelCache{}, Discoverer: native, Context: context.Background(), ManagedAccountProvider: managed}), native
}

func TestManagedModelCatalogueServesDefaultAndAccountScopesSeparately(t *testing.T) {
	managed := &managedProviderFake{}
	svc, native := managedCatalogService(managed, "codex", "account-model")
	for i, scope := range []string{"", ports.ModelCatalogAccountScope("account-2")} {
		got, err := svc.Models(context.Background(), "codex", scope, true)
		if err != nil || len(got.Models) != 1 || got.Models[0].ID != "account-model" || got.Source != ports.ModelCatalogSourceManagedAccount {
			t.Fatalf("scope %q: catalog=%+v err=%v", scope, got, err)
		}
		// A cached read of an unchanged list discovers nothing again.
		_, err = svc.Models(context.Background(), "codex", scope, false)
		managed.change(func() {
			if err != nil || managed.calls.Load() != int32(i+1) || managed.harness != domain.HarnessCodex || managed.scope != scope {
				t.Fatalf("scope %q: calls=%d asked %s %q err=%v", scope, managed.calls.Load(), managed.harness, managed.scope, err)
			}
		})
	}
	if native.discoverCalls.Load() != 0 || native.fingerprintRequests.Load() != 0 {
		t.Fatalf("native discovery was consulted: discover=%d fingerprint=%d", native.discoverCalls.Load(), native.fingerprintRequests.Load())
	}
}

func TestManagedModelCatalogueRefreshesWhenItsFingerprintChanges(t *testing.T) {
	managed := &managedProviderFake{}
	svc, native := managedCatalogService(managed, "claude-code", "old-model")
	if _, err := svc.Models(context.Background(), "claude-code", "", true); err != nil {
		t.Fatal(err)
	}
	// While the helper cannot say, the cached catalogue stands.
	managed.change(func() {
		managed.catalog.Models, managed.fingerprintErr = []ports.AgentModelInfo{{ID: "new-model"}}, errors.New("helper unavailable")
	})
	if got, err := svc.Models(context.Background(), "claude-code", "", false); err != nil || got.Models[0].ID != "old-model" || managed.calls.Load() != 1 {
		t.Fatalf("during an outage: catalog=%+v calls=%d err=%v", got.Models, managed.calls.Load(), err)
	}
	managed.change(func() { managed.fingerprintErr = nil })
	if got, err := svc.Models(context.Background(), "claude-code", "", false); err != nil || len(got.Models) != 1 || got.Models[0].ID != "new-model" {
		t.Fatalf("after the change: catalog=%+v err=%v", got.Models, err)
	}
	if native.discoverCalls.Load() != 0 || native.fingerprintRequests.Load() != 0 {
		t.Fatalf("native discovery was consulted: discover=%d fingerprint=%d", native.discoverCalls.Load(), native.fingerprintRequests.Load())
	}
}

func TestManagedModelDiscoveryLeavesCloudCredentialScopesAlone(t *testing.T) {
	managed := &managedProviderFake{}
	svc, native := managedCatalogService(managed, "claude-code", "managed")
	if _, err := svc.Models(context.Background(), "claude-code", "@cred:anthropic_api_key", true); err != nil {
		t.Fatal(err)
	}
	if managed.calls.Load() != 0 || native.discoverCalls.Load() != 1 {
		t.Fatalf("managed=%d native=%d, want the native discovery for a cloud scope", managed.calls.Load(), native.discoverCalls.Load())
	}
}
