package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	clouddomain "github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
	cloudpostgres "github.com/aoagents/agent-orchestrator/backend/internal/cloud/postgres"
	cloudworker "github.com/aoagents/agent-orchestrator/backend/internal/cloud/worker"
	cloudworkerhub "github.com/aoagents/agent-orchestrator/backend/internal/cloud/workerhub"
)

func integrationAPI(t *testing.T) (*httptest.Server, *cloudpostgres.Store) {
	t.Helper()
	server, store, _ := integrationAPIWithServer(t)
	return server, store
}

func integrationAPIWithServer(
	t *testing.T,
) (*httptest.Server, *cloudpostgres.Store, *Server) {
	t.Helper()
	t.Skip("cloud Postgres integration tests are disabled until hosted DB test infrastructure is restored")
	return nil, nil, nil
}

func localAuthIntegrationAPI(t *testing.T) *httptest.Server {
	t.Helper()
	t.Skip("cloud Postgres integration tests are disabled until hosted DB test infrastructure is restored")
	return nil
}

func tokenID(token string) string {
	if token == "user-one" {
		return "11111111-1111-4111-8111-111111111111"
	}
	return "22222222-2222-4222-8222-222222222222"
}

func requestJSON(
	t *testing.T,
	server *httptest.Server,
	method, path, token string,
	body any,
	headers map[string]string,
) *http.Response {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
	}
	request, err := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	return response
}

func TestLocalAuthRoutesUsePostgresStore(t *testing.T) {
	server := localAuthIntegrationAPI(t)
	email := "local-" + uuid.NewString() + "@example.com"
	signup := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/auth/signup",
		"",
		map[string]string{
			"email":       email,
			"password":    "correct-horse",
			"displayName": "Local User",
		},
		nil,
	)
	defer signup.Body.Close()
	if signup.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(signup.Body)
		t.Fatalf("signup status = %d: %s", signup.StatusCode, payload)
	}
	var signupBody struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.NewDecoder(signup.Body).Decode(&signupBody); err != nil {
		t.Fatal(err)
	}
	logout := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/auth/logout",
		signupBody.AccessToken,
		nil,
		nil,
	)
	defer logout.Body.Close()
	if logout.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d", logout.StatusCode)
	}
	login := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/auth/login",
		"",
		map[string]string{
			"email":    email,
			"password": "correct-horse",
		},
		nil,
	)
	defer login.Body.Close()
	if login.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(login.Body)
		t.Fatalf("login status = %d: %s", login.StatusCode, payload)
	}
	var loginBody struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.NewDecoder(login.Body).Decode(&loginBody); err != nil {
		t.Fatal(err)
	}
	me := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/me",
		loginBody.AccessToken,
		nil,
		nil,
	)
	defer me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("PostgreSQL-backed session status = %d", me.StatusCode)
	}
	revoke := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/auth/logout",
		loginBody.AccessToken,
		nil,
		nil,
	)
	defer revoke.Body.Close()
	if revoke.StatusCode != http.StatusNoContent {
		t.Fatalf("second logout status = %d", revoke.StatusCode)
	}
	revoked := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/me",
		loginBody.AccessToken,
		nil,
		nil,
	)
	defer revoked.Body.Close()
	if revoked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked PostgreSQL session status = %d, want 401", revoked.StatusCode)
	}
}

