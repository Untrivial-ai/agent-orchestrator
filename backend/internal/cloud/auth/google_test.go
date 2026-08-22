package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestGoogleVerifierRequiresAllowedAudienceAndVerifiedEmail(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "google-test-key"
	publicKey := jose.JSONWebKey{
		Key:       &privateKey.PublicKey,
		KeyID:     keyID,
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{publicKey}}); err != nil {
			t.Errorf("encode JWKS: %v", err)
		}
	}))
	defer jwksServer.Close()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{
			Key: privateKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig",
		}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(audience string, verified bool) string {
		t.Helper()
		token, issueErr := jwt.Signed(signer).
			Claims(jwt.Claims{
				Issuer:   GoogleIssuer,
				Subject:  "google-subject",
				Audience: jwt.Audience{audience},
				IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
				Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
			}).
			Claims(map[string]any{
				"email":          "Person@Example.com",
				"email_verified": verified,
				"name":           "Person Example",
				"azp":            audience,
			}).
			Serialize()
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		return token
	}
	verifier, err := NewGoogleVerifier(
		context.Background(), GoogleIssuer, jwksServer.URL, []string{"desktop-client"},
	)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := verifier.Verify(context.Background(), issue("desktop-client", true))
	if err != nil {
		t.Fatal(err)
	}
	if principal.Provider != "google" || principal.ExternalID != "google-subject" ||
		principal.Email != "person@example.com" || principal.DisplayName != "Person Example" {
		t.Fatalf("principal = %#v", principal)
	}
	for _, token := range []string{
		issue("other-client", true),
		issue("desktop-client", false),
	} {
		if _, err := verifier.Verify(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("invalid Google token error = %v", err)
		}
	}
}
