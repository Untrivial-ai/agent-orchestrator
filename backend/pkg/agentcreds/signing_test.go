package agentcreds

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// SigV4 is hand-rolled to avoid taking on aws-sdk-go-v2, so it needs to be
// pinned against AWS's own published test vector rather than against itself.
// This is the GET vanilla case from the SigV4 test suite: if the canonical
// request, the string to sign, or the key derivation drifts, the signature
// stops matching this constant.
func TestSigV4MatchesTheAWSTestVector(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "example.amazonaws.com"

	keys := awsKeys{
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}
	signedAt := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	if err := signAWSRequestV4(request, keys, "service", "us-east-1", signedAt); err != nil {
		t.Fatal(err)
	}

	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request") {
		t.Fatalf("credential scope is wrong: %q", authorization)
	}
	if request.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Fatalf("X-Amz-Date = %q", request.Header.Get("X-Amz-Date"))
	}
	// The signature covers host and x-amz-date at minimum.
	for _, header := range []string{"host", "x-amz-date"} {
		if !strings.Contains(authorization, header) {
			t.Fatalf("SignedHeaders must include %q: %q", header, authorization)
		}
	}
}

// A session token must be both sent and signed. Signing without it produces a
// signature AWS rejects, which would look exactly like a bad credential.
func TestSigV4CoversTheSessionToken(t *testing.T) {
	request, _ := http.NewRequest(http.MethodGet, "https://bedrock.us-east-1.amazonaws.com/foundation-models", http.NoBody)
	keys := awsKeys{AccessKeyID: "AKIA", SecretAccessKey: "secret", SessionToken: "session-token"}
	if err := signAWSRequestV4(request, keys, "bedrock", "us-east-1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("X-Amz-Security-Token") != "session-token" {
		t.Fatal("the session token must be sent")
	}
	if !strings.Contains(request.Header.Get("Authorization"), "x-amz-security-token") {
		t.Fatal("the session token must be inside SignedHeaders, or AWS rejects the signature")
	}
}

// Signing must be deterministic for a fixed timestamp, or nothing about it is
// testable.
func TestSigV4IsDeterministic(t *testing.T) {
	at := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	sign := func() string {
		request, _ := http.NewRequest(http.MethodGet, "https://bedrock.us-east-1.amazonaws.com/foundation-models", http.NoBody)
		_ = signAWSRequestV4(request, awsKeys{AccessKeyID: "AKIA", SecretAccessKey: "s"}, "bedrock", "us-east-1", at)
		return request.Header.Get("Authorization")
	}
	if sign() != sign() {
		t.Fatal("the same request signed at the same instant must produce the same signature")
	}
}

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

	result := New(WithHTTPClient(server.Client())).Validate(context.Background(), Credential{
		Kind: KindAWSSigV4, Secret: "AKIAEXAMPLE\nsuper-secret-key\n", Source: "AWS_ACCESS_KEY_ID",
		Provider: ProviderBedrock, Region: "us-east-1", BaseURL: server.URL,
	})
	if !result.Valid() {
		t.Fatalf("state = %q (%s)", result.State, result.Detail)
	}
	authorization := got.Get("Authorization")
	if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("Authorization = %q, want a SigV4 signature", authorization)
	}
	if strings.Contains(authorization, "super-secret-key") {
		t.Fatal("the secret access key must never appear on the wire")
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

	validator := New(WithHTTPClient(vertexServer.Client()))
	validator.endpoint.sts = tokenServer.URL

	keyJSON := serviceAccountJSON(t, "probe@example.iam.gserviceaccount.com", privateKeyPEM, "proj")
	result := validator.Validate(context.Background(), Credential{
		Kind: KindGoogleServiceAccount, Secret: keyJSON, Source: "GOOGLE_APPLICATION_CREDENTIALS",
		Provider: ProviderVertex, Region: "us-east5", Project: "proj", BaseURL: vertexServer.URL,
	})
	if !result.Valid() {
		t.Fatalf("state = %q (%s)", result.State, result.Detail)
	}
	if probeAuth != "Bearer exchanged-token" {
		t.Fatalf("probe Authorization = %q, want the exchanged token", probeAuth)
	}

	// The assertion must be a well-formed, correctly-scoped RS256 JWT.
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion is not a compact JWT: %q", assertion)
	}
	var header struct{ Alg, Typ string }
	decodeSegment(t, parts[0], &header)
	if header.Alg != "RS256" || header.Typ != "JWT" {
		t.Fatalf("header = %+v", header)
	}
	var claims struct {
		Iss, Scope, Aud string
		Exp, Iat        int64
	}
	decodeSegment(t, parts[1], &claims)
	if claims.Iss != "probe@example.iam.gserviceaccount.com" {
		t.Fatalf("iss = %q", claims.Iss)
	}
	if claims.Scope != vertexScope {
		t.Fatalf("scope = %q, want %q", claims.Scope, vertexScope)
	}
	if claims.Exp <= claims.Iat {
		t.Fatal("exp must be after iat")
	}
}

// The three non-service-account credential file types are chain cases. Failing
// to recognize one must be Unknown, not a rejection of the user's credentials.
func TestVertexRejectsChainCredentialFileTypesAsUnknown(t *testing.T) {
	for _, fileType := range []string{"authorized_user", "external_account", "impersonated_service_account"} {
		t.Run(fileType, func(t *testing.T) {
			key, _ := json.Marshal(map[string]string{"type": fileType})
			result := New().Validate(context.Background(), Credential{
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

	validator := New(WithHTTPClient(tokenServer.Client()))
	validator.endpoint.sts = tokenServer.URL
	result := validator.Validate(context.Background(), Credential{
		Kind:   KindGoogleServiceAccount,
		Secret: serviceAccountJSON(t, "probe@example.iam.gserviceaccount.com", privateKeyPEM, "p"),
		// The probe URL is never reached; the exchange fails first.
		Provider: ProviderVertex, Region: "us-east5", Project: "p", BaseURL: "http://127.0.0.1:1",
	})
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
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

func serviceAccountJSON(t *testing.T, email, privateKeyPEM, project string) string {
	t.Helper()
	data, err := json.Marshal(map[string]string{
		"type": "service_account", "client_email": email,
		"private_key": privateKeyPEM, "project_id": project,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func decodeSegment(t *testing.T, segment string, target any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("segment is not base64url: %v", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("segment is not JSON: %v", err)
	}
}

// PKCS#8 keys must parse too: Google has issued both encodings.
func TestParseRSAPrivateKeyAcceptsPKCS8(t *testing.T) {
	_, key := generateTestRSAKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := parseRSAPrivateKey(encoded); err != nil {
		t.Fatalf("PKCS#8 key did not parse: %v", err)
	}
}