func TestAuthenticatedProjectAndIdempotentSessionFlow(t *testing.T) {
	server, store := integrationAPI(t)
	ctx := context.Background()
	account, err := store.EnsureAccount(ctx, tokenID("user-one"), "User One")
	if err != nil {
		t.Fatalf("ensure user-one account: %v", err)
	}
	connection := requestJSON(
		t,
		server,
		http.MethodPut,
		"/api/cloud/v1/provider-connections/agents/cursor",
		"user-one",
		map[string]string{
			"credentialType": "api_key",
			"secret":         "cursor-secret",
		},
		nil,
	)
	defer connection.Body.Close()
	if connection.StatusCode != http.StatusOK {
		t.Fatalf("agent connection status = %d", connection.StatusCode)
	}
	repositoryURL := "https://github.com/example/" + uuid.NewString()
	projectResponse := requestJSON(t, server, http.MethodPost, "/api/cloud/v1/projects", "user-one", map[string]any{
		"displayName":   "AO Cloud",
		"repositoryUrl": repositoryURL,
		"defaultBranch": "main",
	}, nil)
	defer projectResponse.Body.Close()
	if projectResponse.StatusCode != http.StatusCreated {
		t.Fatalf("project status = %d", projectResponse.StatusCode)
	}
	var projectBody struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.NewDecoder(projectResponse.Body).Decode(&projectBody); err != nil {
		t.Fatalf("decode project response: %v", err)
	}
	duplicateProject := requestJSON(t, server, http.MethodPost, "/api/cloud/v1/projects", "user-one", map[string]any{
		"displayName":   "AO Cloud duplicate",
		"repositoryUrl": repositoryURL,
		"defaultBranch": "main",
	}, nil)
	defer duplicateProject.Body.Close()
	if duplicateProject.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate project status = %d, want 409", duplicateProject.StatusCode)
	}

	idempotencyKey := uuid.NewString()
	sessionInput := map[string]any{
		"projectId":   projectBody.Project.ID,
		"kind":        "worker",
		"harness":     "fake",
		"displayName": "verify-cloud",
		"prompt":      "Verify the cloud flow",
	}
	undersizedInput := maps.Clone(sessionInput)
	undersizedInput["resource"] = map[string]int{
		"cpu":    2,
		"memory": 4,
		"disk":   5,
	}
	undersized := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/sessions",
		"user-one",
		undersizedInput,
		map[string]string{"Idempotency-Key": uuid.NewString()},
	)
	defer undersized.Body.Close()
	if undersized.StatusCode != http.StatusBadRequest {
		t.Fatalf("undersized resource status = %d, want 400", undersized.StatusCode)
	}
	first := requestJSON(t, server, http.MethodPost, "/api/cloud/v1/sessions", "user-one", sessionInput, map[string]string{
		"Idempotency-Key": idempotencyKey,
	})
	defer first.Body.Close()
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first session status = %d", first.StatusCode)
	}
	var firstBody struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstBody); err != nil {
		t.Fatalf("decode first session: %v", err)
	}

	second := requestJSON(t, server, http.MethodPost, "/api/cloud/v1/sessions", "user-one", sessionInput, map[string]string{
		"Idempotency-Key": idempotencyKey,
	})
	defer second.Body.Close()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second session status = %d", second.StatusCode)
	}
	var secondBody struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.NewDecoder(second.Body).Decode(&secondBody); err != nil {
		t.Fatalf("decode second session: %v", err)
	}
	if secondBody.Session.ID != firstBody.Session.ID {
		t.Fatalf("session IDs = %q and %q", firstBody.Session.ID, secondBody.Session.ID)
	}
	conflictingInput := maps.Clone(sessionInput)
	conflictingInput["prompt"] = "A different cloud flow"
	conflicting := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/sessions",
		"user-one",
		conflictingInput,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	defer conflicting.Body.Close()
	if conflicting.StatusCode != http.StatusConflict {
		t.Fatalf(
			"conflicting session receipt status = %d, want 409",
			conflicting.StatusCode,
		)
	}
	orchestratorInput := map[string]any{
		"projectId":   projectBody.Project.ID,
		"kind":        "orchestrator",
		"harness":     "fake",
		"displayName": "Orchestrator",
		"prompt":      "",
	}
	orchestrator := requestJSON(t, server, http.MethodPost, "/api/cloud/v1/sessions", "user-one", orchestratorInput, map[string]string{
		"Idempotency-Key": uuid.NewString(),
	})
	defer orchestrator.Body.Close()
	if orchestrator.StatusCode != http.StatusCreated {
		t.Fatalf("orchestrator status = %d", orchestrator.StatusCode)
	}
	var orchestratorBody struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.NewDecoder(orchestrator.Body).Decode(&orchestratorBody); err != nil {
		t.Fatalf("decode orchestrator response: %v", err)
	}
	duplicateOrchestrator := requestJSON(t, server, http.MethodPost, "/api/cloud/v1/sessions", "user-one", orchestratorInput, map[string]string{
		"Idempotency-Key": uuid.NewString(),
	})
	defer duplicateOrchestrator.Body.Close()
	if duplicateOrchestrator.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate orchestrator status = %d, want 409", duplicateOrchestrator.StatusCode)
	}
	deleteOrchestrator := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/sessions/"+orchestratorBody.Session.ID+"/desired-state",
		"user-one",
		map[string]string{"state": "deleted"},
		nil,
	)
	defer deleteOrchestrator.Body.Close()
	if deleteOrchestrator.StatusCode != http.StatusAccepted {
		t.Fatalf("delete orchestrator status = %d, want 202", deleteOrchestrator.StatusCode)
	}
	orchestratorSandbox, err := store.GetSandbox(ctx, account.ID, clouddomain.SessionID(orchestratorBody.Session.ID))
	if err != nil {
		t.Fatalf("get deleted orchestrator sandbox: %v", err)
	}
	if orchestratorSandbox.DesiredState != "deleted" {
		t.Fatalf("orchestrator sandbox desired state = %q, want deleted", orchestratorSandbox.DesiredState)
	}
	deleteWorker := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/sessions/"+firstBody.Session.ID+"/desired-state",
		"user-one",
		map[string]string{"state": "deleted"},
		nil,
	)
	defer deleteWorker.Body.Close()
	if deleteWorker.StatusCode != http.StatusAccepted {
		t.Fatalf("delete worker status = %d, want 202", deleteWorker.StatusCode)
	}
	deletedWorkerResponse := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/sessions/"+firstBody.Session.ID,
		"user-one",
		nil,
		nil,
	)
	defer deletedWorkerResponse.Body.Close()
	if deletedWorkerResponse.StatusCode != http.StatusOK {
		t.Fatalf("deleted worker status = %d, want 200", deletedWorkerResponse.StatusCode)
	}
	var deletedWorkerBody struct {
		Session clouddomain.Session `json:"session"`
	}
	if err := json.NewDecoder(deletedWorkerResponse.Body).Decode(&deletedWorkerBody); err != nil {
		t.Fatalf("decode deleted worker response: %v", err)
	}
	if deletedWorkerBody.Session.Status != "idle" || deletedWorkerBody.Session.ActiveTurn != nil {
		t.Fatalf("deleted worker state = status %q turn %#v, want idle with no active turn", deletedWorkerBody.Session.Status, deletedWorkerBody.Session.ActiveTurn)
	}
	hardDeleteWorker := requestJSON(
		t,
		server,
		http.MethodDelete,
		"/api/cloud/v1/sessions/"+firstBody.Session.ID,
		"user-one",
		nil,
		nil,
	)
	defer hardDeleteWorker.Body.Close()
	if hardDeleteWorker.StatusCode != http.StatusNoContent {
		t.Fatalf("hard delete worker status = %d, want 204", hardDeleteWorker.StatusCode)
	}
	deletedWorkerMissing := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/sessions/"+firstBody.Session.ID,
		"user-one",
		nil,
		nil,
	)
	defer deletedWorkerMissing.Body.Close()
	if deletedWorkerMissing.StatusCode != http.StatusNotFound {
		t.Fatalf("hard deleted worker get status = %d, want 404", deletedWorkerMissing.StatusCode)
	}

	otherUser := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/sessions/"+firstBody.Session.ID,
		"user-two",
		nil,
		nil,
	)
	defer otherUser.Body.Close()
	if otherUser.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-user session status = %d, want 404", otherUser.StatusCode)
	}
	deleteProject := requestJSON(
		t,
		server,
		http.MethodDelete,
		"/api/cloud/v1/projects/"+projectBody.Project.ID,
		"user-one",
		nil,
		nil,
	)
	defer deleteProject.Body.Close()
	if deleteProject.StatusCode != http.StatusNoContent {
		t.Fatalf("delete project status = %d, want 204", deleteProject.StatusCode)
	}
	listAfterProjectDelete := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/projects",
		"user-one",
		nil,
		nil,
	)
	defer listAfterProjectDelete.Body.Close()
	if listAfterProjectDelete.StatusCode != http.StatusOK {
		t.Fatalf("list projects after delete status = %d, want 200", listAfterProjectDelete.StatusCode)
	}
	var listAfterProjectDeleteBody struct {
		Projects []clouddomain.Project `json:"projects"`
	}
	if err := json.NewDecoder(listAfterProjectDelete.Body).Decode(&listAfterProjectDeleteBody); err != nil {
		t.Fatalf("decode projects after delete: %v", err)
	}
	if len(listAfterProjectDeleteBody.Projects) != 0 {
		t.Fatalf("projects after delete = %d, want 0", len(listAfterProjectDeleteBody.Projects))
	}
}

