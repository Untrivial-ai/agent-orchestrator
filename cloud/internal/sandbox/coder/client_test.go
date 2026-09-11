package coder

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/coder/websocket"
)

const (
	testWorkspaceID = "c334f2ce-4cfd-4d1e-a985-a58751f0a82e"
	testTemplateID  = "2a2e262c-b31c-4202-946d-a19ad45d1fd2"
	testAgentID     = "0536c201-bd3f-44c7-91cb-f22844bbade1"
)

func TestLifecycle(t *testing.T) {
	t.Parallel()

	var (
		mu          sync.Mutex
		transitions []string
	)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Coder-Session-Token") != "test-token" {
			t.Errorf("missing Coder token header")
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v2/users/ao-integration/workspaces":
			var body createWorkspaceRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode create request: %v", err)
			}
			if body.Name != WorkspaceName("session-1") || body.TemplateID != testTemplateID {
				t.Errorf("unexpected create request: %+v", body)
			}
			if len(body.RichParameterValues) != 2 || body.RichParameterValues[0].Name != "instance_type" ||
				body.RichParameterValues[1].Name != "region" {
				t.Errorf("parameters were not sorted: %+v", body.RichParameterValues)
			}
			writer.WriteHeader(http.StatusCreated)
			writeWorkspace(t, writer, "starting", "connecting", false)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v2/workspaces/"+testWorkspaceID:
			writeWorkspace(t, writer, "running", "connected", true)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v2/users/ao-integration/workspace/"+WorkspaceName("session-1"):
			writeWorkspace(t, writer, "running", "connected", true)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v2/users/ao-integration/workspace/"+WorkspaceName("missing"):
			http.Error(writer, "not found", http.StatusNotFound)
		case request.Method == http.MethodPost && request.URL.Path == "/api/v2/workspaces/"+testWorkspaceID+"/builds":
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode transition: %v", err)
			}
			mu.Lock()
			transitions = append(transitions, body["transition"])
			mu.Unlock()
			writer.WriteHeader(http.StatusCreated)
		default:
			http.Error(writer, "unexpected route", http.StatusNotFound)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	client := newTestClient(t, server.URL, map[string]string{
		"region": "ap-south-1", "instance_type": "t3.medium",
	})

	created, err := client.Create(context.Background(), sandbox.Spec{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.State != sandbox.StateProvisioning {
		t.Fatalf("created state = %q, want provisioning", created.State)
	}
	fetched, err := client.Get(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.State != sandbox.StateRunning || fetched.Target != testAgentID {
		t.Fatalf("unexpected fetched environment: %+v", fetched)
	}
	found, ok, err := client.FindBySession(context.Background(), "session-1")
	if err != nil || !ok || found.ID != testWorkspaceID {
		t.Fatalf("find: environment=%+v found=%t err=%v", found, ok, err)
	}
	_, ok, err = client.FindBySession(context.Background(), "missing")
	if err != nil || ok {
		t.Fatalf("missing find: found=%t err=%v", ok, err)
	}
	for _, operation := range []func(context.Context, sandbox.ID) error{
		client.Start, client.Stop, client.Pause, client.Resume, client.Delete,
	} {
		if err := operation(context.Background(), testWorkspaceID); err != nil {
			t.Fatalf("transition: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(transitions, ",") != "start,stop,stop,start,delete" {
		t.Fatalf("transitions = %v", transitions)
	}
}

func TestRunningBuildWaitsForHealthyAgent(t *testing.T) {
	t.Parallel()
	client := &Client{}
	view := workspace{
		ID: testWorkspaceID, Name: "ao-test", Health: workspaceHealth{Healthy: true},
		LatestBuild: workspaceBuild{Status: "running", Resources: []workspaceResource{{
			Agents: []workspaceAgent{{
				ID: testAgentID, Status: "connecting", LifecycleState: "starting",
				Health: workspaceHealth{Healthy: true},
			}},
		}}},
	}
	if environment := client.toEnvironment(view); environment.State != sandbox.StateProvisioning {
		t.Fatalf("state = %q, want provisioning", environment.State)
	}
}

func TestRunningBuildWaitsForAgentStartupScript(t *testing.T) {
	t.Parallel()
	client := &Client{}
	view := workspace{
		ID: testWorkspaceID, Name: "ao-test", Health: workspaceHealth{Healthy: true},
		LatestBuild: workspaceBuild{Status: "running", Resources: []workspaceResource{{
			Agents: []workspaceAgent{{
				ID: testAgentID, Status: "connected", LifecycleState: "starting",
				Health: workspaceHealth{Healthy: true},
			}},
		}}},
	}
	if environment := client.toEnvironment(view); environment.State != sandbox.StateProvisioning {
		t.Fatalf("state = %q, want provisioning", environment.State)
	}
	view.LatestBuild.Resources[0].Agents[0].LifecycleState = "ready"
	if environment := client.toEnvironment(view); environment.State != sandbox.StateRunning {
		t.Fatalf("ready state = %q, want running", environment.State)
	}
}

func TestTransitionalBuildIsNotReportedStopped(t *testing.T) {
	t.Parallel()
	client := &Client{}
	view := workspace{ID: testWorkspaceID, LatestBuild: workspaceBuild{Status: "stopping"}}
	if environment := client.toEnvironment(view); environment.State != sandbox.StateProvisioning {
		t.Fatalf("state = %q, want provisioning", environment.State)
	}
}

func TestBootstrapWorkerStreamsArchiveWithoutSecretsInURL(t *testing.T) {
	t.Parallel()

	const secret = "TOP_SECRET_WORKER_TOKEN"
	archiveResult := make(chan map[string]string, 1)
	preinstalledChecks := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/api/v2/workspaces/"+testWorkspaceID:
			writeWorkspace(t, writer, "running", "connected", true)
		case request.URL.Path == "/api/v2/workspaceagents/"+testAgentID+"/pty":
			if request.Header.Get("Coder-Session-Token") != "test-token" {
				t.Errorf("missing Coder token header")
			}
			if strings.Contains(request.URL.RawQuery, secret) {
				t.Errorf("worker secret leaked into PTY URL")
			}
			if got := request.URL.Query().Get("backend_type"); got != "buffered" {
				t.Errorf("PTY backend_type = %q, want buffered", got)
			}
			command := request.URL.Query().Get("command")
			if strings.Contains(command, preinstalledMiss) {
				connection, err := websocket.Accept(writer, request, nil)
				if err != nil {
					t.Errorf("accept preinstalled probe websocket: %v", err)
					return
				}
				defer connection.CloseNow()
				output := websocket.NetConn(context.Background(), connection, websocket.MessageBinary)
				defer output.Close()
				preinstalledChecks <- struct{}{}
				_, _ = io.WriteString(output, preinstalledMiss+"\r\n")
				return
			}
			for _, expected := range []string{
				"/mnt/ao/repository",
				"/mnt/ao/.ao/worker",
				"/mnt/ao/.ao/home/.claude",
				"/mnt/ao/.ao/home/.codex",
				"mountpoint -q",
				"/mnt/ao/.ao/durable-session-id",
				"/mnt/ao/.ao/worker/worker.pid",
				"kill -0",
				"chmod o+x",
				"sudo -n -b -u",
			} {
				if !strings.Contains(command, expected) {
					t.Errorf("bootstrap command missing durable path contract %q", expected)
				}
			}
			match := regexp.MustCompile(`target=([0-9]+)`).FindStringSubmatch(command)
			if len(match) != 2 {
				t.Errorf("bootstrap command did not include payload length")
				return
			}
			wanted, _ := strconv.Atoi(match[1])
			connection, err := websocket.Accept(writer, request, nil)
			if err != nil {
				t.Errorf("accept websocket: %v", err)
				return
			}
			defer connection.CloseNow()
			netConnection := websocket.NetConn(context.Background(), connection, websocket.MessageBinary)
			defer netConnection.Close()
			if _, err := io.WriteString(netConnection, bootstrapReady+"\r\n"); err != nil {
				t.Errorf("write bootstrap ready: %v", err)
				return
			}
			decoder := json.NewDecoder(netConnection)
			var encoded strings.Builder
			expectedSequence := 0
			truncatedFirstCopy := false
			for {
				var request struct {
					Data string `json:"data"`
				}
				if err := decoder.Decode(&request); err != nil {
					t.Errorf("decode PTY input: %v", err)
					return
				}
				if !truncatedFirstCopy && strings.HasPrefix(request.Data, "data:0:") {
					request.Data = strings.TrimSuffix(request.Data, "\n")
					request.Data = request.Data[:len(request.Data)-1] + "\n"
					truncatedFirstCopy = true
					_, _ = io.WriteString(netConnection, "ignored incomplete frame\r\n")
				}
				parts := strings.SplitN(strings.TrimSuffix(request.Data, "\n"), ":", 4)
				if len(parts) != 4 {
					continue
				}
				sequence, sequenceErr := strconv.Atoi(parts[1])
				declared, declaredErr := strconv.Atoi(parts[2])
				if parts[0] == "data" && sequenceErr == nil && declaredErr == nil &&
					sequence == expectedSequence && len(parts[3]) == declared {
					encoded.WriteString(parts[3])
					expectedSequence++
					_, _ = io.WriteString(netConnection, fmt.Sprintf("%s:%d\r\n", bootstrapUploadACK, expectedSequence))
				}
				if parts[0] == "done" && encoded.Len() == wanted {
					_, _ = io.WriteString(netConnection, bootstrapUploadDone+"\r\n")
					break
				}
			}
			archive, err := base64.StdEncoding.DecodeString(encoded.String())
			if err != nil {
				t.Errorf("decode archive: %v", err)
				return
			}
			archiveResult <- readArchive(t, archive)
			_, _ = io.WriteString(netConnection, bootstrapOK+"\r\n")
		default:
			http.Error(writer, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bootstrapResult := make(chan error, 1)
	go func() {
		bootstrapResult <- client.BootstrapWorker(ctx, testWorkspaceID, sandbox.WorkerBootstrap{
			Binary: []byte("worker-binary"), Destination: "/usr/local/bin/ao-worker",
			HelperBinary: []byte("helper-binary"), HelperDestination: "/usr/local/bin/ao",
			User: "ao-worker", Environment: map[string]string{"AO_WORKER_TOKEN": secret},
			DurableRoot: "/mnt/ao", DurableIdentity: "session-1",
		})
	}()
	var files map[string]string
	select {
	case files = <-archiveResult:
	case <-ctx.Done():
		t.Fatalf("receive bootstrap archive: %v", ctx.Err())
	}
	if files["ao-worker"] != "worker-binary" || files["ao"] != "helper-binary" {
		t.Fatalf("unexpected binaries in archive: %+v", files)
	}
	select {
	case <-preinstalledChecks:
	default:
		t.Fatal("bootstrap did not probe the preinstalled worker before uploading binaries")
	}
	if !strings.Contains(files["worker.env"], secret) {
		t.Fatalf("worker environment missing from archive")
	}
	if !strings.Contains(files["launch.sh"], `>"$3"`) {
		t.Fatalf("worker launcher does not publish its process ID: %q", files["launch.sh"])
	}
	if err := <-bootstrapResult; err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
}

func TestPreinstalledBootstrapUsesExactHashesAndLaunchOnlyArchive(t *testing.T) {
	t.Parallel()
	bootstrap := sandbox.WorkerBootstrap{
		Binary: []byte("worker-binary"), Destination: "/usr/local/bin/ao-worker",
		HelperBinary: []byte("helper-binary"), HelperDestination: "/usr/local/bin/ao",
		User: "ao-worker", Environment: map[string]string{"AO_WORKER_TOKEN": "secret"},
		DurableRoot: "/mnt/ao", DurableIdentity: "session-1",
	}
	archive, err := bootstrapLaunchArchive(bootstrap)
	if err != nil {
		t.Fatalf("build launch archive: %v", err)
	}
	files := readArchive(t, archive)
	if _, ok := files["ao-worker"]; ok {
		t.Fatal("launch-only archive unexpectedly contains ao-worker")
	}
	if _, ok := files["ao"]; ok {
		t.Fatal("launch-only archive unexpectedly contains ao helper")
	}
	if !strings.Contains(files["worker.env"], "secret") || files["launch.sh"] == "" {
		t.Fatalf("launch-only archive is missing launch configuration: %+v", files)
	}

	command := bootstrapCommandForArchive(bootstrap, len(base64.StdEncoding.EncodeToString(archive)), true)
	workerHash := sha256.Sum256(bootstrap.Binary)
	helperHash := sha256.Sum256(bootstrap.HelperBinary)
	for _, expected := range []string{
		preinstalledMiss,
		hex.EncodeToString(workerHash[:]),
		hex.EncodeToString(helperHash[:]),
		"/usr/local/bin/ao-worker",
		"/usr/local/bin/ao",
	} {
		if !strings.Contains(command, expected) {
			t.Errorf("preinstalled bootstrap command missing %q", expected)
		}
	}
	if strings.Contains(command, `install -m 0755 "$stage/ao-worker"`) {
		t.Fatal("preinstalled bootstrap command unexpectedly reinstalls the worker binary")
	}
}

func TestBootstrapCommandRunsThroughUploadWithPipedInput(t *testing.T) {
	t.Parallel()

	command := bootstrapCommand(sandbox.WorkerBootstrap{
		Destination: "/usr/local/bin/ao-worker", User: "ao-worker",
		DurableRoot: "/mnt/ao", DurableIdentity: "session-1",
	}, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, "sh", "-c", command)
	process.Stdin = strings.NewReader("done:0:0:\n")
	output, err := process.CombinedOutput()
	if err == nil {
		t.Fatal("empty bootstrap payload unexpectedly succeeded")
	}
	for _, marker := range []string{bootstrapReady, bootstrapUploadDone, bootstrapFailed} {
		if !strings.Contains(string(output), marker) {
			t.Fatalf("bootstrap output %q missing %s", output, marker)
		}
	}
}

func TestBootstrapWorkerPropagatesPostUploadFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/workspaces/" + testWorkspaceID:
			writeWorkspace(t, writer, "running", "connected", true)
		case "/api/v2/workspaceagents/" + testAgentID + "/pty":
			serveBootstrapPTY(t, writer, request, func(_ *websocket.Conn, output io.Writer) {
				_, _ = io.WriteString(output, bootstrapFailed+":23\r\n")
			})
		default:
			http.Error(writer, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.BootstrapWorker(ctx, testWorkspaceID, sandbox.WorkerBootstrap{
		Binary: []byte("worker-binary"), Destination: "/usr/local/bin/ao-worker",
		User: "ao-worker", DurableRoot: "/mnt/ao", DurableIdentity: "session-1",
	})
	if err == nil || !strings.Contains(err.Error(), bootstrapFailed+":23") {
		t.Fatalf("bootstrap error = %v, want post-upload failure", err)
	}
}

func TestBootstrapWorkerRetriesPTYBeforeReady(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		attempts int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/workspaces/" + testWorkspaceID:
			writeWorkspace(t, writer, "running", "connected", true)
		case "/api/v2/workspaceagents/" + testAgentID + "/pty":
			mu.Lock()
			attempts++
			attempt := attempts
			mu.Unlock()
			if attempt < 3 {
				connection, err := websocket.Accept(writer, request, nil)
				if err != nil {
					t.Errorf("accept websocket: %v", err)
					return
				}
				_ = connection.Close(websocket.StatusNormalClosure, "")
				return
			}
			serveBootstrapPTY(t, writer, request, func(_ *websocket.Conn, output io.Writer) {
				_, _ = io.WriteString(output, bootstrapOK+"\r\n")
			})
		default:
			http.Error(writer, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.BootstrapWorker(ctx, testWorkspaceID, sandbox.WorkerBootstrap{
		Binary: []byte("worker-binary"), Destination: "/usr/local/bin/ao-worker",
		User: "ao-worker", DurableRoot: "/mnt/ao", DurableIdentity: "session-1",
	})
	if err != nil {
		t.Fatalf("bootstrap after transient PTY closes: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Fatalf("PTY attempts = %d, want 3", attempts)
	}
}

func TestBootstrapThroughPTYRejectsPrematureClose(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serveBootstrapPTY(t, writer, request, func(connection *websocket.Conn, _ io.Writer) {
			_ = connection.Close(websocket.StatusNormalClosure, "")
		})
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.bootstrapThroughPTY(ctx, mustParseURL(t, server.URL+"/pty"), "payload")
	if err == nil || !strings.Contains(err.Error(), "read workspace PTY") {
		t.Fatalf("bootstrap error = %v, want premature-close error", err)
	}
}

func TestWaitForBootstrapReadyIgnoresEarlierOutput(t *testing.T) {
	t.Parallel()

	output := make(chan ptyOutput, 2)
	output <- ptyOutput{data: "shell startup noise\r\n"}
	output <- ptyOutput{data: bootstrapReady + "\r\n"}
	close(output)

	if err := waitForBootstrapReady(context.Background(), output); err != nil {
		t.Fatalf("waitForBootstrapReady error = %v", err)
	}
}

func TestWaitForBootstrapReadyRejectsPrematureClose(t *testing.T) {
	t.Parallel()

	output := make(chan ptyOutput)
	close(output)
	if err := waitForBootstrapReady(context.Background(), output); err == nil ||
		!strings.Contains(err.Error(), "closed before worker upload was ready") {
		t.Fatalf("waitForBootstrapReady error = %v, want premature-close error", err)
	}
}

func TestReadBootstrapResultTimesOut(t *testing.T) {
	t.Parallel()

	output := make(chan ptyOutput)
	started := time.Now()
	result, err := readBootstrapResult(context.Background(), output, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not report the worker bootstrap result") {
		t.Fatalf("readBootstrapResult error = %v, want bounded-result error", err)
	}
	if result != "" {
		t.Fatalf("readBootstrapResult output = %q, want empty", result)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("readBootstrapResult elapsed = %s, want bounded wait", elapsed)
	}
}

func TestBootstrapThroughPTYCancellationStopsOutput(t *testing.T) {
	t.Parallel()

	uploaded := make(chan struct{})
	releaseServer := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseServer) }) }
	defer release()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serveBootstrapPTY(t, writer, request, func(_ *websocket.Conn, _ io.Writer) {
			close(uploaded)
			<-releaseServer
		})
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- client.bootstrapThroughPTY(ctx, mustParseURL(t, server.URL+"/pty"), "payload")
	}()
	select {
	case <-uploaded:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap upload did not complete")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bootstrap error = %v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not return after cancellation")
	}
	release()
}

func TestStreamPTYOutputCancellationDoesNotBlock(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	_, done := streamPTYOutput(ctx, reader)
	written := make(chan struct{})
	go func() {
		_, _ = io.WriteString(writer, "unconsumed output\n")
		close(written)
	}()
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("PTY reader did not consume output")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PTY output goroutine blocked after cancellation")
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	tests := []Config{
		{BaseURL: "https://user@example.com", Token: "token", Owner: "owner", TemplateID: testTemplateID},
		{BaseURL: "https://example.com/path", Token: "token", Owner: "owner", TemplateID: testTemplateID},
		{BaseURL: "https://example.com", Owner: "owner", TemplateID: testTemplateID},
		{BaseURL: "https://example.com", Token: "token", Owner: "owner", TemplateID: "not-a-uuid"},
	}
	for _, config := range tests {
		if _, err := New(config); err == nil {
			t.Fatalf("New(%+v) succeeded", config)
		}
	}
}

func TestForSandboxUsesDurableSessionProfile(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, "https://coder.example.com", map[string]string{"source": "deployment"})
	provider, err := client.ForSandbox(domain.Sandbox{
		SessionID:       "session-1",
		ResourceProfile: json.RawMessage(`{"coder":{"baseUrl":"https://coder.example.com","owner":"planned-owner","templateId":"2a2e262c-b31c-4202-946d-a19ad45d1fd2","agentName":"planned-agent","parameters":{"source":"session"},"durableRoot":"/persistent/coder"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	scoped := provider.(*Client)
	if scoped == client {
		t.Fatal("ForSandbox returned the deployment singleton")
	}
	if scoped.owner != "planned-owner" || scoped.templateID != testTemplateID ||
		scoped.agentName != "planned-agent" || scoped.parameters["source"] != "session" ||
		scoped.expectedWorkspaceName != WorkspaceName("session-1") {
		t.Fatalf("unexpected scoped client: %+v", scoped)
	}
	if client.owner != "ao-integration" || client.parameters["source"] != "deployment" {
		t.Fatal("ForSandbox mutated the deployment client")
	}
}

func TestForSandboxRejectsCoderDeploymentChange(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, "https://coder.example.com", nil)
	_, err := client.ForSandbox(domain.Sandbox{
		SessionID: "session-1",
		ResourceProfile: json.RawMessage(
			`{"coder":{"baseUrl":"https://other-coder.example.com","owner":"planned-owner","templateId":"2a2e262c-b31c-4202-946d-a19ad45d1fd2","parameters":{},"durableRoot":"/persistent/coder"}}`,
		),
	})
	if err == nil || !strings.Contains(err.Error(), "does not match configured deployment") {
		t.Fatalf("ForSandbox error = %v", err)
	}
}

func TestScopedCreateUsesDurableOwnerTemplateAndParameters(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v2/users/planned-owner/workspaces" {
			http.Error(writer, "unexpected route", http.StatusNotFound)
			return
		}
		var body createWorkspaceRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode create request: %v", err)
		}
		if body.TemplateID != testTemplateID || body.Name != WorkspaceName("session-1") ||
			len(body.RichParameterValues) != 1 || body.RichParameterValues[0] != (buildParameter{Name: "source", Value: "session"}) {
			t.Errorf("create request did not use durable profile: %+v", body)
		}
		view := healthyWorkspace()
		view.OwnerName = "planned-owner"
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(view)
	}))
	defer server.Close()
	client := scopedTestClient(t, server.URL)
	if _, err := client.Create(context.Background(), sandbox.Spec{SessionID: "session-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestFindBySessionRejectsWorkspaceIdentityMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*workspace)
		want   string
	}{
		{name: "name", mutate: func(view *workspace) { view.Name = "ao-other" }, want: "workspace name mismatch"},
		{name: "owner", mutate: func(view *workspace) { view.OwnerName = "other-owner" }, want: "workspace owner mismatch"},
		{name: "template", mutate: func(view *workspace) { view.TemplateID = "a4ecb7eb-58dc-438f-8fb8-3787236dd43d" }, want: "workspace template mismatch"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				view := healthyWorkspace()
				view.OwnerName = "planned-owner"
				test.mutate(&view)
				writer.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(writer).Encode(view); err != nil {
					t.Errorf("write workspace: %v", err)
				}
			}))
			defer server.Close()
			client := scopedTestClient(t, server.URL)
			_, found, err := client.FindBySession(context.Background(), "session-1")
			if err == nil || !strings.Contains(err.Error(), test.want) || found {
				t.Fatalf("FindBySession = found %t, error %v", found, err)
			}
		})
	}
}

func TestBootstrapRejectsWorkspaceMismatchBeforePTYUpload(t *testing.T) {
	t.Parallel()
	ptyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/api/v2/workspaces/"+testWorkspaceID:
			view := healthyWorkspace()
			view.OwnerName = "planned-owner"
			view.TemplateID = "a4ecb7eb-58dc-438f-8fb8-3787236dd43d"
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(view)
		case strings.Contains(request.URL.Path, "/pty"):
			ptyCalls++
			http.Error(writer, "must not upload", http.StatusInternalServerError)
		default:
			http.Error(writer, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := scopedTestClient(t, server.URL)
	err := client.BootstrapWorker(context.Background(), testWorkspaceID, sandbox.WorkerBootstrap{
		Binary: []byte("worker"), Destination: "/usr/local/bin/ao-worker", User: "ao-worker",
		DurableRoot: "/mnt/ao", DurableIdentity: "session-1",
	})
	if err == nil || !strings.Contains(err.Error(), "workspace template mismatch") {
		t.Fatalf("BootstrapWorker error = %v", err)
	}
	if ptyCalls != 0 {
		t.Fatalf("opened %d PTYs for a mismatched workspace", ptyCalls)
	}
}

func TestValidateBootstrapRejectsUnsafeDurableContract(t *testing.T) {
	t.Parallel()
	base := sandbox.WorkerBootstrap{
		Binary: []byte("worker"), Destination: "/usr/local/bin/ao-worker",
		User: "ao-worker", DurableRoot: "/mnt/ao", DurableIdentity: "session-1",
	}
	tests := []struct {
		name   string
		mutate func(*sandbox.WorkerBootstrap)
	}{
		{name: "missing root", mutate: func(value *sandbox.WorkerBootstrap) { value.DurableRoot = "" }},
		{name: "root filesystem", mutate: func(value *sandbox.WorkerBootstrap) { value.DurableRoot = "/" }},
		{name: "unclean root", mutate: func(value *sandbox.WorkerBootstrap) { value.DurableRoot = "/mnt/../ao" }},
		{name: "missing identity", mutate: func(value *sandbox.WorkerBootstrap) { value.DurableIdentity = "" }},
		{name: "identity control", mutate: func(value *sandbox.WorkerBootstrap) { value.DurableIdentity = "bad\nidentity" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := base
			test.mutate(&value)
			if err := validateBootstrap(value); err == nil {
				t.Fatal("validateBootstrap succeeded")
			}
		})
	}
}

func TestBootstrapThroughPTYReportsFailureAfterUpload(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer connection.CloseNow()
		netConnection := websocket.NetConn(context.Background(), connection, websocket.MessageBinary)
		defer netConnection.Close()
		if _, err := io.WriteString(netConnection, bootstrapReady+"\r\n"); err != nil {
			t.Errorf("write bootstrap ready: %v", err)
			return
		}
		var frame struct {
			Data string `json:"data"`
		}
		if err := json.NewDecoder(netConnection).Decode(&frame); err != nil {
			t.Errorf("decode done frame: %v", err)
			return
		}
		if !strings.HasPrefix(frame.Data, "done:0:") {
			t.Errorf("unexpected frame: %q", frame.Data)
		}
		_, _ = io.WriteString(netConnection, bootstrapUploadDone+"\r\n")
		_, _ = io.WriteString(netConnection, bootstrapFailed+":1\r\n")
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, nil)
	ptyURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.bootstrapThroughPTY(ctx, ptyURL, "")
	if err == nil || !strings.Contains(err.Error(), "worker bootstrap failed") {
		t.Fatalf("bootstrapThroughPTY error = %v", err)
	}
}

func TestBootstrapThroughPTYPipelinesUploadWindows(t *testing.T) {
	t.Parallel()
	const (
		chunkSize    = 3_000
		uploadWindow = 8
	)
	encoded := strings.Repeat("x", chunkSize*(uploadWindow+2))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer connection.CloseNow()
		netConnection := websocket.NetConn(context.Background(), connection, websocket.MessageBinary)
		defer netConnection.Close()
		if _, err := io.WriteString(netConnection, bootstrapReady+"\r\n"); err != nil {
			t.Errorf("write bootstrap ready: %v", err)
			return
		}
		decoder := json.NewDecoder(netConnection)
		expectedSequence := 0
		nextAcknowledgement := uploadWindow
		for {
			var frame struct {
				Data string `json:"data"`
			}
			if err := decoder.Decode(&frame); err != nil {
				t.Errorf("decode PTY input: %v", err)
				return
			}
			parts := strings.SplitN(strings.TrimSuffix(frame.Data, "\n"), ":", 4)
			if len(parts) != 4 {
				continue
			}
			sequence, sequenceErr := strconv.Atoi(parts[1])
			if parts[0] == "data" && sequenceErr == nil && sequence == expectedSequence {
				expectedSequence++
				if expectedSequence == nextAcknowledgement || expectedSequence == uploadWindow+2 {
					_, _ = io.WriteString(netConnection,
						fmt.Sprintf("%s:%d\r\n", bootstrapUploadACK, expectedSequence))
					nextAcknowledgement += uploadWindow
				}
				continue
			}
			if parts[0] == "done" && sequence == expectedSequence {
				_, _ = io.WriteString(netConnection, bootstrapUploadDone+"\r\n")
				_, _ = io.WriteString(netConnection, bootstrapOK+"\r\n")
				return
			}
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.bootstrapThroughPTY(ctx, mustParseURL(t, server.URL), encoded); err != nil {
		t.Fatalf("bootstrapThroughPTY: %v", err)
	}
}

func TestBootstrapThroughPTYPipelineReplaysFromDroppedMiddleFrame(t *testing.T) {
	t.Parallel()
	const (
		chunkSize    = 3_000
		uploadWindow = 8
		droppedFrame = 3
	)
	encoded := strings.Repeat("replay-safe-", 2_750)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer connection.CloseNow()
		netConnection := websocket.NetConn(context.Background(), connection, websocket.MessageBinary)
		defer netConnection.Close()
		if _, err := io.WriteString(netConnection, bootstrapReady+"\r\n"); err != nil {
			t.Errorf("write bootstrap ready: %v", err)
			return
		}
		decoder := json.NewDecoder(netConnection)
		expectedSequence := 0
		dropped := false
		arrivals := make(map[int]int)
		var reconstructed strings.Builder
		for {
			var frame struct {
				Data string `json:"data"`
			}
			if err := decoder.Decode(&frame); err != nil {
				t.Errorf("decode PTY input: %v", err)
				return
			}
			parts := strings.SplitN(strings.TrimSuffix(frame.Data, "\n"), ":", 4)
			if len(parts) != 4 {
				continue
			}
			sequence, sequenceErr := strconv.Atoi(parts[1])
			declared, declaredErr := strconv.Atoi(parts[2])
			if parts[0] == "data" && sequenceErr == nil && declaredErr == nil {
				arrivals[sequence]++
				if sequence != expectedSequence {
					continue
				}
				if sequence == droppedFrame && !dropped {
					dropped = true
					continue
				}
				if len(parts[3]) != declared {
					t.Errorf("frame %d length = %d, want %d", sequence, len(parts[3]), declared)
					return
				}
				reconstructed.WriteString(parts[3])
				expectedSequence++
				if expectedSequence%uploadWindow == 0 || reconstructed.Len() == len(encoded) {
					_, _ = io.WriteString(netConnection,
						fmt.Sprintf("%s:%d\r\n", bootstrapUploadACK, expectedSequence))
				}
				continue
			}
			if parts[0] == "done" && sequence == expectedSequence {
				if !dropped || arrivals[0] < 2 || arrivals[droppedFrame] < 2 {
					t.Errorf("window was not replayed after loss: dropped=%t arrivals=%v", dropped, arrivals)
				}
				if reconstructed.String() != encoded {
					t.Errorf("reconstructed payload differs: got %d bytes, want %d", reconstructed.Len(), len(encoded))
				}
				_, _ = io.WriteString(netConnection, bootstrapUploadDone+"\r\n")
				_, _ = io.WriteString(netConnection, bootstrapOK+"\r\n")
				return
			}
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := client.bootstrapThroughPTY(ctx, mustParseURL(t, server.URL), encoded); err != nil {
		t.Fatalf("bootstrapThroughPTY: %v", err)
	}
}

func TestGetTreatsDeletedWorkspaceAsNotFound(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "deleted", http.StatusGone)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, nil)
	if _, err := client.Get(context.Background(), testWorkspaceID); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

func newTestClient(t *testing.T, baseURL string, parameters map[string]string) *Client {
	t.Helper()
	client, err := New(Config{
		BaseURL: baseURL, Token: "test-token", Owner: "ao-integration",
		TemplateID: testTemplateID, Parameters: parameters,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func scopedTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	base := newTestClient(t, baseURL, map[string]string{"source": "deployment"})
	provider, err := base.ForSandbox(domain.Sandbox{
		SessionID:       "session-1",
		ResourceProfile: json.RawMessage(fmt.Sprintf(`{"coder":{"baseUrl":%q,"owner":"planned-owner","templateId":"2a2e262c-b31c-4202-946d-a19ad45d1fd2","agentName":"dev","parameters":{"source":"session"},"durableRoot":"/persistent/coder"}}`, baseURL)),
	})
	if err != nil {
		t.Fatalf("scope client: %v", err)
	}
	return provider.(*Client)
}

func writeWorkspace(t *testing.T, writer http.ResponseWriter, status, agentStatus string, healthy bool) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	view := healthyWorkspace()
	view.LatestBuild.Status = status
	view.LatestBuild.Resources[0].Agents[0].Status = agentStatus
	view.LatestBuild.Resources[0].Agents[0].Health.Healthy = healthy
	view.Health.Healthy = healthy
	if err := json.NewEncoder(writer).Encode(view); err != nil {
		t.Errorf("write workspace: %v", err)
	}
}

func healthyWorkspace() workspace {
	return workspace{
		ID: testWorkspaceID, Name: WorkspaceName("session-1"), OwnerName: "ao-integration", TemplateID: testTemplateID,
		Health: workspaceHealth{Healthy: true},
		LatestBuild: workspaceBuild{Status: "running", Resources: []workspaceResource{{Agents: []workspaceAgent{{
			ID: testAgentID, Name: "dev", Status: "connected", LifecycleState: "ready",
			Health: workspaceHealth{Healthy: true},
		}}}}},
	}
}

func serveBootstrapPTY(
	t *testing.T,
	writer http.ResponseWriter,
	request *http.Request,
	afterUpload func(*websocket.Conn, io.Writer),
) {
	t.Helper()
	connection, err := websocket.Accept(writer, request, nil)
	if err != nil {
		t.Errorf("accept websocket: %v", err)
		return
	}
	defer connection.CloseNow()
	netConnection := websocket.NetConn(context.Background(), connection, websocket.MessageBinary)
	defer netConnection.Close()
	if _, err := io.WriteString(netConnection, bootstrapReady+"\r\n"); err != nil {
		t.Errorf("write bootstrap ready: %v", err)
		return
	}
	decoder := json.NewDecoder(netConnection)
	expectedSequence := 0
	for {
		var input struct {
			Data string `json:"data"`
		}
		if err := decoder.Decode(&input); err != nil {
			t.Errorf("decode PTY input: %v", err)
			return
		}
		parts := strings.SplitN(strings.TrimSuffix(input.Data, "\n"), ":", 4)
		if len(parts) != 4 {
			continue
		}
		sequence, sequenceErr := strconv.Atoi(parts[1])
		declared, declaredErr := strconv.Atoi(parts[2])
		if sequenceErr != nil || declaredErr != nil {
			continue
		}
		if parts[0] == "data" && sequence == expectedSequence && len(parts[3]) == declared {
			expectedSequence++
			if _, err := io.WriteString(netConnection,
				fmt.Sprintf("%s:%d\r\n", bootstrapUploadACK, expectedSequence)); err != nil {
				t.Errorf("write upload acknowledgement: %v", err)
				return
			}
			continue
		}
		if parts[0] == "done" && sequence == expectedSequence && declared == 0 {
			if _, err := io.WriteString(netConnection, bootstrapUploadDone+"\r\n"); err != nil {
				t.Errorf("write upload completion: %v", err)
				return
			}
			afterUpload(connection, netConnection)
			return
		}
	}
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
}

func readArchive(t *testing.T, compressed []byte) map[string]string {
	t.Helper()
	gzipReader, err := gzip.NewReader(strings.NewReader(string(compressed)))
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer gzipReader.Close()
	tape := tar.NewReader(gzipReader)
	files := map[string]string{}
	for {
		header, err := tape.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		contents, err := io.ReadAll(tape)
		if err != nil {
			t.Fatalf("read %s: %v", header.Name, err)
		}
		files[header.Name] = string(contents)
	}
	return files
}
