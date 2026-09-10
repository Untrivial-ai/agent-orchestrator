package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	clouddomain "github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
	cloudevents "github.com/aoagents/agent-orchestrator/backend/internal/cloud/events"
	cloudpostgres "github.com/aoagents/agent-orchestrator/backend/internal/cloud/postgres"
	cloudworker "github.com/aoagents/agent-orchestrator/backend/internal/cloud/worker"
)

func TestProjectShareRoles(t *testing.T) {
	if !validProjectShareRole("viewer") || !validProjectShareRole("editor") {
		t.Fatal("viewer and editor must be valid project share roles")
	}
	for _, role := range []string{"owner", "admin", "member", ""} {
		if validProjectShareRole(role) {
			t.Fatalf("%q must not be a valid project share role", role)
		}
	}
	if !orgRoleAtLeast("editor", "member") {
		t.Fatal("project editors must be allowed to operate project sessions")
	}
	if orgRoleAtLeast("editor", "admin") {
		t.Fatal("project editors must not receive organization admin access")
	}
}

func TestSessionKindsThatCanDeleteSandboxes(t *testing.T) {
	for _, kind := range []string{"worker", "orchestrator"} {
		if !canDeleteSessionSandbox(kind) {
			t.Fatalf("%q sessions must delete their own sandboxes when terminated", kind)
		}
	}
	if canDeleteSessionSandbox("unknown") {
		t.Fatal("unknown session kinds must not delete a sandbox")
	}
}

func TestSharedProjectAccessPreservesSessionScope(t *testing.T) {
	projectID := clouddomain.ProjectID("project-one")
	orgID := clouddomain.OrgID("org-one")
	sessionA := clouddomain.SessionID("session-a")
	sessionB := clouddomain.SessionID("session-b")

	access := newSharedProjectAccess([]cloudpostgres.SharedProjectGrant{
		{
			OrgID:   orgID,
			Role:    "viewer",
			Project: clouddomain.Project{ID: projectID},
		},
		{
			OrgID:   orgID,
			Role:    "editor",
			Project: clouddomain.Project{ID: projectID},
			Session: &clouddomain.Session{ID: sessionA, ProjectID: projectID},
		},
		{
			OrgID:   orgID,
			Role:    "editor",
			Project: clouddomain.Project{ID: projectID},
			Session: &clouddomain.Session{ID: sessionB, ProjectID: projectID},
		},
		{
			OrgID:   "other-org",
			Role:    "editor",
			Project: clouddomain.Project{ID: "other-project"},
		},
	}, orgID)

	if !access.allowsProject(projectID) {
		t.Fatal("session-scoped share should expose its project")
	}
	if role, ok := access.projectRole(projectID); !ok || role != "viewer" {
		t.Fatalf("project role = (%q, %t), want (viewer, true) for the project-wide grant", role, ok)
	}
	if role, ok := access.sessionRole(projectID, sessionA); !ok || role != "editor" {
		t.Fatalf("session A role = (%q, %t), want (editor, true)", role, ok)
	}
	if role, ok := access.sessionRole(projectID, sessionB); !ok || role != "editor" {
		t.Fatalf("session B role = (%q, %t), want (editor, true)", role, ok)
	}
	if !access.canOperateSession(projectID, sessionA) || !access.canOperateSession(projectID, sessionB) {
		t.Fatal("session-specific editors must be able to operate their own sessions")
	}
	if role, ok := access.sessionRole(projectID, "session-c"); !ok || role != "viewer" {
		t.Fatalf("project-wide viewer role = (%q, %t), want (viewer, true)", role, ok)
	}
	if access.canOperateSession(projectID, "session-c") {
		t.Fatal("viewer access must not grant terminal or mutation operations")
	}
	if access.allowsProject("other-project") {
		t.Fatal("share from another organization must not grant project access")
	}

	access = newSharedProjectAccess([]cloudpostgres.SharedProjectGrant{
		{
			OrgID:   orgID,
			Role:    "editor",
			Project: clouddomain.Project{ID: projectID},
			Session: &clouddomain.Session{ID: sessionA, ProjectID: projectID},
		},
		{
			OrgID:   orgID,
			Role:    "viewer",
			Project: clouddomain.Project{ID: projectID},
			Session: &clouddomain.Session{ID: sessionA, ProjectID: projectID},
		},
	}, orgID)
	if _, ok := access.projectRole(projectID); ok {
		t.Fatal("session-only grants must not create project-wide editor access")
	}
	if role, ok := access.sessionRole(projectID, sessionA); !ok || role != "viewer" {
		t.Fatalf("overlapping session grants = (%q, %t), want least-privileged (viewer, true)", role, ok)
	}
	if _, ok := access.sessionRole(projectID, sessionB); ok {
		t.Fatal("session-scoped grants must not expose another session")
	}
	if access.canOperateSession(projectID, sessionB) {
		t.Fatal("session A editor must not operate session B")
	}
}