func TestWorkerAndBrowserTerminalReplayLiveRouting(t *testing.T) {
	server, store := integrationAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	account, err := store.EnsureAccount(ctx, tokenID("user-one"), "User One")
	if err != nil {
		t.Fatalf("EnsureAccount() error = %v", err)
	}
	project, err := store.CreateProject(ctx, account.ID, cloudpostgres.CreateProjectInput{
		DisplayName:   "Terminal E2E",
		RepositoryURL: "https://github.com/example/" + uuid.NewString(),
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	created, err := store.CreateSession(ctx, account.ID, cloudpostgres.CreateSessionInput{
		IdempotencyKey: uuid.NewString(),
		ProjectID:      project.ID,
		Kind:           "worker",
		Harness:        "fake",
		DisplayName:    "terminal-e2e",
		Resource:       clouddomain.DefaultResourceProfile(),
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	bootstrapTicket, err := store.IssueAccessTicket(
		ctx,
		account.ID,
		created.Session.ID,
		"worker_bootstrap",
		[]string{"worker:connect", "worker:event", "worker:terminal"},
		time.Minute,
	)
	if err != nil {
		t.Fatalf("IssueAccessTicket(worker) error = %v", err)
	}
	bootstrapResponse := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/bootstrap",
		"",
		map[string]any{
			"bootstrapToken": bootstrapTicket,
			"version":        "test",
			"capabilities":   []string{"pty.v1"},
		},
		nil,
	)
	defer bootstrapResponse.Body.Close()
	if bootstrapResponse.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap status = %d", bootstrapResponse.StatusCode)
	}
	var bootstrapBody struct {
		WorkerToken string `json:"workerToken"`
		WorkerID    string `json:"workerId"`
		Epoch       int64  `json:"epoch"`
	}
	if err := json.NewDecoder(bootstrapResponse.Body).Decode(&bootstrapBody); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	retryResponse := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/bootstrap",
		"",
		map[string]any{
			"bootstrapToken": bootstrapTicket,
			"version":        "test",
			"capabilities":   []string{"pty.v1"},
		},
		nil,
	)
	defer retryResponse.Body.Close()
	if retryResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bootstrap retry status = %d, want 401", retryResponse.StatusCode)
	}
	markWorkerReady(t, server, bootstrapBody.WorkerToken)

	workerURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/cloud/v1/worker/connect"
	workerHeaders := http.Header{}
	workerHeaders.Set("Authorization", "Worker "+bootstrapBody.WorkerToken)
	workerSocket, _, err := websocket.Dial(ctx, workerURL, &websocket.DialOptions{HTTPHeader: workerHeaders})
	if err != nil {
		t.Fatalf("dial worker socket: %v", err)
	}
	defer workerSocket.Close(websocket.StatusNormalClosure, "test complete")

	readyResponse := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/events",
		"",
		map[string]any{
			"type": "agent.activity",
			"payload": map[string]any{
				"event":       "session-start",
				"state":       "",
				"hasActivity": false,
				"native":      map[string]string{"session_id": "native-session"},
			},
		},
		map[string]string{"Authorization": "Worker " + bootstrapBody.WorkerToken},
	)
	defer readyResponse.Body.Close()
	if readyResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("agent readiness event status = %d", readyResponse.StatusCode)
	}
	_, readyCommandData, err := workerSocket.Read(ctx)
	if err != nil {
		t.Fatalf("read agent readiness command: %v", err)
	}
	var readyCommand cloudworkerhub.Command
	if err := json.Unmarshal(readyCommandData, &readyCommand); err != nil {
		t.Fatalf("decode agent readiness command: %v", err)
	}
	if readyCommand.Type != "agent_ready" {
		t.Fatalf("agent readiness command = %#v", readyCommand)
	}

	ticketResponse := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/sessions/"+string(created.Session.ID)+"/terminal-ticket",
		"user-one",
		map[string]any{},
		nil,
	)
	defer ticketResponse.Body.Close()
	var ticketBody struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(ticketResponse.Body).Decode(&ticketBody); err != nil {
		t.Fatalf("decode terminal ticket: %v", err)
	}
	terminalURL := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/cloud/v1/terminal?ticket=" + ticketBody.Ticket
	terminalHeaders := http.Header{}
	terminalHeaders.Set("Origin", "http://127.0.0.1:5174")
	terminalSocket, _, err := websocket.Dial(ctx, terminalURL, &websocket.DialOptions{HTTPHeader: terminalHeaders})
	if err != nil {
		t.Fatalf("dial terminal socket: %v", err)
	}
	defer terminalSocket.Close(websocket.StatusNormalClosure, "test complete")
	_, resetMessageData, err := terminalSocket.Read(ctx)
	if err != nil {
		t.Fatalf("read terminal reset: %v", err)
	}
	var resetMessage terminalServerMessage
	if err := json.Unmarshal(resetMessageData, &resetMessage); err != nil {
		t.Fatalf("decode terminal reset: %v", err)
	}
	if resetMessage.Type != "reset" || resetMessage.Sequence <= 0 {
		t.Fatalf("terminal reset = %#v", resetMessage)
	}

	output := base64.StdEncoding.EncodeToString([]byte("worker output"))
	eventResponse := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/events",
		"",
		map[string]any{"type": "terminal.output", "payload": map[string]string{"data": output, "encoding": "base64"}},
		map[string]string{"Authorization": "Worker " + bootstrapBody.WorkerToken},
	)
	defer eventResponse.Body.Close()
	if eventResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("worker event status = %d", eventResponse.StatusCode)
	}
	_, browserMessage, err := terminalSocket.Read(ctx)
	if err != nil {
		t.Fatalf("read browser output: %v", err)
	}
	var outputMessage terminalServerMessage
	if err := json.Unmarshal(browserMessage, &outputMessage); err != nil {
		t.Fatalf("decode browser output: %v", err)
	}
	if outputMessage.Type != "output" || outputMessage.Data != output {
		t.Fatalf("browser output = %#v", outputMessage)
	}

	input := base64.StdEncoding.EncodeToString([]byte("hello"))
	if err := terminalSocket.Write(ctx, websocket.MessageText, []byte(`{"type":"input","data":"`+input+`"}`)); err != nil {
		t.Fatalf("write browser input: %v", err)
	}
	_, workerMessage, err := workerSocket.Read(ctx)
	if err != nil {
		t.Fatalf("read worker command: %v", err)
	}
	var command struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(workerMessage, &command); err != nil {
		t.Fatalf("decode worker command: %v", err)
	}
	if command.Type != "input" || command.Data != input {
		t.Fatalf("worker command = %#v", command)
	}

	workspaceRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		server.URL+"/api/cloud/v1/sessions/"+string(created.Session.ID)+"/workspace/files?path=",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRequest.Header.Set("Authorization", "Bearer user-one")
	type workspaceHTTPResult struct {
		response *http.Response
		err      error
	}
	workspaceResult := make(chan workspaceHTTPResult, 1)
	go func() {
		response, requestErr := server.Client().Do(workspaceRequest)
		workspaceResult <- workspaceHTTPResult{response: response, err: requestErr}
	}()
	_, workspaceCommandData, err := workerSocket.Read(ctx)
	if err != nil {
		t.Fatalf("read workspace command: %v", err)
	}
	var workspaceCommand cloudworkerhub.Command
	if err := json.Unmarshal(workspaceCommandData, &workspaceCommand); err != nil {
		t.Fatalf("decode workspace command: %v", err)
	}
	if workspaceCommand.Type != "workspace_request" ||
		workspaceCommand.Action != "list" ||
		workspaceCommand.RequestID == "" {
		t.Fatalf("workspace command = %#v", workspaceCommand)
	}
	workspaceResponse := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/workspace-response",
		"",
		map[string]any{
			"requestId": workspaceCommand.RequestID,
			"payload": map[string]any{
				"path": "",
				"entries": []map[string]any{{
					"name": "README.md",
					"path": "README.md",
				}},
			},
		},
		map[string]string{"Authorization": "Worker " + bootstrapBody.WorkerToken},
	)
	workspaceResponse.Body.Close()
	if workspaceResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("workspace response status = %d", workspaceResponse.StatusCode)
	}
	result := <-workspaceResult
	if result.err != nil {
		t.Fatalf("workspace request failed: %v", result.err)
	}
	defer result.response.Body.Close()
	if result.response.StatusCode != http.StatusOK {
		t.Fatalf("workspace request status = %d", result.response.StatusCode)
	}
	var workspaceBody struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(result.response.Body).Decode(&workspaceBody); err != nil {
		t.Fatal(err)
	}
	if len(workspaceBody.Entries) != 1 || workspaceBody.Entries[0].Name != "README.md" {
		t.Fatalf("workspace body = %#v", workspaceBody)
	}
}

