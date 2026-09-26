package browsercontract

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// Authority issues and verifies unguessable per-session browser capabilities.
// It is intentionally stateless so process replacement does not invalidate
// capabilities held by surviving clients.
type Authority struct{}

// NewAuthority returns a stateless browser capability authority.
func NewAuthority() *Authority { return &Authority{} }

// Issue mints a fresh capability for one session launch.
func (a *Authority) Issue(sessionID string) (token, verifier string, err error) {
	if a == nil || sessionID == "" {
		return "", "", fmt.Errorf("issue browser capability: session id is required")
	}
	raw := make([]byte, sha256.Size)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate browser capability: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, capabilityVerifier(sessionID, token), nil
}

// Valid compares the presented token with the durable verifier in constant
// time. Binding the digest to the session id prevents a verifier copied
// between rows from authorizing the copied-to session.
func (a *Authority) Valid(sessionID, token, verifier string) bool {
	if a == nil || sessionID == "" || token == "" || verifier == "" {
		return false
	}
	expected := capabilityVerifier(sessionID, token)
	return hmac.Equal([]byte(expected), []byte(verifier))
}

func capabilityVerifier(sessionID, token string) string {
	h := sha256.New()
	_, _ = h.Write([]byte("ao-browser-capability-verifier-v1\x00"))
	_, _ = h.Write([]byte(sessionID))
	_, _ = h.Write([]byte{'\x00'})
	_, _ = h.Write([]byte(token))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
