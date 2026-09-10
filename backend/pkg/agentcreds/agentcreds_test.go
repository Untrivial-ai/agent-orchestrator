package agentcreds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The classification table is the core of the package, and two of its rows are
// the ones most likely to be "corrected" into bugs later: 429 is a pass, and
// an unrecognized status is unknown rather than a failure.
func TestClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   State
	}{
		{"ok", http.StatusOK, `{"data":[{"id":"claude-opus-4-5"}]}`, StateValid},
		{"rate limited still authenticated", http.StatusTooManyRequests, ``, StateValid},
		{"unauthorized", http.StatusUnauthorized, `{"error":{"message":"API key is invalid."}}`, StateInvalid},
		{"forbidden", http.StatusForbidden, `{"error":{"message":"no access"}}`, StateInvalid},
		{"server error says nothing about the credential", http.StatusInternalServerError, ``, StateUnknown},
		{"not found says nothing about the credential", http.StatusNotFound, ``, StateUnknown},
		{"bad gateway says nothing about the credential", http.StatusBadGateway, ``, StateUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			result := New(WithHTTPClient(server.Client())).Validate(context.Background(), Credential{
				Kind: KindAPIKey, Secret: "sk-ant-test", Source: "ANTHROPIC_API_KEY",
				Provider: ProviderFirstParty, BaseURL: server.URL,
			})
			if result.State != tc.want {
				t.Fatalf("state = %q (%s), want %q", result.State, result.Detail, tc.want)
			}
		})
	}
}

// The header must follow the credential's Kind. A token sent under the wrong
// header is rejected for the wrong reason, and that rejection is
// indistinguishable from a revoked credential.
func TestHeaderFollowsCredentialKind(t *testing.T) {
	tests := []struct {
		kind       Kind
		wantHeader string
		wantValue  string
	}{
		{KindAPIKey, "X-Api-Key", "secret-value"},
		{KindOAuthToken, "Authorization", "Bearer secret-value"},
		{KindAuthToken, "Authorization", "Bearer secret-value"},
	}
	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			var got *http.Request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Clone(context.Background())
				_, _ = w.Write([]byte(`{"data":[]}`))
			}))
			defer server.Close()

			New(WithHTTPClient(server.Client())).Validate(context.Background(), Credential{
				Kind: tc.kind, Secret: "secret-value", Provider: ProviderFirstParty, BaseURL: server.URL,
			})
			if got.Header.Get(tc.wantHeader) != tc.wantValue {
				t.Fatalf("%s = %q, want %q", tc.wantHeader, got.Header.Get(tc.wantHeader), tc.wantValue)
			}
			if got.Header.Get("anthropic-version") != anthropicAPIVersion {
				t.Fatal("every first-party request must carry anthropic-version")
			}
		})
	}
}

// I2: an empty secret must never reach the provider. Sending one produces
// "x-api-key header is required", which reads exactly like a rejection and
// would tell a user with working credentials to sign in again.
func TestEmptySecretIsNeverProbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("an empty credential must not produce a request")
	}))
	defer server.Close()

	result := New(WithHTTPClient(server.Client())).Validate(context.Background(), Credential{
		Kind: KindAPIKey, Secret: "   ", Provider: ProviderFirstParty, BaseURL: server.URL,
	})
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want %q", result.State, StateUnknown)
	}
}

// The provider's own message is what makes an error actionable: Anthropic
// returns a distinct one per credential class.
func TestRejectionCarriesTheProviderMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"OAuth access token is invalid."}}`))
	}))
	defer server.Close()

	result := New(WithHTTPClient(server.Client())).Validate(context.Background(), Credential{
		Kind: KindOAuthToken, Secret: "sk-ant-oat01-x", Provider: ProviderFirstParty, BaseURL: server.URL,
	})
	if result.State != StateInvalid {
		t.Fatalf("state = %q, want invalid", result.State)
	}
	if !errors.Is(result.Err, ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", result.Err)
	}
	if want := "OAuth access token is invalid."; !contains(result.Detail, want) {
		t.Fatalf("detail = %q, want it to carry %q", result.Detail, want)
	}
}

// A resolver bug must be recognizable so it can be downgraded to unknown.
func TestIsResolverBug(t *testing.T) {
	bugs := []string{
		"Anthropic rejected the credential: x-api-key header is required",
		"Foundry rejected the credential: missing api key",
	}
	for _, detail := range bugs {
		if !IsResolverBug(detail) {
			t.Fatalf("IsResolverBug(%q) = false, want true", detail)
		}
	}
	real := []string{
		"Anthropic rejected the credential: API key is invalid.",
		"Anthropic rejected the credential: OAuth access token is invalid.",
	}
	for _, detail := range real {
		if IsResolverBug(detail) {
			t.Fatalf("IsResolverBug(%q) = true, want false — this is a genuine rejection", detail)
		}
	}
}

func TestFingerprintIsShortAndNotTheSecret(t *testing.T) {
	const secret = "sk-ant-api03-a-very-long-and-secret-value"
	fingerprint := Fingerprint(secret)
	if len(fingerprint) != 12 {
		t.Fatalf("fingerprint = %q, want 12 characters", fingerprint)
	}
	if contains(fingerprint, "sk-ant") || contains(secret, fingerprint) {
		t.Fatal("the fingerprint must not reveal any part of the secret")
	}
	if Fingerprint("") != "" || Fingerprint("  ") != "" {
		t.Fatal("an empty secret has no fingerprint")
	}
	if Fingerprint(secret) != Fingerprint(secret) {
		t.Fatal("fingerprints must be stable")
	}
	if Fingerprint(secret) == Fingerprint(secret+"x") {
		t.Fatal("a changed secret must change the fingerprint")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// A 200 with a body we cannot parse is still an acceptance. The model list is
// a bonus for the surfaces that do not gate on entitlement, so failing to read
// it must not retract the answer the status code already gave.
func TestUnparsableBodyDoesNotRetractAnAcceptance(t *testing.T) {
	for _, body := range []string{"", "not json at all", "<html>proxy interstitial</html>"} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			result := New(WithHTTPClient(server.Client())).Validate(context.Background(), Credential{
				Kind: KindAPIKey, Secret: "k", Provider: ProviderFirstParty, BaseURL: server.URL,
			})
			if !result.Valid() {
				t.Fatalf("state = %q (%s), want valid", result.State, result.Detail)
			}
			if len(result.Models) != 0 {
				t.Fatalf("models = %v, want none", result.Models)
			}
		})
	}
}

// But where the catalog IS the verdict, an unreadable body means entitlement
// cannot be confirmed, and the safe answer is unknown.
func TestUnparsableBodyIsUnknownWhereEntitlementMatters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json at all"))
	}))
	defer server.Close()

	result := New(WithHTTPClient(server.Client())).Validate(context.Background(), Credential{
		Kind: KindAuthToken, Secret: "k", Provider: ProviderBedrock,
		Region: "us-east-1", BaseURL: server.URL,
	})
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}