func TestChatMessageAuthIdempotencyAndWorkerReplay(t *testing.T) {
	server, store, api := integrationAPIWithServer(t)
	api.workerReplayWait = 25 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	account, err := store.EnsureAccount(ctx, tokenID("user-one"), "User One")
	if err != nil {
		t.Fatalf("EnsureAccount() error = %v", err)
	}
	project, err := store.CreateProject(ctx, account.ID, cloudpostgres.CreateProjectInput{
		DisplayName:   "Chat E2E",
		RepositoryURL: "https://github.com/example/" + uuid.NewString(),
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	created, err := store.CreateSession(ctx, account.ID, cloudpostgres.CreateSessionInput{
		IdempotencyKey: uuid.NewString(),
		ProjectID:      project.ID,
		Kind:           "worker",
		Harness:        "fake",
		DisplayName:    "chat-e2e",
		Prompt:         "initial task",
		Resource:       clouddomain.DefaultResourceProfile(),
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	initialEvents, err := store.ChatEventsAfter(ctx, account.ID, created.Session.ID, 0, 10)
	if err != nil || len(initialEvents) != 1 {
		t.Fatalf("initial chat events = %#v, error = %v", initialEvents, err)
	}
	initialEvent := initialEvents[0]
	initialTurn, err := store.GetActiveTurn(ctx, account.ID, created.Session.ID)
	if err != nil || initialTurn == nil || initialTurn.State != "provisioning" {
		t.Fatalf("initial durable turn = %#v, error = %v", initialTurn, err)
	}
	if _, err := store.TransitionActiveTurn(
		ctx,
		account.ID,
		created.Session.ID,
		"completed",
		"",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSandboxDesiredState(
		ctx,
		account.ID,
		created.Session.ID,
		"deleted",
	); err != nil {
		t.Fatal(err)
	}
	path := "/api/cloud/v1/sessions/" + string(created.Session.ID) + "/messages"
	key := uuid.NewString()
	unauthorized := requestJSON(
		t,
		server,
		http.MethodPost,
		path,
		"user-two",
		map[string]string{"text": "hello Claude"},
		map[string]string{"Idempotency-Key": key},
	)
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-account message status = %d, want 404", unauthorized.StatusCode)
	}

	first := requestJSON(
		t,
		server,
		http.MethodPost,
		path,
		"user-one",
		map[string]string{"text": "hello Claude"},
		map[string]string{"Idempotency-Key": key},
	)
	defer first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first message status = %d", first.StatusCode)
	}
	var firstBody struct {
		Event clouddomain.Event `json:"event"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstBody); err != nil {
		t.Fatalf("decode first message: %v", err)
	}
	if firstBody.Event.Type != "chat.user_message" {
		t.Fatalf("first event type = %q", firstBody.Event.Type)
	}
	wokenSandbox, err := store.GetSandbox(ctx, account.ID, created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wokenSandbox.DesiredState != "running" {
		t.Fatalf("message desired state = %q, want running", wokenSandbox.DesiredState)
	}
	wokenSession, err := store.GetSession(ctx, account.ID, created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wokenSession.ActivityState != "active" || wokenSession.Status != "working" {
		t.Fatalf("message session = %#v, want active/working", wokenSession)
	}

	retry := requestJSON(
		t,
		server,
		http.MethodPost,
		path,
		"user-one",
		map[string]string{"text": "hello Claude"},
		map[string]string{"Idempotency-Key": key},
	)
	defer retry.Body.Close()
	if retry.StatusCode != http.StatusAccepted {
		t.Fatalf("retry message status = %d", retry.StatusCode)
	}
	var retryBody struct {
		Event clouddomain.Event `json:"event"`
	}
	if err := json.NewDecoder(retry.Body).Decode(&retryBody); err != nil {
		t.Fatalf("decode retried message: %v", err)
	}
	if retryBody.Event.Sequence != firstBody.Event.Sequence ||
		string(retryBody.Event.Payload) != string(firstBody.Event.Payload) {
		t.Fatalf("retry event = %#v, want %#v", retryBody.Event, firstBody.Event)
	}
	activeConflict := requestJSON(
		t,
		server,
		http.MethodPost,
		path,
		"user-one",
		map[string]string{"text": "overlapping turn"},
		map[string]string{"Idempotency-Key": uuid.NewString()},
	)
	activeConflict.Body.Close()
	if activeConflict.StatusCode != http.StatusConflict {
		t.Fatalf("active turn status = %d, want 409", activeConflict.StatusCode)
	}
	conflict := requestJSON(
		t,
		server,
		http.MethodPost,
		path,
		"user-one",
		map[string]string{"text": "different text"},
		map[string]string{"Idempotency-Key": key},
	)
	conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting retry status = %d, want 409", conflict.StatusCode)
	}

	if _, err := store.AppendEvent(
		ctx,
		account.ID,
		created.Session.ID,
		"terminal.output",
		json.RawMessage(`{"data":"ignored"}`),
	); err != nil {
		t.Fatalf("AppendEvent(non-chat) error = %v", err)
	}
	eventsResponse := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/sessions/"+string(created.Session.ID)+"/chat-events?after=0&limit=1",
		"user-one",
		nil,
		nil,
	)
	defer eventsResponse.Body.Close()
	if eventsResponse.StatusCode != http.StatusOK {
		t.Fatalf("chat events status = %d", eventsResponse.StatusCode)
	}
	var eventsBody struct {
		Events []clouddomain.Event `json:"events"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&eventsBody); err != nil {
		t.Fatalf("decode chat events: %v", err)
	}
	if len(eventsBody.Events) != 1 || eventsBody.Events[0].Sequence != initialEvent.Sequence {
		t.Fatalf("chat events = %#v", eventsBody.Events)
	}

	bootstrapTicket, err := store.IssueAccessTicket(
		ctx,
		account.ID,
		created.Session.ID,
		"worker_bootstrap",
		[]string{"worker:connect", "worker:event", "worker:terminal"},
		time.Minute,
	)
	if err != nil {
		t.Fatalf("IssueAccessTicket(worker) error = %v", err)
	}
	bootstrapResponse := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/bootstrap",
		"",
		map[string]any{
			"bootstrapToken": bootstrapTicket,
			"version":        "test",
			"capabilities":   []string{"chat.stream-json.v1"},
		},
		nil,
	)
	defer bootstrapResponse.Body.Close()
	var bootstrapBody struct {
		WorkerToken string `json:"workerToken"`
	}
	if err := json.NewDecoder(bootstrapResponse.Body).Decode(&bootstrapBody); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	workerEvent := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/events",
		"",
		map[string]any{
			"type":    "chat.assistant_delta",
			"payload": map[string]string{"text": "hello"},
		},
		map[string]string{"Authorization": "Worker " + bootstrapBody.WorkerToken},
	)
	workerEvent.Body.Close()
	if workerEvent.StatusCode != http.StatusAccepted {
		t.Fatalf("chat worker event status = %d", workerEvent.StatusCode)
	}
	workerURL := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/cloud/v1/worker/connect?after=0"
	headers := http.Header{}
	headers.Set("Authorization", "Worker "+bootstrapBody.WorkerToken)
	socket, _, err := websocket.Dial(ctx, workerURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatalf("dial worker socket: %v", err)
	}
	defer socket.Close(websocket.StatusNormalClosure, "test complete")
	for _, expected := range []struct {
		sequence int64
		text     string
	}{
		{sequence: firstBody.Event.Sequence, text: "hello Claude"},
	} {
		_, encodedCommand, readErr := socket.Read(ctx)
		if readErr != nil {
			t.Fatalf("read replayed prompt: %v", readErr)
		}
		var command cloudworkerhub.Command
		if err := json.Unmarshal(encodedCommand, &command); err != nil {
			t.Fatalf("decode replayed prompt: %v", err)
		}
		decodedPrompt, err := base64.StdEncoding.DecodeString(command.Data)
		if err != nil {
			t.Fatalf("decode prompt data: %v", err)
		}
		if command.Type != "prompt" ||
			command.Sequence != expected.sequence ||
			string(decodedPrompt) != expected.text {
			t.Fatalf("replayed prompt command = %#v text=%q", command, decodedPrompt)
		}
	}
	if _, err := store.TransitionActiveTurn(
		ctx,
		account.ID,
		created.Session.ID,
		"completed",
		"",
	); err != nil {
		t.Fatalf("complete first replayed turn: %v", err)
	}
	followUp, _, err := store.AppendUserMessage(
		ctx,
		account.ID,
		created.Session.ID,
		uuid.NewString(),
		"periodic replay",
	)
	if err != nil {
		t.Fatalf("append follow-up without live notification: %v", err)
	}
	var periodicPrompt *cloudworkerhub.Command
	for range 4 {
		_, encodedCommand, err := socket.Read(ctx)
		if err != nil {
			t.Fatalf("read periodically replayed prompt: %v", err)
		}
		var command cloudworkerhub.Command
		if err := json.Unmarshal(encodedCommand, &command); err != nil {
			t.Fatalf("decode periodically replayed prompt: %v", err)
		}
		if command.Type != "prompt" {
			t.Fatalf("periodically replayed command = %#v", command)
		}
		if command.Sequence == followUp.Sequence {
			periodicPrompt = &command
			break
		}
		if command.Sequence > followUp.Sequence {
			t.Fatalf("unexpected replay sequence = %d", command.Sequence)
		}
	}
	if periodicPrompt == nil {
		t.Fatal("follow-up prompt was not periodically replayed")
	}
	decodedPrompt, err := base64.StdEncoding.DecodeString(periodicPrompt.Data)
	if err != nil {
		t.Fatalf("decode periodically replayed prompt data: %v", err)
	}
	if string(decodedPrompt) != "periodic replay" {
		t.Fatalf("periodically replayed prompt text = %q", decodedPrompt)
	}
}

func TestAgentProviderConnectionValidationAndAccountIsolation(t *testing.T) {
	server, _ := integrationAPI(t)
	for _, token := range []string{"user-one", "user-two"} {
		response := requestJSON(
			t,
			server,
			http.MethodDelete,
			"/api/cloud/v1/provider-connections/agents/cursor",
			token,
			nil,
			nil,
		)
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("initial %s delete status = %d", token, response.StatusCode)
		}
	}

	for _, test := range []struct {
		name   string
		agent  string
		body   map[string]any
		status int
		code   string
	}{
		{
			name:   "unknown agent",
			agent:  "other",
			body:   map[string]any{"credentialType": "api_key", "secret": "secret"},
			status: http.StatusBadRequest,
			code:   "INVALID_AGENT",
		},
		{
			name:   "unsupported credential type",
			agent:  "cursor",
			body:   map[string]any{"credentialType": "oauth_token", "secret": "secret"},
			status: http.StatusBadRequest,
			code:   "INVALID_CREDENTIAL_TYPE",
		},
		{
			name:   "empty secret",
			agent:  "claude-code",
			body:   map[string]any{"credentialType": "api_key", "secret": " \n "},
			status: http.StatusBadRequest,
			code:   "INVALID_AGENT_CREDENTIAL",
		},
		{
			name:   "oversized secret",
			agent:  "codex",
			body:   map[string]any{"credentialType": "api_key", "secret": strings.Repeat("x", maxAgentCredentialBytes+1)},
			status: http.StatusBadRequest,
			code:   "INVALID_AGENT_CREDENTIAL",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := requestJSON(
				t,
				server,
				http.MethodPut,
				"/api/cloud/v1/provider-connections/agents/"+test.agent,
				"user-one",
				test.body,
				nil,
			)
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Code != test.code {
				t.Fatalf("code = %q, want %q", body.Code, test.code)
			}
		})
	}

	put := requestJSON(
		t,
		server,
		http.MethodPut,
		"/api/cloud/v1/provider-connections/agents/cursor",
		"user-one",
		map[string]any{"credentialType": "api_key", "secret": "  cursor-secret  "},
		nil,
	)
	defer put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", put.StatusCode)
	}
	encodedPut, err := io.ReadAll(put.Body)
	if err != nil {
		t.Fatalf("read put response: %v", err)
	}
	if bytes.Contains(encodedPut, []byte("cursor-secret")) ||
		bytes.Contains(encodedPut, []byte("encryptedSecret")) {
		t.Fatalf("put response exposed credential material: %s", encodedPut)
	}

	otherAccountList := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/provider-connections",
		"user-two",
		nil,
		nil,
	)
	defer otherAccountList.Body.Close()
	var otherListBody struct {
		ProviderConnections []cloudpostgres.ProviderConnection `json:"providerConnections"`
	}
	if err := json.NewDecoder(otherAccountList.Body).Decode(&otherListBody); err != nil {
		t.Fatalf("decode other account list: %v", err)
	}
	for _, connection := range otherListBody.ProviderConnections {
		if connection.Provider == "cursor" && connection.Label == "default" {
			t.Fatalf("other account can see user-one connection")
		}
	}

	otherDelete := requestJSON(
		t,
		server,
		http.MethodDelete,
		"/api/cloud/v1/provider-connections/agents/cursor",
		"user-two",
		nil,
		nil,
	)
	defer otherDelete.Body.Close()
	if otherDelete.StatusCode != http.StatusNoContent {
		t.Fatalf("other account delete status = %d", otherDelete.StatusCode)
	}

	userList := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/provider-connections",
		"user-one",
		nil,
		nil,
	)
	defer userList.Body.Close()
	var userListBody struct {
		ProviderConnections []cloudpostgres.ProviderConnection `json:"providerConnections"`
	}
	if err := json.NewDecoder(userList.Body).Decode(&userListBody); err != nil {
		t.Fatalf("decode user account list: %v", err)
	}
	found := false
	for _, connection := range userListBody.ProviderConnections {
		if connection.Provider == "cursor" && connection.Label == "default" {
			found = true
			var config agentConnectionConfig
			if err := json.Unmarshal(connection.Config, &config); err != nil {
				t.Fatalf("decode connection config: %v", err)
			}
			if config.CredentialType != "api_key" {
				t.Fatalf("credential type = %q", config.CredentialType)
			}
		}
	}
	if !found {
		t.Fatalf("user-one cursor connection was deleted by user-two")
	}
}

