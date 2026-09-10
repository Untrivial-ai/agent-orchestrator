package agentcreds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Phase E: chain-sourced credentials are delegated to the provider CLI, which
// has already resolved them. Nothing here may ever produce a rejection.
func TestChainDelegationNeverProducesARejection(t *testing.T) {
	tests := []struct {
		name   string
		runner commandRunner
	}{
		{
			name: "cli is not installed",
			runner: func(context.Context, string, ...string) ([]byte, error) {
				return nil, errors.New("executable file not found in $PATH")
			},
		},
		{
			name: "cli fails",
			runner: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("Unable to locate credentials"), errors.New("exit status 255")
			},
		},
		{
			name: "cli times out",
			runner: func(context.Context, string, ...string) ([]byte, error) {
				return nil, context.DeadlineExceeded
			},
		},
		{
			name:   "cli returns nothing useful",
			runner: func(context.Context, string, ...string) ([]byte, error) { return []byte("  "), nil },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			validator := New(WithCommandRunner(tc.runner))
			for _, result := range []Result{
				validator.ValidateBedrockViaCLI(context.Background(), "us-east-1"),
				validator.ValidateVertexViaCLI(context.Background(), "proj", "us-east5"),
			} {
				if result.State == StateInvalid {
					t.Fatalf("CLI delegation produced a rejection (%s); it may only ever report unknown", result.Detail)
				}
			}
		})
	}
}

// The happy path: aws resolves the chain and lists Claude models.
func TestBedrockViaCLISucceedsWhenTheChainResolves(t *testing.T) {
	validator := New(WithCommandRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "aws" {
			t.Fatalf("ran %q, want aws", name)
		}
		joined := ""
		for _, arg := range args {
			joined += arg + " "
		}
		if !contains(joined, "list-foundation-models") {
			t.Fatalf("args = %v, want a control-plane listing", args)
		}
		if contains(joined, "invoke-model") || contains(joined, "bedrock-runtime") {
			t.Fatalf("args = %v, must never call a billable endpoint", args)
		}
		return []byte(`{"modelSummaries":[{"modelId":"anthropic.claude-opus-4-5-v1:0","providerName":"Anthropic"}]}`), nil
	}))
	result := validator.ValidateBedrockViaCLI(context.Background(), "us-east-1")
	if !result.Valid() {
		t.Fatalf("state = %q (%s)", result.State, result.Detail)
	}
	if len(result.Models) != 1 {
		t.Fatalf("models = %+v", result.Models)
	}
}

// An account that authenticates but has no Claude entitlement is Unknown, not
// valid — the same rule the direct probe applies.
func TestBedrockViaCLIWithoutClaudeAccessIsUnknown(t *testing.T) {
	validator := New(WithCommandRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"modelSummaries":[]}`), nil
	}))
	result := validator.ValidateBedrockViaCLI(context.Background(), "us-east-1")
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

// gcloud hands back an access token, which is then probed like any other.
func TestVertexViaCLIProbesWithTheResolvedToken(t *testing.T) {
	var probeAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"publisherModels":[{"name":"publishers/anthropic/models/claude-x"}]}`))
	}))
	defer server.Close()

	validator := New(
		WithHTTPClient(server.Client()),
		WithCommandRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "gcloud" {
				t.Fatalf("ran %q, want gcloud", name)
			}
			return []byte("ya29.chain-resolved-token\n"), nil
		}),
	)
	validator.endpoint.vertex = server.URL
	result := validator.ValidateVertexViaCLI(context.Background(), "proj", "us-east5")
	if !result.Valid() {
		t.Fatalf("state = %q (%s)", result.State, result.Detail)
	}
	if probeAuth != "Bearer ya29.chain-resolved-token" {
		t.Fatalf("probe Authorization = %q", probeAuth)
	}
}

// End to end: an unreadable Bedrock credential falls through to the CLI rather
// than reporting that the user has no credentials.
func TestValidateLocalFallsBackToTheCLIForChainCredentials(t *testing.T) {
	called := false
	validator := New(WithCommandRunner(func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return []byte(`{"modelSummaries":[{"modelId":"anthropic.claude-x","providerName":"Anthropic"}]}`), nil
	}))
	result := validator.ValidateLocal(context.Background(), "bedrock", ResolveOptions{
		// An SSO profile: real credentials, none of them readable here.
		Env: envFrom(map[string]string{"AWS_PROFILE": "sso", "AWS_REGION": "us-east-1"}),
	})
	if !called {
		t.Fatal("chain-sourced Bedrock credentials must be delegated to the aws CLI")
	}
	if !result.Valid() {
		t.Fatalf("state = %q (%s)", result.State, result.Detail)
	}
}

