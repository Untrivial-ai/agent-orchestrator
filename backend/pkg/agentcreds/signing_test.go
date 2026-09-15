package agentcreds

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseAWSKeysRejectsIncompleteCredentials(t *testing.T) {
	for _, packed := range []string{"", "only-access-key", "\nsecret", "access\n"} {
		if _, err := parseAWSKeys(packed); err == nil {
			t.Fatalf("parseAWSKeys(%q) succeeded, want an error", packed)
		}
	}
}

// A signed Bedrock probe must carry a SigV4 Authorization header, not the raw
// key.
func TestBedrockSigV4ProbeIsSignedAndNeverSendsTheRawKey(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"modelSummaries":[{"modelId":"anthropic.claude-x","providerName":"Anthropic"}]}`))
	}))
	defer server.Close()

	result := New(server.Client()).Validate(context.Background(), Credential{
		Kind: KindAWSSigV4, Secret: "AKIAEXAMPLE\nsuper-secret-key\nsession-token", Source: "AWS_ACCESS_KEY_ID",
		Provider: ProviderBedrock, Region: "us-east-1", BaseURL: server.URL,
	})
	if result.State != StateUnknown || len(result.Models) != 1 {
		t.Fatalf("state/models = %q/%v, want catalog-only unknown with one model", result.State, result.Models)
	}
	authorization := got.Get("Authorization")
	if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("Authorization = %q, want a SigV4 signature", authorization)
	}
	if strings.Contains(authorization, "super-secret-key") {
		t.Fatal("the secret access key must never appear on the wire")
	}
	if got.Get("X-Amz-Security-Token") != "session-token" || !strings.Contains(authorization, "x-amz-security-token") {
		t.Fatal("the AWS signer must send and sign the session token")
	}
}

// Vertex service-account keys are not bearer tokens: they must be signed into
// a JWT and exchanged first.
func TestVertexServiceAccountExchangesBeforeProbing(t *testing.T) {
	privateKeyPEM, _ := generateTestRSAKey(t)

	var assertion string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		assertion = r.Form.Get("assertion")
		if grant := r.Form.Get("grant_type"); grant != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Fatalf("grant_type = %q", grant)
		}
		_, _ = w.Write([]byte(`{"access_token":"exchanged-token","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	var probeAuth string
	vertexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"publisherModels":[{"name":"publishers/anthropic/models/claude-opus-4-5@20251101"}]}`))
	}))
	defer vertexServer.Close()

	validator := New(vertexServer.Client())

	keyJSON := serviceAccountJSON(t, "probe@example.iam.gserviceaccount.com", privateKeyPEM, "proj", tokenServer.URL)
	result := validator.Validate(context.Background(), Credential{
		Kind: KindGoogleServiceAccount, Secret: keyJSON, Source: "GOOGLE_APPLICATION_CREDENTIALS",
		Provider: ProviderVertex, Region: "us-east5", Project: "proj", BaseURL: vertexServer.URL,
	})
	if result.State != StateValid {
		t.Fatalf("state = %q (%s)", result.State, result.Detail)
	}
	if probeAuth != "Bearer exchanged-token" {
		t.Fatalf("probe Authorization = %q, want the exchanged token", probeAuth)
	}

	if parts := strings.Split(assertion, "."); len(parts) != 3 {
		t.Fatalf("assertion is not a compact JWT: %q", assertion)
	}
}

// The three non-service-account credential file types are chain cases. Failing
// to recognize one must be Unknown, not a rejection of the user's credentials.
func TestVertexRejectsChainCredentialFileTypesAsUnknown(t *testing.T) {
	for _, fileType := range []string{"authorized_user", "external_account", "impersonated_service_account"} {
		t.Run(fileType, func(t *testing.T) {
			key, _ := json.Marshal(map[string]string{"type": fileType})
			result := New(nil).Validate(context.Background(), Credential{
				Kind: KindGoogleServiceAccount, Secret: string(key),
				Provider: ProviderVertex, Region: "us-east5", Project: "p",
			})
			if result.State != StateUnknown {
				t.Fatalf("state = %q, want unknown", result.State)
			}
		})
	}
}

// A rejected key exchange is a rejected credential and must not be lost as a
// generic transport failure.
func TestVertexRejectedKeyExchangeIsInvalid(t *testing.T) {
	privateKeyPEM, _ := generateTestRSAKey(t)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))
	}))
	defer tokenServer.Close()

	validator := New(tokenServer.Client())
	result := validator.Validate(context.Background(), Credential{
		Kind:   KindGoogleServiceAccount,
		Secret: serviceAccountJSON(t, "probe@example.iam.gserviceaccount.com", privateKeyPEM, "p", tokenServer.URL),
		// The probe URL is never reached; the exchange fails first.
		Provider: ProviderVertex, Region: "us-east5", Project: "p", BaseURL: "http://127.0.0.1:1",
	})
	if result.State != StateInvalid {
		t.Fatalf("state = %q, want invalid", result.State)
	}
	if result.Err == nil {
		t.Fatal("a rejected exchange must carry its error")
	}
}

func generateTestRSAKey(t *testing.T) (pemString string, key *rsa.PrivateKey) {
	t.Helper()
	// 1024 bits: this is a throwaway test key, and larger sizes make the suite
	// noticeably slower for no added coverage.
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	return string(pem.EncodeToMemory(block)), key
}

func serviceAccountJSON(t *testing.T, email, privateKeyPEM, project, tokenURL string) string {
	t.Helper()
	data, err := json.Marshal(map[string]string{
		"type": "service_account", "client_email": email,
		"private_key": privateKeyPEM, "project_id": project, "token_uri": tokenURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
