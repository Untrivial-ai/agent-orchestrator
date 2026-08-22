// Package auth verifies external identities and issues AO session credentials.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const refreshTokenPrefix = "ao_refresh_"

// ErrInvalidToken means a supplied identity, access, or refresh token cannot
// be trusted.
var ErrInvalidToken = errors.New("invalid token")

// AccessTokenManager issues and verifies short-lived AO JWT access tokens.
// Tokens contain only the AO user ID; organization authorization remains a
// live PostgreSQL lookup.
type AccessTokenManager struct {
	key      []byte
	issuer   string
	audience string
	ttl      time.Duration
	now      func() time.Time
}

// AccessClaims are the registered claims carried by an AO access token.
type AccessClaims struct {
	jwt.RegisteredClaims
}

// WorkspaceClaims authorize exactly one cloud project's coordinator daemon to
// manage session-scoped runtimes. They deliberately use a different audience
// from desktop access tokens, so a sandbox credential cannot call user APIs.
type WorkspaceClaims struct {
	OrgID       string `json:"org_id"`
	WorkspaceID string `json:"workspace_id"`
	jwt.RegisteredClaims
}

// NewAccessTokenManager validates the signing configuration and constructs a
// manager. HMAC keys shorter than 256 bits are rejected.
func NewAccessTokenManager(key []byte, issuer, audience string, ttl time.Duration) (*AccessTokenManager, error) {
	if len(key) < 32 {
		return nil, errors.New("AO access-token signing key must be at least 32 bytes")
	}
	issuer = strings.TrimSpace(issuer)
	audience = strings.TrimSpace(audience)
	if issuer == "" || audience == "" {
		return nil, errors.New("AO access-token issuer and audience are required")
	}
	if ttl <= 0 {
		return nil, errors.New("AO access-token lifetime must be positive")
	}
	return &AccessTokenManager{
		key:      append([]byte(nil), key...),
		issuer:   issuer,
		audience: audience,
		ttl:      ttl,
		now:      time.Now,
	}, nil
}

// Issue signs an access token for one AO user and returns its expiry.
func (m *AccessTokenManager) Issue(userID string) (string, time.Time, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", time.Time{}, errors.New("access-token subject is required")
	}
	now := m.now().UTC()
	expiresAt := now.Add(m.ttl)
	claims := AccessClaims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer:    m.issuer,
		Subject:   userID,
		Audience:  jwt.ClaimStrings{m.audience},
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.key)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// Verify validates an AO access token and returns its registered claims.
func (m *AccessTokenManager) Verify(token string) (AccessClaims, error) {
	claims := AccessClaims{}
	parsed, err := jwt.ParseWithClaims(
		strings.TrimSpace(token),
		&claims,
		func(candidate *jwt.Token) (any, error) {
			if candidate.Method != jwt.SigningMethodHS256 {
				return nil, ErrInvalidToken
			}
			return m.key, nil
		},
		jwt.WithAudience(m.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithIssuer(m.issuer),
		jwt.WithLeeway(30*time.Second),
		jwt.WithTimeFunc(m.now),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil || !parsed.Valid || strings.TrimSpace(claims.Subject) == "" {
		return AccessClaims{}, ErrInvalidToken
	}
	return claims, nil
}

// IssueWorkspace signs a capability for one coordinator daemon. The token's
// subject remains the owning AO user so ordinary tenant RLS can be applied
// after verification without adding a privileged database path.
func (m *AccessTokenManager) IssueWorkspace(userID, orgID, workspaceID string, ttl time.Duration) (string, error) {
	userID = strings.TrimSpace(userID)
	orgID = strings.TrimSpace(orgID)
	workspaceID = strings.TrimSpace(workspaceID)
	if userID == "" || orgID == "" || workspaceID == "" || ttl <= 0 {
		return "", errors.New("workspace token subject, organization, workspace, and lifetime are required")
	}
	now := m.now().UTC()
	claims := WorkspaceClaims{
		OrgID: orgID, WorkspaceID: workspaceID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: m.issuer, Subject: userID,
			Audience: jwt.ClaimStrings{m.audience + ":workspace"},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.key)
	if err != nil {
		return "", err
	}
	return token, nil
}

// VerifyWorkspace validates a coordinator capability minted by IssueWorkspace.
func (m *AccessTokenManager) VerifyWorkspace(token string) (WorkspaceClaims, error) {
	claims := WorkspaceClaims{}
	parsed, err := jwt.ParseWithClaims(
		strings.TrimSpace(token), &claims,
		func(candidate *jwt.Token) (any, error) {
			if candidate.Method != jwt.SigningMethodHS256 {
				return nil, ErrInvalidToken
			}
			return m.key, nil
		},
		jwt.WithAudience(m.audience+":workspace"), jwt.WithExpirationRequired(), jwt.WithIssuedAt(),
		jwt.WithIssuer(m.issuer), jwt.WithLeeway(30*time.Second), jwt.WithTimeFunc(m.now),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil || !parsed.Valid || strings.TrimSpace(claims.Subject) == "" ||
		strings.TrimSpace(claims.OrgID) == "" || strings.TrimSpace(claims.WorkspaceID) == "" {
		return WorkspaceClaims{}, ErrInvalidToken
	}
	return claims, nil
}

// NewRefreshToken creates an opaque refresh token and its SHA-256 digest. Only
// the digest is persisted by the control plane.
func NewRefreshToken() (string, []byte, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", nil, err
	}
	token := refreshTokenPrefix + base64.RawURLEncoding.EncodeToString(value)
	return token, HashToken(token), nil
}

// HashToken returns the stable digest used to look up an opaque refresh token.
func HashToken(token string) []byte {
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return digest[:]
}