type terminalTicketTestStore struct {
	store

	mu       sync.Mutex
	consumed bool
	attempts int
}

func (s *terminalTicketTestStore) ConsumeAccessTicket(
	_ context.Context,
	token, purpose string,
) (cloudpostgres.ConsumedTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if token != "valid-ticket" || purpose != "terminal" || s.consumed {
		return cloudpostgres.ConsumedTicket{}, cloudpostgres.ErrInvalidTicket
	}
	s.consumed = true
	return cloudpostgres.ConsumedTicket{
		AccountID: "account-one",
		SessionID: "session-one",
		Purpose:   purpose,
		Scopes:    []string{"terminal:read"},
	}, nil
}

func (s *terminalTicketTestStore) LatestEventSequenceByType(
	context.Context,
	clouddomain.AccountID,
	clouddomain.SessionID,
	string,
) (int64, error) {
	return 0, errors.New("stop terminal after successful ticket validation")
}

func (s *terminalTicketTestStore) consumeAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

func terminalSocketTestServer(store *terminalTicketTestStore) *Server {
	return &Server{
		store:         store,
		events:        cloudevents.New(nil),
		webOriginHost: "app.example",
	}
}

func websocketUpgradeRequest(path string, origin string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	return request
}

func TestTerminalSocketRejectsInvalidRequestsWithoutConsumingTicket(t *testing.T) {
	for _, test := range []struct {
		name    string
		request func() *http.Request
		status  int
	}{
		{
			name: "invalid after",
			request: func() *http.Request {
				return websocketUpgradeRequest("/terminal?ticket=valid-ticket&after=-1", "https://app.example")
			},
			status: http.StatusBadRequest,
		},
		{
			name: "rejected origin",
			request: func() *http.Request {
				return websocketUpgradeRequest("/terminal?ticket=valid-ticket", "https://attacker.example")
			},
			status: http.StatusForbidden,
		},
		{
			name: "invalid upgrade",
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/terminal?ticket=valid-ticket", nil)
			},
			status: http.StatusUpgradeRequired,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &terminalTicketTestStore{}
			response := httptest.NewRecorder()
			terminalSocketTestServer(store).terminalSocket(response, test.request())
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if attempts := store.consumeAttempts(); attempts != 0 {
				t.Fatalf("ticket consume attempts = %d, want 0", attempts)
			}
		})
	}
}

func TestTerminalSocketConsumesTicketOnlyAfterSuccessfulUpgrade(t *testing.T) {
	store := &terminalTicketTestStore{}
	server := httptest.NewServer(http.HandlerFunc(terminalSocketTestServer(store).terminalSocket))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	terminalURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/terminal?ticket=valid-ticket"
	headers := http.Header{"Origin": []string{"https://app.example"}}
	socket, _, err := websocket.Dial(ctx, terminalURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatalf("dial valid terminal socket: %v", err)
	}
	defer socket.Close(websocket.StatusNormalClosure, "test complete")
	if attempts := store.consumeAttempts(); attempts != 1 {
		t.Fatalf("ticket consume attempts = %d, want 1 after a successful upgrade", attempts)
	}

	replay, _, err := websocket.Dial(ctx, terminalURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatalf("dial replay terminal socket: %v", err)
	}
	defer replay.Close(websocket.StatusNormalClosure, "test complete")
	_, _, err = replay.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("replayed ticket close = %v, want policy violation", err)
	}
	if attempts := store.consumeAttempts(); attempts != 2 {
		t.Fatalf("ticket consume attempts = %d, want 2 including rejected replay", attempts)
	}
}

