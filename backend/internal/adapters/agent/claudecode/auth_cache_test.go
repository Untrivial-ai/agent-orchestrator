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
		State: agentcreds.StateValid, Fingerprint: credential.Fingerprint(), CheckedAt: now,
	}

	cache.put(result)
	if _, ok := cache.get(credential.Fingerprint()); !ok {
		t.Fatal("current credential should reuse its cached verdict")
	}
	other := agentcreds.Credential{Kind: agentcreds.KindAPIKey, Secret: "replacement"}
	if _, ok := cache.get(other.Fingerprint()); ok {
		t.Fatal("a replacement credential must not reuse the previous verdict")
	}
	cache.invalidate()
	if _, ok := cache.get(credential.Fingerprint()); ok {
		t.Fatal("runtime rejection must invalidate the cached verdict")
	}
}

func TestAuthCacheExpiresAndDoesNotStoreUnknown(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	cache := newAuthCache(time.Minute)
	cache.now = func() time.Time { return now }
	credential := agentcreds.Credential{Kind: agentcreds.KindAPIKey, Secret: "working"}
	fingerprint := credential.Fingerprint()

	cache.put(agentcreds.Result{State: agentcreds.StateUnknown, Fingerprint: fingerprint})
	if _, ok := cache.get(fingerprint); ok {
		t.Fatal("an inconclusive probe must not suppress the next validation attempt")
	}
	cache.put(agentcreds.Result{State: agentcreds.StateValid, Fingerprint: fingerprint})
	now = now.Add(time.Minute + time.Nanosecond)
	if _, ok := cache.get(fingerprint); ok {
		t.Fatal("expired verdict must not be reused")
	}
}
