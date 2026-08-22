package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/auth"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
)

type memoryWorkspaceStore struct {
	mu        sync.Mutex
	workspace domain.Workspace
	updated   chan struct{}
}

func (s *memoryWorkspaceStore) CreateWorkspace(
	_ context.Context, principal domain.Principal, orgID, repositoryURL, repositoryRef string,
) (domain.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspace = domain.Workspace{
		ID: "workspace-1", OrgID: orgID, OwnerUserID: principal.UserID,
		RepositoryURL: repositoryURL, RepositoryRef: repositoryRef,
		State: domain.WorkspacePending, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	return s.workspace, nil
}

func (s *memoryWorkspaceStore) Workspace(
	context.Context, domain.Principal, string, string,
) (domain.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspace, nil
}

func (s *memoryWorkspaceStore) UpdateWorkspaceProvisioning(
	_ context.Context, workspace domain.Workspace, state, sandboxID, failure string,
) error {
	s.mu.Lock()
	s.workspace = workspace
	s.workspace.State = state
	s.workspace.SandboxID = sandboxID
	s.workspace.Error = failure
	s.mu.Unlock()
	if state == domain.WorkspaceReady {
		select {
		case s.updated <- struct{}{}:
		default:
		}
	}
	return nil
}

type fakeWorkspaceProvisioner struct{}

func (fakeWorkspaceProvisioner) Provision(_ context.Context, _ domain.Workspace, bootstrap domain.WorkspaceBootstrap) (string, error) {
	if string(bootstrap.ClaudeCredentials) != `{"claudeAiOauth":{"accessToken":"secret"}}` {
		return "", errors.New("unexpected Claude credentials")
	}
	if bootstrap.RuntimeToken == "" {
		return "", errors.New("missing workspace runtime token")
	}
	return "sandbox-1", nil
}

func (fakeWorkspaceProvisioner) PreviewURL(context.Context, string) (string, error) {
	return "https://3001-signed.proxy.daytona.work", nil
}

func TestWorkspaceProvisioningRequiresAuthAndReturnsSignedAOConnection(t *testing.T) {
	principal := domain.Principal{UserID: "58fc7182-0360-412f-abd9-5057097db664", Provider: "google"}
	accountStore := &memoryAccountStore{principal: principal, refreshes: make(map[string]string)}
	workspaceStore := &memoryWorkspaceStore{updated: make(chan struct{}, 1)}
	tokens, err := auth.NewAccessTokenManager(
		[]byte("0123456789abcdef0123456789abcdef"), "issuer", "audience", 15*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Store: accountStore, Google: &staticIdentityVerifier{}, AccessTokens: tokens,
		AllowedEmails:   []string{"person@example.com"},
		RefreshTokenTTL: time.Hour, WorkspaceStore: workspaceStore,
		Workspaces: fakeWorkspaceProvisioner{},
	})
	if err != nil {
		t.Fatal(err)
	}

	unauthenticated := httptest.NewRequest(http.MethodPost, "/api/cloud/v1/orgs/org-1/workspaces", bytes.NewBufferString(`{"repositoryUrl":"https://github.com/Untrivial-ai/agent-orchestrator.git"}`))
	unauthenticatedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthenticatedResponse.Code)
	}

	accessToken, _, err := tokens.Issue(principal.UserID)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/cloud/v1/orgs/org-1/workspaces", bytes.NewBufferString(`{"repositoryUrl":"https://github.com/Untrivial-ai/agent-orchestrator.git","repositoryRef":"main","claudeCredentialsBase64":"eyJjbGF1ZGVBaU9hdXRoIjp7ImFjY2Vzc1Rva2VuIjoic2VjcmV0In19"}`))
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("create status = %d: %s", response.Code, response.Body.String())
	}
	select {
	case <-workspaceStore.updated:
	case <-time.After(time.Second):
		t.Fatal("workspace provisioning did not complete")
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/cloud/v1/orgs/org-1/workspaces/workspace-1", nil)
	getRequest.Header.Set("Authorization", "Bearer "+accessToken)
	getResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", getResponse.Code, getResponse.Body.String())
	}
	var result workspaceResponse
	if err := json.Unmarshal(getResponse.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Workspace.State != domain.WorkspaceReady || result.Workspace.SandboxID != "sandbox-1" || result.PreviewURL == "" {
		t.Fatalf("workspace response = %#v", result)
	}
}

func TestValidGitHubRepository(t *testing.T) {
	for _, raw := range []string{
		"http://github.com/org/repo", "https://example.com/org/repo", "https://github.com/org", "https://user@github.com/org/repo",
	} {
		if validGitHubRepository(raw) {
			t.Fatalf("validGitHubRepository(%q) = true", raw)
		}
	}
	if !validGitHubRepository("https://github.com/org/repo.git") {
		t.Fatal("valid GitHub repository was rejected")
	}
}

func TestWorkspaceProvisioningRejectsMissingClaudeCredentials(t *testing.T) {
	principal := domain.Principal{UserID: "58fc7182-0360-412f-abd9-5057097db664", Provider: "google"}
	accountStore := &memoryAccountStore{principal: principal, refreshes: make(map[string]string)}
	tokens, err := auth.NewAccessTokenManager(
		[]byte("0123456789abcdef0123456789abcdef"), "issuer", "audience", 15*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Store: accountStore, Google: &staticIdentityVerifier{}, AccessTokens: tokens,
		AllowedEmails: []string{"person@example.com"}, RefreshTokenTTL: time.Hour,
		WorkspaceStore: &memoryWorkspaceStore{updated: make(chan struct{}, 1)}, Workspaces: fakeWorkspaceProvisioner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	accessToken, _, err := tokens.Issue(principal.UserID)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/cloud/v1/orgs/org-1/workspaces", bytes.NewBufferString(`{"repositoryUrl":"https://github.com/org/repo"}`))
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_CLAUDE_CREDENTIALS") {
		t.Fatalf("response = %d: %s", response.Code, response.Body.String())
	}
}