func TestSharedProjectRequestScope(t *testing.T) {
	orgID := clouddomain.OrgID("org-one")
	for _, test := range []struct {
		method  string
		path    string
		allowed bool
	}{
		{method: http.MethodGet, path: "/api/cloud/v1/orgs/org-one/projects", allowed: true},
		{method: http.MethodPost, path: "/api/cloud/v1/orgs/org-one/projects", allowed: false},
		{method: http.MethodGet, path: "/api/cloud/v1/orgs/org-one/sessions", allowed: true},
		{method: http.MethodPost, path: "/api/cloud/v1/orgs/org-one/sessions", allowed: true},
		{method: http.MethodGet, path: "/api/cloud/v1/orgs/org-one/sessions/session-one", allowed: true},
		{method: http.MethodGet, path: "/api/cloud/v1/orgs/org-one/members", allowed: false},
		{method: http.MethodGet, path: "/api/cloud/v1/orgs/org-one/provider-connections", allowed: false},
		{method: http.MethodGet, path: "/api/cloud/v1/orgs/other/sessions", allowed: false},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		if got := sharedProjectRequestAllowed(request, orgID); got != test.allowed {
			t.Fatalf("%s %s allowed = %t, want %t", test.method, test.path, got, test.allowed)
		}
	}
}

func TestRefreshClaimedPullRequestRefreshesGitHubAppProject(t *testing.T) {
	var gotOrg clouddomain.OrgID
	var gotRepositoryID int64
	server := &Server{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		githubApp: &githubAppRuntime{
			repositoryRefresh: func(_ context.Context, orgID clouddomain.OrgID, repositoryID int64) error {
				gotOrg = orgID
				gotRepositoryID = repositoryID
				return nil
			},
		},
	}
	repositoryID := int64(991)

	server.refreshClaimedPullRequest(context.Background(), clouddomain.Project{
		ID:                 "project-one",
		OrgID:              "org-one",
		GitHubRepositoryID: &repositoryID,
	})

	if gotOrg != "org-one" || gotRepositoryID != repositoryID {
		t.Fatalf("refresh = (%q, %d), want (org-one, %d)", gotOrg, gotRepositoryID, repositoryID)
	}
}

func TestValidateOrgMemberRoleUpdateProtectsOwnerInvariants(t *testing.T) {
	target := clouddomain.OrgMember{
		User:       clouddomain.User{ID: "target-user"},
		Membership: clouddomain.OrgMembership{Role: "owner"},
	}
	for _, test := range []struct {
		name       string
		principal  string
		actorRole  string
		target     clouddomain.OrgMember
		ownerCount int
		nextRole   string
		wantStatus int
		wantOK     bool
	}{
		{
			name:       "admin cannot change owner",
			principal:  "admin-user",
			actorRole:  "admin",
			target:     target,
			ownerCount: 2,
			nextRole:   "member",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "last owner cannot be demoted",
			principal:  "owner-two",
			actorRole:  "owner",
			target:     target,
			ownerCount: 1,
			nextRole:   "admin",
			wantStatus: http.StatusConflict,
		},
		{
			name:       "member cannot change own role",
			principal:  "target-user",
			actorRole:  "admin",
			target:     clouddomain.OrgMember{User: clouddomain.User{ID: "target-user"}, Membership: clouddomain.OrgMembership{Role: "member"}},
			ownerCount: 1,
			nextRole:   "viewer",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "owner can change another owner when another remains",
			principal:  "owner-two",
			actorRole:  "owner",
			target:     target,
			ownerCount: 2,
			nextRole:   "admin",
			wantOK:     true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, _, _, ok := validateOrgMemberRoleUpdate(
				test.principal,
				test.actorRole,
				test.target,
				test.ownerCount,
				test.nextRole,
			)
			if ok != test.wantOK || status != test.wantStatus {
				t.Fatalf("validateOrgMemberRoleUpdate() = (%d, %t), want (%d, %t)", status, ok, test.wantStatus, test.wantOK)
			}
		})
	}
}

func TestActivityTurnTransitions(t *testing.T) {
	for _, test := range []struct {
		event         string
		state         string
		wantStarts    bool
		wantCompletes bool
	}{
		{event: "user-prompt-submit", state: "active", wantStarts: true},
		{event: "pre-tool-use", state: "active"},
		{event: "stop", state: "idle", wantCompletes: true},
		{event: "after-agent", state: "idle", wantCompletes: true},
		{event: "notification", state: "idle"},
		{event: "permission-request", state: "blocked"},
		{event: "session-end", state: "exited"},
	} {
		t.Run(test.event+"/"+test.state, func(t *testing.T) {
			if got := activityStartsTurn(test.event, test.state); got != test.wantStarts {
				t.Fatalf(
					"activityStartsTurn(%q, %q) = %t, want %t",
					test.event,
					test.state,
					got,
					test.wantStarts,
				)
			}
			if got := activityCompletesTurn(test.event, test.state); got != test.wantCompletes {
				t.Fatalf(
					"activityCompletesTurn(%q, %q) = %t, want %t",
					test.event,
					test.state,
					got,
					test.wantCompletes,
				)
			}
		})
	}
}

