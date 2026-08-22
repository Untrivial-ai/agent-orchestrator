package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/auth"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/postgres"
)

type staticIdentityVerifier struct {
	principal domain.Principal
	err       error
	token     string
}

func (v *staticIdentityVerifier) Verify(_ context.Context, token string) (domain.Principal, error) {
	v.token = token
	return v.principal, v.err
}

type memoryAccountStore struct {
	mu          sync.Mutex
	principal   domain.Principal
	memberships []domain.Membership
	refreshes   map[string]string
	pingErr     error
	upsertErr   error
}

func (s *memoryAccountStore) Ping(context.Context) error {
	return s.pingErr
}

func (s *memoryAccountStore) UpsertGoogleUser(_ context.Context, principal domain.Principal) (domain.Principal, error) {
	if s.upsertErr != nil {
		return domain.Principal{}, s.upsertErr
	}
	principal.UserID = s.principal.UserID
	s.principal = principal
	return principal, nil
}

func (s *memoryAccountStore) PrincipalByID(_ context.Context, userID string) (domain.Principal, error) {
	if userID != s.principal.UserID {
		return domain.Principal{}, postgres.ErrNotFound
	}
	return s.principal, nil
}

func (s *memoryAccountStore) CreateRefreshSession(
	_ context.Context,
	userID string,
	tokenHash []byte,
	_ time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshes[string(tokenHash)] = userID
	return nil
}

func (s *memoryAccountStore) RotateRefreshSession(
	_ context.Context,
	oldHash, newHash []byte,
) (domain.Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	userID, ok := s.refreshes[string(oldHash)]
	if !ok {
		return domain.Principal{}, postgres.ErrNotFound
	}
	delete(s.refreshes, string(oldHash))
	s.refreshes[string(newHash)] = userID
	return s.principal, nil
}

func (s *memoryAccountStore) RevokeRefreshSession(_ context.Context, tokenHash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.refreshes, string(tokenHash))
	return nil
}

func (s *memoryAccountStore) ListMemberships(
	context.Context,
	domain.Principal,
) ([]domain.Membership, error) {
	return s.memberships, nil
}

func TestGoogleExchangeIssuesAOAccessAndRotatingRefreshTokens(t *testing.T) {
	principal := domain.Principal{
		UserID:      "58fc7182-0360-412f-abd9-5057097db664",
		Provider:    "google",
		ExternalID:  "google-subject",
		Email:       "person@example.com",
		DisplayName: "Person Example",
	}
	store := &memoryAccountStore{
		principal: principal,
		memberships: []domain.Membership{{
			OrgID:       "f737107a-d943-4aee-9fa7-46c6f5cafef8",
			OrgSlug:     "personal-58fc71820360412fabd95057097db664",
			DisplayName: "Person Example's organization",
			Role:        "owner",
		}},
		refreshes: make(map[string]string),
	}
	google := &staticIdentityVerifier{principal: principal}
	server := newTestServer(t, store, google)

	exchange := httptest.NewRequest(
		http.MethodPost,
		"/api/cloud/v1/auth/google",
		bytes.NewBufferString(`{"idToken":"google-id-token"}`),
	)
	exchange.Header.Set("Content-Type", "application/json")
	exchangeResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(exchangeResponse, exchange)
	if exchangeResponse.Code != http.StatusOK {
		t.Fatalf("exchange status = %d: %s", exchangeResponse.Code, exchangeResponse.Body.String())
	}
	if exchangeResponse.Header().Get("Cache-Control") != "no-store" || google.token != "google-id-token" {
		t.Fatalf("cache control = %q, verified token = %q", exchangeResponse.Header().Get("Cache-Control"), google.token)
	}
	var issued sessionResponse
	if err := json.Unmarshal(exchangeResponse.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.AccessToken == "" || issued.RefreshToken == "" ||
		issued.User.AuthProvider != "google" || len(issued.Organizations) != 1 {
		t.Fatalf("issued session = %#v", issued)
	}

	me := httptest.NewRequest(http.MethodGet, "/api/cloud/v1/me", nil)
	me.Header.Set("Authorization", "Bearer "+issued.AccessToken)
	meResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(meResponse, me)
	if meResponse.Code != http.StatusOK {
		t.Fatalf("me status = %d: %s", meResponse.Code, meResponse.Body.String())
	}

	body, err := json.Marshal(refreshRequest{RefreshToken: issued.RefreshToken})
	if err != nil {
		t.Fatal(err)
	}
	firstRefresh := httptest.NewRequest(http.MethodPost, "/api/cloud/v1/auth/refresh", bytes.NewReader(body))
	firstRefresh.Header.Set("Content-Type", "application/json")
	firstResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(firstResponse, firstRefresh)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first refresh status = %d: %s", firstResponse.Code, firstResponse.Body.String())
	}
	var rotated sessionResponse
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken == "" || rotated.RefreshToken == issued.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}

	replay := httptest.NewRequest(http.MethodPost, "/api/cloud/v1/auth/refresh", bytes.NewReader(body))
	replay.Header.Set("Content-Type", "application/json")
	replayResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("refresh replay status = %d: %s", replayResponse.Code, replayResponse.Body.String())
	}
}

func TestGoogleExchangeRejectsUnverifiedIdentity(t *testing.T) {
	store := &memoryAccountStore{refreshes: make(map[string]string)}
	server := newTestServer(t, store, &staticIdentityVerifier{err: auth.ErrInvalidToken})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/cloud/v1/auth/google",
		bytes.NewBufferString(`{"idToken":"forged"}`),
	)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

func TestGoogleExchangeReturnsConflictForDuplicateAccountState(t *testing.T) {
	store := &memoryAccountStore{
		refreshes: make(map[string]string),
		upsertErr: postgres.ErrConflict,
	}
	server := newTestServer(t, store, &staticIdentityVerifier{principal: domain.Principal{
		ExternalID: "google-subject",
		Email:      "person@example.com",
	}})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/cloud/v1/auth/google",
		bytes.NewBufferString(`{"idToken":"google-id-token"}`),
	)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

func TestRefreshAndLogoutRejectMalformedRefreshTokenConsistently(t *testing.T) {
	store := &memoryAccountStore{refreshes: make(map[string]string)}
	server := newTestServer(t, store, &staticIdentityVerifier{})
	for _, path := range []string{
		"/api/cloud/v1/auth/refresh",
		"/api/cloud/v1/auth/logout",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				path,
				bytes.NewBufferString(`{"refreshToken":"malformed"}`),
			)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var responseError errorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &responseError); err != nil {
				t.Fatal(err)
			}
			if responseError.Code != "INVALID_REFRESH_TOKEN" {
				t.Fatalf("code = %q, want INVALID_REFRESH_TOKEN", responseError.Code)
			}
		})
	}
}

func TestReadinessFailsClosedWhenPostgresIsUnavailable(t *testing.T) {
	store := &memoryAccountStore{refreshes: make(map[string]string), pingErr: errors.New("unavailable")}
	server := newTestServer(t, store, &staticIdentityVerifier{})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

func newTestServer(t *testing.T, store AccountStore, verifier IdentityVerifier) *Server {
	t.Helper()
	tokens, err := auth.NewAccessTokenManager(
		[]byte("0123456789abcdef0123456789abcdef"),
		"ao-cloud-test",
		"ao-desktop-test",
		15*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Store:           store,
		Google:          verifier,
		AccessTokens:    tokens,
		RefreshTokenTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}
