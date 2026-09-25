package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newExchangeTestClient builds a Client whose OAuth token endpoint points at a
// test server. Only the fields ExchangeOAuthCode touches need to be real.
func newExchangeTestClient(t *testing.T, webBaseURL string) *Client {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	client, err := New(Config{
		AppID:         1,
		AppSlug:       "ao-test",
		ClientID:      "Iv1.test",
		ClientSecret:  "secret",
		PrivateKeyPEM: string(pemBytes),
		PublicURL:     "https://api.example.com",
		APIBaseURL:    webBaseURL,
		WebBaseURL:    webBaseURL,
	}, http.DefaultClient)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

// The bundled installation+OAuth flow (CompleteInstallationOAuth) has no PKCE
// verifier because GitHub authorized during installation without our
// code_challenge. GitHub rejects the token exchange if code_verifier is present
// but empty, so ExchangeOAuthCode must OMIT the field entirely when there is no
// verifier, and include it only when one was actually issued.
func TestExchangeOAuthCodeVerifierPresence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		verifier    string
		wantPresent bool
	}{
		{name: "empty verifier omits the field", verifier: "", wantPresent: false},
		{name: "whitespace verifier omits the field", verifier: "   ", wantPresent: false},
		{name: "real verifier includes the field", verifier: "pkce-code-verifier", wantPresent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Errorf("decode exchange body: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"gho_test"}`))
			}))
			defer server.Close()

			client := newExchangeTestClient(t, server.URL)
			token, err := client.ExchangeOAuthCode(context.Background(), "the-code", tc.verifier)
			if err != nil {
				t.Fatalf("ExchangeOAuthCode: %v", err)
			}
			if token != "gho_test" {
				t.Fatalf("token = %q, want gho_test", token)
			}
			if _, present := body["code_verifier"]; present != tc.wantPresent {
				t.Fatalf("code_verifier present = %v, want %v (body=%v)", present, tc.wantPresent, body)
			}
			// The code always rides the exchange regardless of the verifier.
			if got, _ := body["code"].(string); got != "the-code" {
				t.Fatalf("code = %q, want the-code", got)
			}
		})
	}
}
