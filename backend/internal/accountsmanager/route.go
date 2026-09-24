package accountsmanager

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	routeTokenPrefix        = "ao-codex-route-v1." // #nosec G101 -- public capability-token namespace, not a credential.
	routeAccessProviderType = "ao-codex-route"
	routeAccessProviderName = "ao-codex-route"
)

type routeClaims struct {
	Version   int    `json:"v"`
	SessionID string `json:"session_id"`
}

// routeCapability authenticates a short-lived child-process capability and
// remembers which AO session presented it. The account is deliberately not in
// the token: switching an account updates the session mapping atomically, so a
// running Codex process can use the new account on its next request.
type routeCapability struct {
	aead cipher.AEAD

	mu     sync.Mutex
	claims map[string]routeClaims // caller scope -> claims
	tokens map[string]string      // AO session id -> stable child capability
}

func newRouteCapability(key []byte) (*routeCapability, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("routing key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize route capability: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize route capability: %w", err)
	}
	return &routeCapability{
		aead:   aead,
		claims: make(map[string]routeClaims),
		tokens: make(map[string]string),
	}, nil
}

func (c *routeCapability) Identifier() string { return routeAccessProviderName }

func (c *routeCapability) Mint(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id is required for a route capability")
	}
	c.mu.Lock()
	if token := c.tokens[sessionID]; token != "" {
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()
	claims, err := json.Marshal(routeClaims{Version: 1, SessionID: sessionID})
	if err != nil {
		return "", fmt.Errorf("encode route capability: %w", err)
	}
	nonce := make([]byte, c.aead.NonceSize(), c.aead.NonceSize()+len(claims)+c.aead.Overhead())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate route capability: %w", err)
	}
	encoded := c.aead.Seal(nonce, nonce, claims, []byte(routeTokenPrefix))
	token := routeTokenPrefix + base64.RawURLEncoding.EncodeToString(encoded)
	digest := sha256.Sum256([]byte(token))
	principal := routeAccessProviderName + ":" + hex.EncodeToString(digest[:])
	scope := coresession.CallerScope(principal)
	c.mu.Lock()
	if existing := c.tokens[sessionID]; existing != "" {
		c.mu.Unlock()
		return existing, nil
	}
	c.tokens[sessionID] = token
	c.claims[scope] = routeClaims{Version: 1, SessionID: sessionID}
	c.mu.Unlock()
	return token, nil
}

func (c *routeCapability) Authenticate(_ context.Context, request *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	token := routeTokenFromRequest(request)
	if !strings.HasPrefix(token, routeTokenPrefix) {
		return nil, sdkaccess.NewNotHandledError()
	}
	claims, err := c.open(token)
	if err != nil {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	digest := sha256.Sum256([]byte(token))
	principal := routeAccessProviderName + ":" + hex.EncodeToString(digest[:])
	scope := coresession.CallerScope(principal)
	c.mu.Lock()
	c.claims[scope] = claims
	c.mu.Unlock()
	return &sdkaccess.Result{Provider: routeAccessProviderName, Principal: principal}, nil
}

func (c *routeCapability) sessionForScope(scope string) (string, bool) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.claims[scope]
	if !ok {
		return "", false
	}
	return entry.SessionID, true
}

func (c *routeCapability) open(token string) (routeClaims, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, routeTokenPrefix))
	if err != nil || len(encoded) <= c.aead.NonceSize() {
		return routeClaims{}, errors.New("invalid route capability")
	}
	nonce, ciphertext := encoded[:c.aead.NonceSize()], encoded[c.aead.NonceSize():]
	payload, err := c.aead.Open(nil, nonce, ciphertext, []byte(routeTokenPrefix))
	if err != nil {
		return routeClaims{}, errors.New("invalid route capability")
	}
	var claims routeClaims
	if err = json.Unmarshal(payload, &claims); err != nil || claims.Version != 1 || strings.TrimSpace(claims.SessionID) == "" {
		return routeClaims{}, errors.New("invalid route capability")
	}
	claims.SessionID = strings.TrimSpace(claims.SessionID)
	return claims, nil
}

func routeTokenFromRequest(request *http.Request) string {
	if request == nil {
		return ""
	}
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if parts := strings.Fields(authorization); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(request.Header.Get("x-api-key"))
}

type exactRouteSelector struct {
	capability *routeCapability
	routes     *routeState
	fallback   coreauth.Selector
}

func newExactRouteSelector(capability *routeCapability, routes *routeState, fallback coreauth.Selector) *exactRouteSelector {
	if fallback == nil {
		fallback = &coreauth.FillFirstSelector{}
	}
	return &exactRouteSelector{capability: capability, routes: routes, fallback: fallback}
}

func (s *exactRouteSelector) Pick(ctx context.Context, provider, model string, opts coreexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	if s == nil {
		return (&coreauth.FillFirstSelector{}).Pick(ctx, provider, model, opts, auths)
	}
	if s.capability == nil || s.routes == nil {
		return s.fallback.Pick(ctx, provider, model, opts, auths)
	}
	scope, _ := opts.Metadata[coreexecutor.CallerScopeMetadataKey].(string)
	if sessionID, ok := s.capability.sessionForScope(scope); ok {
		provider = strings.TrimSpace(provider)
		// The Responses endpoint asks the CLIProxy conductor to select from
		// the mixed provider pool even when every registered credential is
		// Codex. A scoped route is still restricted to the pinned Codex auth.
		if !strings.EqualFold(provider, "codex") && !strings.EqualFold(provider, "mixed") {
			return nil, ports.ErrCodexProxyAccountUnavailable
		}
		accountID, ok := s.routes.accountForSession(sessionID)
		if !ok {
			return nil, ports.ErrCodexProxyAccountUnavailable
		}
		for _, auth := range auths {
			if auth != nil && auth.ID == accountID && exactRouteAuthUsable(auth, model, time.Now()) {
				return auth, nil
			}
		}
		return nil, ports.ErrCodexProxyAccountUnavailable
	}
	return s.fallback.Pick(ctx, provider, model, opts, auths)
}

func exactRouteAuthUsable(auth *coreauth.Auth, model string, now time.Time) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.Disabled || auth.Unavailable || auth.Status != coreauth.StatusActive {
		return false
	}
	if !auth.NextRetryAfter.IsZero() && auth.NextRetryAfter.After(now) {
		return false
	}
	for key, state := range auth.ModelStates {
		if state == nil || !strings.EqualFold(strings.TrimSpace(key), strings.TrimSpace(model)) {
			continue
		}
		return !state.Unavailable && state.Status == coreauth.StatusActive && (state.NextRetryAfter.IsZero() || !state.NextRetryAfter.After(now))
	}
	return true
}
