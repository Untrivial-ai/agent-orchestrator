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

func TestWorkspaceTokenCannotBeUsedAsDesktopAccessToken(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	manager, err := NewAccessTokenManager([]byte("0123456789abcdef0123456789abcdef"), "issuer", "desktop", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	token, err := manager.IssueWorkspace("user-1", "org-1", "workspace-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := manager.VerifyWorkspace(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-1" || claims.OrgID != "org-1" || claims.WorkspaceID != "workspace-1" {
		t.Fatalf("claims = %#v", claims)
	}
	if _, err = manager.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("workspace token accepted as access token: %v", err)
	}
	manager.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err = manager.VerifyWorkspace(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired workspace token error = %v", err)
	}
}