func TestAgentConnectionGatesSessionAndBootstrapsCredential(t *testing.T) {
	server, store := integrationAPI(t)
	deleteResponse := requestJSON(
		t,
		server,
		http.MethodDelete,
		"/api/cloud/v1/provider-connections/agents/claude-code",
		"user-one",
		nil,
		nil,
	)
	deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("initial delete status = %d", deleteResponse.StatusCode)
	}

	projectResponse := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/projects",
		"user-one",
		map[string]any{
			"displayName":   "Credential bootstrap",
			"repositoryUrl": "https://github.com/example/" + uuid.NewString(),
			"defaultBranch": "main",
		},
		nil,
	)
	defer projectResponse.Body.Close()
	var projectBody struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.NewDecoder(projectResponse.Body).Decode(&projectBody); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	sessionInput := map[string]any{
		"projectId":   projectBody.Project.ID,
		"kind":        "worker",
		"harness":     "claude-code",
		"displayName": "credential-bootstrap",
		"prompt":      "Test credentials",
	}
	missing := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/sessions",
		"user-one",
		sessionInput,
		map[string]string{"Idempotency-Key": uuid.NewString()},
	)
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing connection status = %d, want 400", missing.StatusCode)
	}
	var missingBody struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(missing.Body).Decode(&missingBody); err != nil {
		t.Fatalf("decode missing connection response: %v", err)
	}
	if missingBody.Code != "AGENT_CONNECTION_REQUIRED" {
		t.Fatalf("missing connection code = %q", missingBody.Code)
	}

	oauthSecret := "sk-ant-oat01-" + strings.Repeat("a", 80)
	put := requestJSON(
		t,
		server,
		http.MethodPut,
		"/api/cloud/v1/provider-connections/agents/claude-code",
		"user-one",
		map[string]any{"credentialType": "oauth_token", "secret": oauthSecret},
		nil,
	)
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("put connection status = %d", put.StatusCode)
	}
	created := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/sessions",
		"user-one",
		sessionInput,
		map[string]string{"Idempotency-Key": uuid.NewString()},
	)
	defer created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("created session status = %d", created.StatusCode)
	}
	var createdBody struct {
		Session struct {
			ID clouddomain.SessionID `json:"id"`
		} `json:"session"`
	}
	if err := json.NewDecoder(created.Body).Decode(&createdBody); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	account, err := store.EnsureAccount(context.Background(), tokenID("user-one"), "User One")
	if err != nil {
		t.Fatalf("EnsureAccount() error = %v", err)
	}
	ticket, err := store.IssueAccessTicket(
		context.Background(),
		account.ID,
		createdBody.Session.ID,
		"worker_bootstrap",
		[]string{"worker:connect"},
		time.Minute,
	)
	if err != nil {
		t.Fatalf("IssueAccessTicket() error = %v", err)
	}
	bootstrap := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/bootstrap",
		"",
		map[string]any{
			"bootstrapToken": ticket,
			"version":        "test",
			"capabilities":   []string{"pty.v1"},
		},
		nil,
	)
	defer bootstrap.Body.Close()
	if bootstrap.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap status = %d", bootstrap.StatusCode)
	}
	var bootstrapBody cloudworker.BootstrapResponse
	if err := json.NewDecoder(bootstrap.Body).Decode(&bootstrapBody); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if bootstrapBody.AgentCredential == nil {
		t.Fatal("bootstrap agentCredential = nil")
	}
	if *bootstrapBody.AgentCredential != (cloudworker.AgentCredential{
		Provider:       "claude-code",
		CredentialType: "oauth_token",
		Secret:         oauthSecret,
	}) {
		t.Fatalf("bootstrap agentCredential = %#v", bootstrapBody.AgentCredential)
	}
}