// The provider gate, end to end: an unrecognized provider probes nothing.
func TestValidateLocalStaysSilentForAnUnknownProvider(t *testing.T) {
	validator := New(WithCommandRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("an unknown provider must not run anything")
		return nil, nil
	}))
	result := validator.ValidateLocal(context.Background(), "some-future-provider", ResolveOptions{Env: envFrom(nil)})
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

// I2, end to end: a malformed probe is our bug and must be downgraded to
// unknown rather than reported as the user's credential being rejected.
func TestValidateLocalDowngradesResolverBugsToUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"x-api-key header is required"}}`))
	}))
	defer server.Close()

	validator := New(WithHTTPClient(server.Client()))
	validator.endpoint.anthropic = server.URL
	result := validator.ValidateLocal(context.Background(), "firstParty", ResolveOptions{
		Env: envFrom(map[string]string{"ANTHROPIC_API_KEY": "present-but-somehow-not-sent"}),
	})
	if result.State == StateInvalid {
		t.Fatal("a malformed probe must never be reported as a rejected credential")
	}
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

// A genuine rejection must survive that downgrade.
func TestValidateLocalKeepsGenuineRejections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"API key is invalid."}}`))
	}))
	defer server.Close()

	validator := New(WithHTTPClient(server.Client()))
	validator.endpoint.anthropic = server.URL
	result := validator.ValidateLocal(context.Background(), "firstParty", ResolveOptions{
		Env: envFrom(map[string]string{"ANTHROPIC_API_KEY": "sk-ant-revoked"}),
	})
	if result.State != StateInvalid {
		t.Fatalf("state = %q (%s), want invalid", result.State, result.Detail)
	}
	if result.Source != "ANTHROPIC_API_KEY" {
		t.Fatalf("source = %q, want the env var that supplied the credential", result.Source)
	}
}

// The cache is keyed on the credential, not only on time: swapping a revoked
// key for a working one must not be answered from the old verdict.
func TestCacheKeyedOnFingerprint(t *testing.T) {
	cache := NewCache(time.Minute)
	revoked := Result{State: StateInvalid, Fingerprint: Fingerprint("revoked-key")}
	cache.Put("claude-code", revoked)

	if _, ok := cache.Get("claude-code", Fingerprint("revoked-key")); !ok {
		t.Fatal("the same credential must hit the cache")
	}
	if _, ok := cache.Get("claude-code", Fingerprint("a-new-working-key")); ok {
		t.Fatal("a different credential must miss: the stored verdict is not about it")
	}
	if _, ok := cache.Get("claude-code", ""); ok {
		t.Fatal("an unresolved credential must never match a cached verdict")
	}
}

func TestCacheExpiresAndInvalidates(t *testing.T) {
	cache := NewCache(time.Minute)
	now := time.Now()
	cache.now = func() time.Time { return now }
	cache.Put("claude-code", Result{State: StateValid, Fingerprint: Fingerprint("k")})

	if _, ok := cache.Get("claude-code", Fingerprint("k")); !ok {
		t.Fatal("a fresh entry must hit")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := cache.Get("claude-code", Fingerprint("k")); ok {
		t.Fatal("an expired entry must miss")
	}

	now = time.Now()
	cache.Put("claude-code", Result{State: StateValid, Fingerprint: Fingerprint("k")})
	// A runtime 401 contradicts whatever is stored.
	cache.Invalidate("claude-code")
	if _, ok := cache.Get("claude-code", Fingerprint("k")); ok {
		t.Fatal("a runtime rejection must drop the cached verdict")
	}
}

// Caching an unknown would suppress the retry that might actually answer the
// question, turning one bad network moment into minutes of silence.
func TestCacheDoesNotStoreUnknowns(t *testing.T) {
	cache := NewCache(time.Minute)
	cache.Put("claude-code", Result{State: StateUnknown, Fingerprint: Fingerprint("k")})
	if _, ok := cache.Peek("claude-code"); ok {
		t.Fatal("unknown verdicts must not be cached")
	}
}

// The nil cache must behave as a permanent miss so callers need no nil checks.
func TestNilCacheIsSafe(t *testing.T) {
	var cache *Cache
	cache.Put("claude-code", Result{State: StateValid})
	cache.Invalidate("claude-code")
	if _, ok := cache.Get("claude-code", "f"); ok {
		t.Fatal("a nil cache must always miss")
	}
}