func TestActivityNativeSessionID(t *testing.T) {
	if got := activityNativeSessionID(json.RawMessage(`{"session_id":"native-session"}`)); got != "native-session" {
		t.Fatalf("activityNativeSessionID() = %q", got)
	}
	if got := activityNativeSessionID(json.RawMessage(`{"session_id":""}`)); got != "" {
		t.Fatalf("activityNativeSessionID(blank) = %q", got)
	}
}

func TestRedactWorkerEventPayload(t *testing.T) {
	payload := json.RawMessage(`{"token":"secret","nested":{"api_key":"key"},"message":"AO_WORKER_TOKEN=abc123"}`)
	redacted := redactWorkerEventPayload("custom", payload)
	if string(redacted) != `{"message":"AO_WORKER_TOKEN=[redacted]","nested":{"api_key":"[redacted]"},"token":"[redacted]"}` {
		t.Fatalf("redacted payload = %s", redacted)
	}
}

func TestRedactWorkerTerminalPayload(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello\nAO_WORKER_BOOTSTRAP_TOKEN=secret\n"))
	payload, _ := json.Marshal(map[string]string{
		"encoding": "base64",
		"data":     encoded,
	})
	redacted := redactWorkerEventPayload("terminal.output", payload)
	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(redacted, &result); err != nil {
		t.Fatalf("decode redacted payload: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if got := string(decoded); got != "hello\nAO_WORKER_BOOTSTRAP_TOKEN=[redacted]\n" {
		t.Fatalf("decoded payload = %q", got)
	}
}

func TestWorkerBootstrapNeverIncludesGitHubAppToken(t *testing.T) {
	if includeLocalGitHubToken("docker", "github-app", true) {
		t.Fatal("github-app mode allowed an installation token in worker bootstrap")
	}
	if !includeLocalGitHubToken("docker", "local-gh", true) {
		t.Fatal("local-gh Docker mode did not preserve its explicit development token")
	}
}

func TestWorkerAuthAcceptsBasicOnlyForExactGitProxyRoute(t *testing.T) {
	manager := cloudworker.NewTokenManager([]byte("01234567890123456789012345678901"))
	claims := cloudworker.Claims{
		AccountID: "account-one",
		SessionID: "session-one",
		WorkerID:  "worker-one",
		Epoch:     7,
		Scopes:    []string{"worker:git"},
	}
	token, err := manager.Issue(claims, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		method        string
		target        string
		scheme        string
		username      string
		current       bool
		wantStatus    int
		wantStoreCall bool
	}{
		{
			name:          "Basic Git discovery",
			method:        http.MethodGet,
			target:        "/api/cloud/v1/git/acme/repository.git/info/refs?service=git-upload-pack",
			scheme:        "Basic",
			username:      cloudworker.GitProxyUsername,
			current:       true,
			wantStatus:    http.StatusNoContent,
			wantStoreCall: true,
		},
		{
			name:       "Basic worker API",
			method:     http.MethodPost,
			target:     "/api/cloud/v1/worker/heartbeat",
			scheme:     "Basic",
			username:   cloudworker.GitProxyUsername,
			current:    true,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "Basic near Git route",
			method:     http.MethodGet,
			target:     "/api/cloud/v1/git/acme/repository.gitx/info/refs?service=git-upload-pack",
			scheme:     "Basic",
			username:   cloudworker.GitProxyUsername,
			current:    true,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid Basic username",
			method:     http.MethodGet,
			target:     "/api/cloud/v1/git/acme/repository.git/info/refs?service=git-upload-pack",
			scheme:     "Basic",
			username:   "x-access-token",
			current:    true,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:          "revoked worker epoch",
			method:        http.MethodPost,
			target:        "/api/cloud/v1/git/acme/repository.git/git-receive-pack",
			scheme:        "Basic",
			username:      cloudworker.GitProxyUsername,
			current:       false,
			wantStatus:    http.StatusUnauthorized,
			wantStoreCall: true,
		},
		{
			name:          "Worker scheme remains valid for worker API",
			method:        http.MethodPost,
			target:        "/api/cloud/v1/worker/heartbeat",
			scheme:        "Worker",
			current:       true,
			wantStatus:    http.StatusNoContent,
			wantStoreCall: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authStore := &workerAuthStore{current: test.current}
			server := &Server{store: authStore, workerTokens: manager}
			handler := server.workerAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got := workerFromContext(r.Context())
				if got.WorkerID != claims.WorkerID || got.Epoch != claims.Epoch {
					t.Fatalf("worker claims = %#v", got)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(test.method, test.target, nil)
			if test.scheme == "Basic" {
				request.SetBasicAuth(test.username, token)
			} else {
				request.Header.Set("Authorization", "Worker "+token)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if got := authStore.calls > 0; got != test.wantStoreCall {
				t.Fatalf("WorkerConnectionCurrent called = %t, want %t", got, test.wantStoreCall)
			}
		})
	}
}

func TestWorkerGitAuthChallengesThenAcceptsCurrentHelperCredential(t *testing.T) {
	manager := cloudworker.NewTokenManager([]byte("01234567890123456789012345678901"))
	claims := cloudworker.Claims{
		AccountID: "account-one",
		SessionID: "session-one",
		WorkerID:  "worker-one",
		Epoch:     7,
		Scopes:    []string{"worker:git"},
	}
	authStore := &workerAuthStore{current: true}
	server := &Server{store: authStore, workerTokens: manager}
	httpServer := httptest.NewServer(server.workerAuth(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			got := workerFromContext(r.Context())
			if got.WorkerID != claims.WorkerID || got.Epoch != claims.Epoch {
				t.Fatalf("worker claims = %#v", got)
			}
			w.WriteHeader(http.StatusNoContent)
		},
	)))
	defer httpServer.Close()

	gitPath := "/api/cloud/v1/git/acme/repository.git/info/refs?service=git-upload-pack"
	response, err := httpServer.Client().Get(httpServer.URL + gitPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", response.StatusCode)
	}
	if got := response.Header.Get("WWW-Authenticate"); got != `Basic realm="ao-worker-git"` {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
	if authStore.calls != 0 {
		t.Fatal("unauthenticated challenge attempted worker epoch validation")
	}

	currentToken, err := manager.Issue(claims, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := http.NewRequest(http.MethodGet, httpServer.URL+gitPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	retry.SetBasicAuth(cloudworker.GitProxyUsername, currentToken)
	response, err = httpServer.Client().Do(retry)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("authenticated retry status = %d, want 204", response.StatusCode)
	}
	if authStore.calls != 1 {
		t.Fatalf("worker epoch validation calls = %d, want 1", authStore.calls)
	}

	for _, request := range []struct {
		name   string
		method string
		path   string
	}{
		{
			name:   "worker endpoint",
			method: http.MethodPost,
			path:   "/api/cloud/v1/worker/heartbeat",
		},
		{
			name:   "malformed Git operation",
			method: http.MethodGet,
			path:   "/api/cloud/v1/git/acme/repository.git/objects/info/packs",
		},
	} {
		t.Run(request.name, func(t *testing.T) {
			unauthenticated, err := http.NewRequest(
				request.method,
				httpServer.URL+request.path,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			got, err := httpServer.Client().Do(unauthenticated)
			if err != nil {
				t.Fatal(err)
			}
			_ = got.Body.Close()
			if got.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", got.StatusCode)
			}
			if challenge := got.Header.Get("WWW-Authenticate"); challenge != "" {
				t.Fatalf("unexpected WWW-Authenticate = %q", challenge)
			}
		})
	}
}

type workerAuthStore struct {
	store
	current bool
	calls   int
}

func (s *workerAuthStore) WorkerConnectionCurrent(
	context.Context,
	clouddomain.AccountID,
	clouddomain.SessionID,
	string,
	int64,
) (bool, error) {
	s.calls++
	return s.current, nil
}

func TestValidDaytonaAPIURL(t *testing.T) {
	configured := "https://app.daytona.io/api"
	for _, value := range []string{
		"https://app.daytona.io/api",
		"https://api.daytona.io",
		"https://tenant.daytona.io/custom",
	} {
		if !validDaytonaAPIURL(value, configured) {
			t.Fatalf("validDaytonaAPIURL(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"http://app.daytona.io/api",
		"https://evil.example/api",
		"https://app.daytona.io.evil.example/api",
		"https://app.daytona.io:8443/api",
		"https://user:pass@app.daytona.io/api",
	} {
		if validDaytonaAPIURL(value, configured) {
			t.Fatalf("validDaytonaAPIURL(%q) = true, want false", value)
		}
	}
	if !validDaytonaAPIURL("https://daytona.internal:8443/api", "https://daytona.internal:8443/api") {
		t.Fatal("configured Daytona URL with explicit port was rejected")
	}
}