func TestWorkerOrchestrationIsProjectScoped(t *testing.T) {
	server, store := integrationAPI(t)
	ctx := context.Background()
	connection := requestJSON(
		t,
		server,
		http.MethodPut,
		"/api/cloud/v1/provider-connections/agents/cursor",
		"user-one",
		map[string]string{"credentialType": "api_key", "secret": "cursor-secret"},
		nil,
	)
	connection.Body.Close()
	if connection.StatusCode != http.StatusOK {
		t.Fatalf("cursor connection status = %d", connection.StatusCode)
	}
	account, err := store.EnsureAccount(ctx, tokenID("user-one"), "User One")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, account.ID, cloudpostgres.CreateProjectInput{
		DisplayName:   "Orchestration",
		RepositoryURL: "https://github.com/example/" + uuid.NewString(),
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := store.CreateSession(ctx, account.ID, cloudpostgres.CreateSessionInput{
		IdempotencyKey: uuid.NewString(),
		ProjectID:      project.ID,
		Kind:           "orchestrator",
		Harness:        "cursor",
		DisplayName:    "orchestrator",
		Resource:       clouddomain.DefaultResourceProfile(),
		Provider:       "daytona",
	})
	if err != nil {
		t.Fatal(err)
	}
	unscopedToken := bootstrapWorker(t, server, store, account.ID, parent.Session.ID, []string{
		"worker:connect",
		"worker:event",
		"worker:terminal",
	})
	unscoped := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/worker/orchestrate/sessions",
		"",
		nil,
		map[string]string{
			"Authorization":   "Worker " + unscopedToken,
			"X-AO-Session-ID": string(parent.Session.ID),
		},
	)
	unscoped.Body.Close()
	if unscoped.StatusCode != http.StatusForbidden {
		t.Fatalf("unscoped orchestration status = %d, want 403", unscoped.StatusCode)
	}
	token := bootstrapWorker(t, server, store, account.ID, parent.Session.ID, []string{
		"worker:connect",
		"worker:event",
		"worker:terminal",
		"worker:orchestrate",
	})
	headers := map[string]string{
		"Authorization":   "Worker " + token,
		"X-AO-Session-ID": string(parent.Session.ID),
		"Idempotency-Key": uuid.NewString(),
	}
	spawn := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/orchestrate/sessions",
		"",
		map[string]string{
			"displayName": "worker-one",
			"prompt":      "fix the tests",
		},
		headers,
	)
	defer spawn.Body.Close()
	if spawn.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(spawn.Body)
		t.Fatalf("spawn status = %d: %s", spawn.StatusCode, payload)
	}
	var spawned cloudpostgres.CreateSessionResult
	if err := json.NewDecoder(spawn.Body).Decode(&spawned); err != nil {
		t.Fatal(err)
	}
	if spawned.Session.ProjectID != project.ID ||
		spawned.Session.Harness != parent.Session.Harness ||
		spawned.Sandbox.Provider != parent.Sandbox.Provider {
		t.Fatalf("spawned result = %#v", spawned)
	}

	list := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/worker/orchestrate/sessions",
		"",
		nil,
		map[string]string{
			"Authorization":   "Worker " + token,
			"X-AO-Session-ID": string(parent.Session.ID),
		},
	)
	defer list.Body.Close()
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", list.StatusCode)
	}
	var listed struct {
		Sessions []clouddomain.Session `json:"sessions"`
	}
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 2 {
		t.Fatalf("listed sessions = %#v", listed.Sessions)
	}
	activeTurn, err := store.GetActiveTurn(ctx, account.ID, spawned.Session.ID)
	if err != nil || activeTurn == nil {
		t.Fatalf("spawned active turn = %#v, error = %v", activeTurn, err)
	}
	resultPayload, _ := json.Marshal(map[string]any{
		"turnId": activeTurn.ID,
		"text":   "The delegated tests pass.",
	})
	if _, err := store.AppendEvent(
		ctx,
		account.ID,
		spawned.Session.ID,
		"chat.assistant_message",
		resultPayload,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionActiveTurn(
		ctx,
		account.ID,
		spawned.Session.ID,
		"completed",
		"",
	); err != nil {
		t.Fatal(err)
	}
	inspect := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/worker/orchestrate/sessions/"+string(spawned.Session.ID)+"/inspection",
		"",
		nil,
		map[string]string{
			"Authorization":   "Worker " + token,
			"X-AO-Session-ID": string(parent.Session.ID),
		},
	)
	defer inspect.Body.Close()
	if inspect.StatusCode != http.StatusOK {
		t.Fatalf("inspection status = %d", inspect.StatusCode)
	}
	var inspection workerInspectionResponse
	if err := json.NewDecoder(inspect.Body).Decode(&inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.Turn == nil ||
		inspection.Turn.State != "completed" ||
		!inspection.ResultAvailable ||
		inspection.Result != "The delegated tests pass." {
		t.Fatalf("inspection = %#v", inspection)
	}

	otherProject, err := store.CreateProject(ctx, account.ID, cloudpostgres.CreateProjectInput{
		DisplayName:   "Other",
		RepositoryURL: "https://github.com/example/" + uuid.NewString(),
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateSession(ctx, account.ID, cloudpostgres.CreateSessionInput{
		IdempotencyKey: uuid.NewString(),
		ProjectID:      otherProject.ID,
		Kind:           "worker",
		Harness:        "fake",
		DisplayName:    "other-worker",
		Resource:       clouddomain.DefaultResourceProfile(),
	})
	if err != nil {
		t.Fatal(err)
	}
	crossProject := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/orchestrate/sessions/"+string(other.Session.ID)+"/messages",
		"",
		map[string]string{"text": "not allowed"},
		headers,
	)
	crossProject.Body.Close()
	if crossProject.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project send status = %d, want 404", crossProject.StatusCode)
	}
	crossProjectInspect := requestJSON(
		t,
		server,
		http.MethodGet,
		"/api/cloud/v1/worker/orchestrate/sessions/"+string(other.Session.ID)+"/inspection",
		"",
		nil,
		headers,
	)
	crossProjectInspect.Body.Close()
	if crossProjectInspect.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project inspection status = %d, want 404", crossProjectInspect.StatusCode)
	}
}

