package claudecode

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentcreds"
)

func TestAuthCacheRequiresTheCurrentCredentialAndSupportsInvalidation(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	cache := newAuthCache(time.Minute)
	cache.now = func() time.Time { return now }
	credential := agentcreds.Credential{Kind: agentcreds.KindAPIKey, Secret: "working"}
	result := agentcreds.Result{
		State: agentcreds.StateValid, Provider: agentcreds.ProviderFirstParty,
		Fingerprint: credential.Fingerprint(), CheckedAt: now,
	}

	cache.put(result)
	if _, ok := cache.get(credential.Fingerprint(), agentcreds.ProviderFirstParty); !ok {
		t.Fatal("current credential should reuse its cached verdict")
	}
	other := agentcreds.Credential{Kind: agentcreds.KindAPIKey, Secret: "replacement"}
	if _, ok := cache.get(other.Fingerprint(), agentcreds.ProviderFirstParty); ok {
		t.Fatal("a replacement credential must not reuse the previous verdict")
	}
	if _, ok := cache.get(credential.Fingerprint(), agentcreds.ProviderBedrock); ok {
		t.Fatal("a different provider must not reuse the previous verdict")
	}
	cache.invalidate()
	if _, ok := cache.get(credential.Fingerprint(), agentcreds.ProviderFirstParty); ok {
		t.Fatal("runtime rejection must invalidate the cached verdict")
	}
}

func TestAuthCacheUsesShortTTLForNegativeAndInconclusiveResults(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	cache := newAuthCache(time.Minute)
	cache.now = func() time.Time { return now }
	credential := agentcreds.Credential{
		Kind: agentcreds.KindAPIKey, Secret: "working", Provider: agentcreds.ProviderFirstParty,
	}
	fingerprint := credential.Fingerprint()

	for _, state := range []agentcreds.State{agentcreds.StateInvalid, agentcreds.StateUnknown} {
		cache.put(agentcreds.Result{State: state, Provider: agentcreds.ProviderFirstParty, Fingerprint: fingerprint})
		if _, ok := cache.get(fingerprint, agentcreds.ProviderFirstParty); !ok {
			t.Fatalf("%s result should be reused briefly", state)
		}
		now = now.Add(30*time.Second + time.Nanosecond)
		if _, ok := cache.get(fingerprint, agentcreds.ProviderFirstParty); ok {
			t.Fatalf("%s result should expire after 30 seconds", state)
		}
	}
	cache.put(agentcreds.Result{State: agentcreds.StateValid, Provider: agentcreds.ProviderFirstParty, Fingerprint: fingerprint})
	now = now.Add(time.Minute + time.Nanosecond)
	if _, ok := cache.get(fingerprint, agentcreds.ProviderFirstParty); ok {
		t.Fatal("expired verdict must not be reused")
	}
}

func TestAuthCacheDoesNotStoreResultsWithoutAStableFingerprint(t *testing.T) {
	cache := newAuthCache(time.Minute)
	cache.put(agentcreds.Result{State: agentcreds.StateInvalid, Provider: agentcreds.ProviderFirstParty})
	if cache.entry != nil {
		t.Fatal("a result without a credential fingerprint must not be cached")
	}
}
