// Package worker defines the authenticated protocol identity used by the
// process running inside one sandbox. A worker holds no permanent credential:
// it exchanges a one-time bootstrap ticket for a short-lived token, then
// renews that token on every heartbeat.
package worker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidToken indicates that a worker token is malformed, forged, or expired.
var ErrInvalidToken = errors.New("invalid or expired worker token")

// DefaultTokenTTL is how long an issued worker token stays valid.
const DefaultTokenTTL = 15 * time.Minute

const tokenPrefix = "aow1"

// Claims describes an authenticated worker identity.
type Claims struct {
	OrgID     string   `json:"orgId"`
	SessionID string   `json:"sessionId"`
	WorkerID  string   `json:"workerId"`
	Epoch     int64    `json:"epoch"`
	ExpiresAt int64    `json:"expiresAt"`
	Scopes    []string `json:"scopes"`
}

// TokenManager issues and verifies signed worker tokens.
type TokenManager struct {
	key []byte
	now func() time.Time
}

// NewTokenManager creates a worker token manager over a copy of key.
func NewTokenManager(key []byte) *TokenManager {
	return &TokenManager{key: append([]byte(nil), key...), now: time.Now}
}

// Issue signs claims with the requested lifetime.
func (m *TokenManager) Issue(claims Claims, ttl time.Duration) (string, error) {
	if claims.OrgID == "" || claims.SessionID == "" || claims.WorkerID == "" || claims.Epoch <= 0 {
		return "", errors.New("worker token claims are incomplete")
	}
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	claims.ExpiresAt = m.now().Add(ttl).Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode worker claims: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := m.sign(encoded)
	return tokenPrefix + "." + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// Verify validates a worker token and returns its claims. A valid signature is
// necessary but not sufficient: callers must also confirm the claimed epoch is
// still the live one, so a worker replaced by a recreate cannot act.
func (m *TokenManager) Verify(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != tokenPrefix {
		return Claims{}, ErrInvalidToken
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, m.sign(parts[1])) {
		return Claims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if claims.ExpiresAt <= m.now().Unix() ||
		claims.OrgID == "" ||
		claims.SessionID == "" ||
		claims.WorkerID == "" ||
		claims.Epoch <= 0 {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}

func (m *TokenManager) sign(encodedPayload string) []byte {
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write([]byte(tokenPrefix + "."))
	_, _ = mac.Write([]byte(encodedPayload))
	return mac.Sum(nil)
}

// HasScope reports whether claims grant the expected capability.
func HasScope(claims Claims, expected string) bool {
	for _, scope := range claims.Scopes {
		if scope == expected {
			return true
		}
	}
	return false
}

// NextWorkerID derives a worker identity from a session and its connection epoch.
func NextWorkerID(sessionID string, epoch int64) string {
	return sessionID + ":" + strconv.FormatInt(epoch, 10)
}