func TestBrowserInterruptAndWorkerTurnActivity(t *testing.T) {
	server, store := integrationAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	account, err := store.EnsureAccount(ctx, tokenID("user-one"), "User One")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, account.ID, cloudpostgres.CreateProjectInput{
		DisplayName:   "Interrupt",
		RepositoryURL: "https://github.com/example/" + uuid.NewString(),
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(ctx, account.ID, cloudpostgres.CreateSessionInput{
		IdempotencyKey: uuid.NewString(),
		ProjectID:      project.ID,
		Kind:           "worker",
		Harness:        "fake",
		DisplayName:    "interrupt-worker",
		Resource:       clouddomain.DefaultResourceProfile(),
	})
	if err != nil {
		t.Fatal(err)
	}
	token := bootstrapWorker(t, server, store, account.ID, session.Session.ID, []string{
		"worker:connect",
		"worker:event",
		"worker:terminal",
	})
	markWorkerReady(t, server, token)
	workerURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/cloud/v1/worker/connect"
	workerHeaders := http.Header{}
	workerHeaders.Set("Authorization", "Worker "+token)
	socket, _, err := websocket.Dial(ctx, workerURL, &websocket.DialOptions{HTTPHeader: workerHeaders})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close(websocket.StatusNormalClosure, "test complete")
	if _, _, err := store.AppendUserMessage(
		ctx,
		account.ID,
		session.Session.ID,
		uuid.NewString(),
		"long running task",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionActiveTurn(
		ctx,
		account.ID,
		session.Session.ID,
		"running",
		"",
	); err != nil {
		t.Fatal(err)
	}

	interrupt := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/sessions/"+string(session.Session.ID)+"/interrupt",
		"user-one",
		map[string]any{},
		nil,
	)
	defer interrupt.Body.Close()
	if interrupt.StatusCode != http.StatusAccepted {
		t.Fatalf("interrupt status = %d", interrupt.StatusCode)
	}
	interruptedSession, err := store.GetSession(ctx, account.ID, session.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if interruptedSession.ActivityState != "active" ||
		interruptedSession.ActiveTurn == nil ||
		interruptedSession.ActiveTurn.State != "cancel_requested" {
		t.Fatalf("interrupt session = %#v, want active cancellation", interruptedSession)
	}
	var interruptCommand *cloudworkerhub.Command
	for range 2 {
		_, encodedCommand, err := socket.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var command cloudworkerhub.Command
		if err := json.Unmarshal(encodedCommand, &command); err != nil {
			t.Fatal(err)
		}
		if command.Type == "interrupt" {
			interruptCommand = &command
			break
		}
		if command.Type != "prompt" {
			t.Fatalf("unexpected command before interrupt = %#v", command)
		}
	}
	if interruptCommand == nil || interruptCommand.Sequence <= 0 {
		t.Fatalf("interrupt command = %#v", interruptCommand)
	}

	for _, event := range []struct {
		eventType string
		wantState string
	}{
		{eventType: "chat.turn_started", wantState: "active"},
		{eventType: "chat.turn_interrupted", wantState: "idle"},
		{eventType: "chat.turn_completed", wantState: "idle"},
	} {
		response := requestJSON(
			t,
			server,
			http.MethodPost,
			"/api/cloud/v1/worker/events",
			"",
			map[string]any{"type": event.eventType, "payload": map[string]any{}},
			map[string]string{"Authorization": "Worker " + token},
		)
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("%s status = %d", event.eventType, response.StatusCode)
		}
		got, err := store.GetSession(ctx, account.ID, session.Session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ActivityState != event.wantState {
			t.Fatalf("%s activity = %q, want %q", event.eventType, got.ActivityState, event.wantState)
		}
	}

	if _, _, err := store.AppendUserMessage(
		ctx,
		account.ID,
		session.Session.ID,
		uuid.NewString(),
		"hook-driven turn",
	); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		name       string
		state      string
		wantActive bool
		wantTurn   string
	}{
		{name: "user-prompt-submit", state: "active", wantActive: true, wantTurn: "running"},
		{name: "stop", state: "idle", wantActive: false},
	} {
		response := requestJSON(
			t,
			server,
			http.MethodPost,
			"/api/cloud/v1/worker/events",
			"",
			map[string]any{
				"type": "agent.activity",
				"payload": map[string]any{
					"event":       event.name,
					"state":       event.state,
					"hasActivity": true,
				},
			},
			map[string]string{"Authorization": "Worker " + token},
		)
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("agent.activity %s status = %d", event.name, response.StatusCode)
		}
		got, err := store.GetSession(ctx, account.ID, session.Session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ActivityState != event.state {
			t.Fatalf("agent.activity %s state = %q, want %q", event.name, got.ActivityState, event.state)
		}
		if event.wantActive {
			if got.ActiveTurn == nil || got.ActiveTurn.State != event.wantTurn {
				t.Fatalf("agent.activity %s turn = %#v, want %q", event.name, got.ActiveTurn, event.wantTurn)
			}
		} else if got.ActiveTurn != nil {
			t.Fatalf("agent.activity %s left active turn %#v", event.name, got.ActiveTurn)
		}
	}
}

func bootstrapWorker(
	t *testing.T,
	server *httptest.Server,
	store *cloudpostgres.Store,
	accountID clouddomain.AccountID,
	sessionID clouddomain.SessionID,
	scopes []string,
) string {
	t.Helper()
	ticket, err := store.IssueAccessTicket(
		context.Background(),
		accountID,
		sessionID,
		"worker_bootstrap",
		scopes,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	response := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/bootstrap",
		"",
		map[string]any{"bootstrapToken": ticket, "version": "test", "capabilities": []string{"chat.stream-json.v1"}},
		nil,
	)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("bootstrap status = %d: %s", response.StatusCode, payload)
	}
	var body struct {
		WorkerToken string `json:"workerToken"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.WorkerToken
}

func markWorkerReady(t *testing.T, server *httptest.Server, token string) {
	t.Helper()
	response := requestJSON(
		t,
		server,
		http.MethodPost,
		"/api/cloud/v1/worker/heartbeat",
		"",
		map[string]any{
			"version":      "test",
			"capabilities": []string{"chat.stream-json.v1"},
		},
		map[string]string{"Authorization": "Worker " + token},
	)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("worker heartbeat status = %d: %s", response.StatusCode, payload)
	}
}
