package auth

import (
	"errors"
	"testing"
	"time"
)

func TestAccessTokenManagerValidatesAudienceAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	manager, err := NewAccessTokenManager(
		[]byte("0123456789abcdef0123456789abcdef"),
		"ao-cloud-test",
		"ao-desktop-test",
		15*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	token, expiresAt, err := manager.Issue("user-id")
	if err != nil {
		t.Fatal(err)
	}
	if expiresAt != now.Add(15*time.Minute) {
		t.Fatalf("expiry = %s", expiresAt)
	}
	claims, err := manager.Verify(token)
	if err != nil || claims.Subject != "user-id" {
		t.Fatalf("claims = %#v, error = %v", claims, err)
	}

	otherAudience, err := NewAccessTokenManager(
		[]byte("0123456789abcdef0123456789abcdef"),
		"ao-cloud-test",
		"other-client",
		15*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	otherAudience.now = manager.now
	if _, err := otherAudience.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong audience error = %v", err)
	}

	manager.now = func() time.Time { return now.Add(16 * time.Minute) }
	if _, err := manager.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token error = %v", err)
	}
}

func TestRefreshTokensAreOpaqueAndHashed(t *testing.T) {
	token, digest, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 32 {
		t.Fatalf("digest length = %d", len(digest))
	}
	if got := HashToken(token); string(got) != string(digest) {
		t.Fatal("token digest is not stable")
	}
	other, _, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == other {
		t.Fatal("refresh token was reused")
	}
}
