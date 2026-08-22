package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/auth"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
)

type memoryRuntimeStore struct {
	mu        sync.Mutex
	workspace domain.Workspace
	runtime   domain.SessionRuntime
	running   chan struct{}
}

func (s *memoryRuntimeStore) RuntimeWorkspace(context.Context, domain.Principal, string, string) (domain.Workspace, error) {
	return s.workspace, nil
}
func (s *memoryRuntimeStore) CreateSessionRuntime(_ context.Context, _ domain.Principal, workspace domain.Workspace, sessionID string) (domain.SessionRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime = domain.SessionRuntime{ID: "runtime-1", WorkspaceID: workspace.ID, OrgID: workspace.OrgID, SessionID: sessionID, State: domain.SessionRuntimeProvisioning}
	return s.runtime, nil
}
func (s *memoryRuntimeStore) SessionRuntime(context.Context, domain.Principal, string, string, string) (domain.SessionRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtime, nil
}
func (s *memoryRuntimeStore) UpdateSessionRuntime(_ context.Context, _ domain.Principal, runtime domain.SessionRuntime, state, sandboxID, failure string) error {
	s.mu.Lock()
	runtime.State = state
	runtime.SandboxID = sandboxID
	runtime.Error = failure
	s.runtime = runtime
	s.mu.Unlock()
	if state == domain.SessionRuntimeRunning {
		close(s.running)
	}
	return nil
}

type fakeSessionProvisioner struct{ launch chan domain.RuntimeLaunch }

func (p *fakeSessionProvisioner) ProvisionSessionRuntime(_ context.Context, _ domain.Workspace, launch domain.RuntimeLaunch) (string, error) {
	p.launch <- launch
	return "sandbox-session-1", nil
}
func (*fakeSessionProvisioner) DeleteSessionRuntime(context.Context, string) error { return nil }
func (*fakeSessionProvisioner) SessionRuntimeAlive(context.Context, string) (bool, error) {
	return true, nil
}
func (*fakeSessionProvisioner) SessionRuntimeOutput(context.Context, string, int) (string, error) {
	return "ready", nil
}
func (*fakeSessionProvisioner) SessionRuntimeInput(context.Context, string, string, bool) error {
	return nil
}
func (*fakeSessionProvisioner) SessionRuntimeInterrupt(context.Context, string) error { return nil }

func TestWorkspaceCapabilityCreatesOneSessionRuntime(t *testing.T) {
	principal := domain.Principal{UserID: "user-1"}
	accounts := &memoryAccountStore{principal: principal, refreshes: map[string]string{}}
	tokens, err := auth.NewAccessTokenManager([]byte("0123456789abcdef0123456789abcdef"), "issuer", "audience", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	workspace := domain.Workspace{ID: "workspace-1", OrgID: "org-1", OwnerUserID: principal.UserID, RepositoryURL: "https://github.com/org/repo"}
	store := &memoryRuntimeStore{workspace: workspace, running: make(chan struct{})}
	provider := &fakeSessionProvisioner{launch: make(chan domain.RuntimeLaunch, 1)}
	server, err := New(Options{Store: accounts, Google: &staticIdentityVerifier{}, AllowedEmails: []string{"person@example.com"}, AccessTokens: tokens, RefreshTokenTTL: time.Hour, SessionStore: store, SessionRuntimes: provider, PublicURL: "https://cloud.example"})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := tokens.IssueWorkspace(principal.UserID, workspace.OrgID, workspace.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	credential := base64.StdEncoding.EncodeToString([]byte(`{"claudeAiOauth":{"accessToken":"secret"}}`))
	body := `{"sessionId":"orchestrator-1","sourceWorkspace":"/workspace","argv":["claude"],"claudeCredentialsBase64":"` + credential + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/cloud/internal/v1/workspaces/workspace-1/runtimes/", bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+capability)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case launch := <-provider.launch:
		if launch.SessionID != "orchestrator-1" {
			t.Fatalf("launch=%#v", launch)
		}
	case <-time.After(time.Second):
		t.Fatal("provisioner not called")
	}
	select {
	case <-store.running:
	case <-time.After(time.Second):
		t.Fatal("runtime not marked running")
	}

	access, _, err := tokens.Issue(principal.UserID)
	if err != nil {
		t.Fatal(err)
	}
	wrong := httptest.NewRequest(http.MethodGet, "/api/cloud/internal/v1/workspaces/workspace-1/runtimes/orchestrator-1", nil)
	wrong.Header.Set("Authorization", "Bearer "+access)
	wrongResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(wrongResponse, wrong)
	if wrongResponse.Code != http.StatusUnauthorized {
		t.Fatalf("desktop token status=%d", wrongResponse.Code)
	}
}
