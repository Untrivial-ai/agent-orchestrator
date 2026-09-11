// Command ao-worker is the supervisor process that runs inside one sandbox. It
// receives no permanent credential and opens no inbound port: it reads a
// one-time bootstrap ticket from its environment, dials the control plane
// outward to exchange it for a short-lived token, and then heartbeats.
//
// It prepares the repository checkout and supervises the interactive coding
// agent PTY while a separate heartbeat loop keeps its epoch-scoped token current.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workertransport"
)

const (
	workerVersion     = "0.1.0"
	heartbeatInterval = 20 * time.Second
	// The control plane recreates a sandbox whose worker has been silent for a
	// minute, so a worker that cannot reach home for longer than that is
	// already being replaced and should exit rather than compete with its
	// successor.
	maxHeartbeatFailures = 3
	requestTimeout       = 30 * time.Second
	maxResponseBody      = 1 << 20
)

// Kept mutable so tests can exercise renewal without waiting 45 minutes.
var checkoutRenewalInterval = 45 * time.Minute

var workerCapabilities = []string{
	"worker.heartbeat",
	"worker.events",
	"agent.activity",
	"worker.turns",
	"worker.credentials",
	"repository.checkout",
	"workspace.files",
	"terminal.workspace",
	"terminal.agent",
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("worker exited", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	publicURL := strings.TrimRight(strings.TrimSpace(os.Getenv("AO_CLOUD_PUBLIC_URL")), "/")
	sessionID := strings.TrimSpace(os.Getenv("AO_CLOUD_SESSION_ID"))
	bootstrapToken := strings.TrimSpace(os.Getenv("AO_WORKER_BOOTSTRAP_TOKEN"))
	workspace := strings.TrimSpace(os.Getenv("AO_WORKSPACE_DIR"))
	if publicURL == "" {
		return errors.New("AO_CLOUD_PUBLIC_URL is required")
	}
	if sessionID == "" {
		return errors.New("AO_CLOUD_SESSION_ID is required")
	}
	if bootstrapToken == "" {
		return errors.New("AO_WORKER_BOOTSTRAP_TOKEN is required")
	}
	if workspace == "" {
		return errors.New("AO_WORKSPACE_DIR is required")
	}
	dataDir := strings.TrimSpace(os.Getenv("AO_DATA_DIR"))
	if dataDir == "" {
		dataDir = os.TempDir()
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create worker data directory: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := &client{
		baseURL:   publicURL + "/api/cloud/v1",
		http:      &http.Client{Timeout: requestTimeout},
		tokenFile: filepath.Join(dataDir, "worker-token"),
	}

	bootstrap, err := client.bootstrap(ctx, bootstrapToken)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	if bootstrap.SessionID != sessionID {
		return errors.New("bootstrap session does not match AO_CLOUD_SESSION_ID")
	}
	// The ticket is single-use and now spent; from here the only credential is
	// the rotating worker token.
	_ = os.Unsetenv("AO_WORKER_BOOTSTRAP_TOKEN")
	bootstrapToken = ""
	if err := client.setToken(bootstrap.WorkerToken); err != nil {
		return err
	}
	logger.Info("worker bootstrapped",
		"session_id", bootstrap.SessionID,
		"worker_id", bootstrap.WorkerID,
		"epoch", bootstrap.Epoch,
		"harness", bootstrap.Launch.Harness,
		"repository_url", bootstrap.Launch.RepositoryURL,
	)

	if worker.IsScratchRepositoryURL(bootstrap.Launch.RepositoryURL) {
		if err := worker.PrepareScratchWorkspace(
			ctx,
			worker.ExecGitRunner{},
			workspace,
		); err != nil {
			return fmt.Errorf("prepare scratch workspace: %w", err)
		}
		logger.Info("initialized scratch workspace")
	} else {
		checkoutGrant, err := client.checkoutGrant(ctx)
		if err != nil {
			if !anonymousCheckoutEnabled() {
				if errors.Is(err, errCheckoutForbidden) {
					// A permanent fault: the session has no repository grant, so
					// no amount of restarting this worker will make progress.
					// Surface the cause plainly; the reconciler's startup ceiling
					// stops the sandbox once repairs stay fruitless.
					logger.Error("checkout grant refused; session cannot start without a repository grant",
						"session_id", bootstrap.SessionID,
						"repository_url", bootstrap.Launch.RepositoryURL,
						"hint", "connect the repository through the GitHub App, or set AO_CLOUD_ALLOW_ANONYMOUS_GITHUB_CHECKOUT for a public repository",
					)
				}
				return fmt.Errorf("request checkout grant: %w", err)
			}
			checkoutGrant = worker.CheckoutGrantResponse{
				CloneURL: bootstrap.Launch.RepositoryURL,
			}
			logger.Info("using anonymous public GitHub checkout")
		}
		if err := worker.PrepareCheckout(
			ctx,
			worker.ExecGitRunner{},
			workspace,
			checkoutGrant,
		); err != nil {
			return fmt.Errorf("prepare repository checkout: %w", err)
		}
		if err := worker.ConfigureWorkerGit(
			ctx, worker.ExecGitRunner{}, workspace, dataDir, publicURL,
			bootstrap.SessionID, bootstrap.Launch.Branch,
		); err != nil {
			return fmt.Errorf("configure repository tooling: %w", err)
		}
	}
	for key, value := range map[string]string{
		"AO_CLOUD_PUBLIC_URL": publicURL,
		"AO_SESSION_ID":       bootstrap.SessionID,
		"AO_SESSION_BRANCH":   bootstrap.Launch.Branch,
		"AO_DATA_DIR":         dataDir,
	} {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set worker tooling environment %s: %w", key, err)
		}
	}
	if !worker.IsScratchRepositoryURL(bootstrap.Launch.RepositoryURL) {
		if err := os.Setenv("PATH", worker.ToolingBinDir(dataDir)+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
			return fmt.Errorf("activate worker tooling: %w", err)
		}
	}
	// Heartbeat before waiting out the first interval. Bootstrap registration is
	// not a check-in, so a repaired worker can otherwise be replaced again
	// before the control plane ever observes it.
	if renewed, err := client.heartbeat(ctx); err != nil {
		logger.Warn("first heartbeat failed", "error", err)
	} else if err := client.setToken(renewed); err != nil {
		return err
	}
	var agentCommand workerexec.Command
	agentTerminalID := ""
	pullRequestSocketPath := filepath.Join(dataDir, "ao-pull-request.sock")
	reviewSocketPath := filepath.Join(dataDir, "ao-review.sock")
	if err := verifyHarnessAvailable(bootstrap.Launch.Harness); err != nil {
		// Workspace files and shell terminals use the same worker transport as the
		// coding agent. Keep that transport alive when a rootfs is missing the
		// selected harness instead of making the whole sandbox unreachable.
		logger.Warn("coding-agent harness unavailable; continuing with workspace transport", "error", err)
	} else {
		credential, err := client.Credential(ctx)
		if err != nil {
			return fmt.Errorf("load coding-agent credential: %w", err)
		}
		agentCommand, err = (workerexec.HarnessBuilder{
			DataDir: dataDir,
		}).BuildInteractive(bootstrap.Launch, credential, workspace)
		if err != nil {
			return fmt.Errorf("build interactive coding-agent command: %w", err)
		}
		agentCommand.Env["AO_CLOUD_WORKER_API_URL"] = client.baseURL
		agentCommand.Env["AO_CLOUD_WORKER_TOKEN_FILE"] = client.tokenFile
		agentCommand.Env["AO_SESSION_ID"] = bootstrap.SessionID
		agentCommand.Env["AO_PROJECT_ID"] = bootstrap.Launch.ProjectID
		agentCommand.Env["AO_SESSION_KIND"] = bootstrap.Launch.Kind
		agentCommand.Env["AO_PULL_REQUEST_SOCKET"] = pullRequestSocketPath
		agentCommand.Env["AO_PULL_REQUEST_HELP"] = "curl --unix-socket $AO_PULL_REQUEST_SOCKET " +
			`-X POST http://localhost/pull-request -H 'Content-Type: application/json' ` +
			`-d '{"branch":"<pushed branch name>","title":"<PR title>","body":"<PR body>"}' ` +
			"to push the current branch and open a pull request against the repository's default branch."
		agentCommand.Env["AO_REVIEW_SOCKET"] = reviewSocketPath
		agentCommand.Env["AO_REVIEW_HELP"] = "curl --unix-socket $AO_REVIEW_SOCKET " +
			`-X POST http://localhost/review -H 'Content-Type: application/json' ` +
			`-d '{"reviewRunId":"<review run id from the prompt>","verdict":"approved|changes_requested","body":"<your findings>"}' ` +
			"to submit an AO-triggered review verdict."
		agentTerminal, err := client.ensureAgentTerminal(ctx)
		if err != nil {
			agentCommand.Cleanup()
			return fmt.Errorf("initialize agent terminal: %w", err)
		}
		agentTerminalID = agentTerminal.TerminalID
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	started := make(chan error, 1)
	transportSupervisor := workertransport.Supervisor{
		Control: client, Workspace: workspace, Logger: logger,
		AgentCommand: agentCommand, AgentTerminalID: agentTerminalID,
		Started: started,
	}
	if os.Getenv("AO_CLOUD_TERMINAL_STREAM") == "1" {
		transportSupervisor.Streams = client
	}
	results := make(chan error, 5)
	go func() { results <- client.heartbeatLoop(runCtx, logger) }()
	go func() { results <- transportSupervisor.Run(runCtx) }()
	go func() {
		results <- client.checkoutRenewalLoop(runCtx, logger, workspace, bootstrap.Launch.RepositoryURL)
	}()
	go func() {
		results <- runPullRequestBridge(runCtx, pullRequestSocketPath, client, workspace, logger)
	}()
	go func() {
		results <- runReviewBridge(runCtx, reviewSocketPath, client, logger)
	}()
	if err := <-started; err != nil {
		cancel()
		<-results
		<-results
		<-results
		<-results
		<-results
		return fmt.Errorf("start interactive coding-agent terminal: %w", err)
	}
	if err := client.publishEvent(ctx, "worker.ready", map[string]any{
		"workerId":     bootstrap.WorkerID,
		"epoch":        bootstrap.Epoch,
		"version":      workerVersion,
		"capabilities": workerCapabilities,
	}); err != nil {
		logger.Warn("publish worker.ready failed", "error", err)
	}
	first := <-results
	cancel()
	<-results
	<-results
	<-results
	<-results
	if ctx.Err() != nil {
		logger.Info("worker shutting down")
		return nil
	}
	return first
}

var errStaleWorker = errors.New("worker credential replaced")

// errCheckoutForbidden marks a checkout grant the control plane refused. It is a
// permanent configuration fault (the session has no repository grant, e.g. a
// private repository not connected through the GitHub App), not a transient
// error, so retrying the worker cannot make progress.
var errCheckoutForbidden = errors.New("checkout grant not authorized")

type client struct {
	baseURL   string
	http      *http.Client
	tokenFile string
	mu        sync.RWMutex
	token     string
}

func (c *client) bootstrap(ctx context.Context, bootstrapToken string) (worker.BootstrapResponse, error) {
	var response worker.BootstrapResponse
	err := c.do(ctx, "/worker/bootstrap", worker.BootstrapRequest{
		BootstrapToken: bootstrapToken,
		Version:        workerVersion,
		Capabilities:   workerCapabilities,
	}, &response)
	if err != nil {
		return worker.BootstrapResponse{}, err
	}
	if response.WorkerToken == "" {
		return worker.BootstrapResponse{}, errors.New("control plane returned no worker token")
	}
	return response, nil
}

func (c *client) heartbeat(ctx context.Context) (string, error) {
	var response worker.HeartbeatResponse
	err := c.do(ctx, "/worker/heartbeat", worker.HeartbeatRequest{
		Version:      workerVersion,
		Capabilities: workerCapabilities,
	}, &response)
	if err != nil {
		return "", err
	}
	if response.WorkerToken == "" {
		return "", errors.New("control plane returned no renewed worker token")
	}
	return response.WorkerToken, nil
}

func (c *client) heartbeatLoop(ctx context.Context, logger *slog.Logger) error {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			renewed, err := c.heartbeat(ctx)
			if errors.Is(err, errStaleWorker) {
				return errors.New("worker credential was replaced; a newer worker owns this session")
			}
			if err != nil {
				failures++
				logger.Warn("heartbeat failed", "error", err, "consecutive_failures", failures)
				if failures >= maxHeartbeatFailures {
					return fmt.Errorf("heartbeat failed %d times: %w", failures, err)
				}
				continue
			}
			failures = 0
			if err := c.setToken(renewed); err != nil {
				return err
			}
		}
	}
}

func (c *client) checkoutRenewalLoop(
	ctx context.Context, logger *slog.Logger, workspace, repositoryURL string,
) error {
	if worker.IsScratchRepositoryURL(repositoryURL) {
		<-ctx.Done()
		return nil
	}
	ticker := time.NewTicker(checkoutRenewalInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.renewCheckout(ctx, logger, workspace)
		}
	}
}

func (c *client) renewCheckout(ctx context.Context, logger *slog.Logger, workspace string) {
	grant, err := c.checkoutGrant(ctx)
	if err != nil {
		logger.Warn("renew checkout grant failed", "error", err)
		return
	}
	if err := worker.PrepareCheckout(ctx, worker.ExecGitRunner{}, workspace, grant); err != nil {
		logger.Warn("refresh repository checkout failed", "error", err)
	}
}

func (c *client) ClaimTurn(ctx context.Context) (*worker.Turn, error) {
	var response worker.ClaimTurnResponse
	if err := c.do(ctx, "/worker/turns/claim", worker.ClaimTurnRequest{}, &response); err != nil {
		return nil, err
	}
	return response.Turn, nil
}

func (c *client) Credential(ctx context.Context) (worker.CredentialResponse, error) {
	var response worker.CredentialResponse
	err := c.doMethod(ctx, http.MethodGet, "/worker/credential", nil, &response)
	if err != nil {
		return worker.CredentialResponse{}, err
	}
	if response.Provider == "" || response.CredentialType == "" || response.Secret == "" {
		return worker.CredentialResponse{}, errors.New("control plane returned an incomplete coding-agent credential")
	}
	return response, nil
}

func (c *client) checkoutGrant(ctx context.Context) (worker.CheckoutGrantResponse, error) {
	var response worker.CheckoutGrantResponse
	if err := c.do(ctx, "/worker/checkout-grant", struct{}{}, &response); err != nil {
		return worker.CheckoutGrantResponse{}, err
	}
	if response.CloneURL == "" ||
		(response.Token != "" && !response.ExpiresAt.After(time.Now())) {
		return worker.CheckoutGrantResponse{}, errors.New("control plane returned an invalid checkout grant")
	}
	return response, nil
}

func (c *client) pushGrant(ctx context.Context) (worker.CheckoutGrantResponse, error) {
	var response worker.CheckoutGrantResponse
	if err := c.do(ctx, "/worker/push-grant", struct{}{}, &response); err != nil {
		return worker.CheckoutGrantResponse{}, err
	}
	if response.CloneURL == "" || response.Token == "" || !response.ExpiresAt.After(time.Now()) {
		return worker.CheckoutGrantResponse{}, errors.New("control plane returned an invalid push grant")
	}
	return response, nil
}

func (c *client) raisePullRequest(
	ctx context.Context,
	input worker.RaisePullRequestRequest,
) (worker.RaisePullRequestResponse, error) {
	var response worker.RaisePullRequestResponse
	if err := c.do(ctx, "/worker/pull-requests", input, &response); err != nil {
		return worker.RaisePullRequestResponse{}, err
	}
	if response.HTMLURL == "" || response.Number <= 0 {
		return worker.RaisePullRequestResponse{}, errors.New("control plane returned an incomplete pull request response")
	}
	return response, nil
}

func (c *client) submitReview(
	ctx context.Context,
	reviewRunID string,
	input worker.SubmitReviewRequest,
) (worker.SubmitReviewResponse, error) {
	var response worker.SubmitReviewResponse
	if err := c.do(ctx, "/worker/reviews/"+url.PathEscape(reviewRunID)+"/submit", input, &response); err != nil {
		return worker.SubmitReviewResponse{}, err
	}
	if response.ID == "" {
		return worker.SubmitReviewResponse{}, errors.New("control plane returned an incomplete review response")
	}
	return response, nil
}

func anonymousCheckoutEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(
		os.Getenv("AO_CLOUD_ALLOW_ANONYMOUS_GITHUB_CHECKOUT"),
	))
	return err == nil && enabled
}

func verifyHarnessAvailable(harness string) error {
	var binary string
	switch harness {
	case "claude-code":
		binary = "claude"
	case "codex":
		binary = "codex"
	case "cursor":
		binary = "cursor-agent"
	default:
		return fmt.Errorf("unsupported coding-agent harness %q", harness)
	}
	if _, err := exec.LookPath(binary); err != nil {
		return fmt.Errorf("%s harness binary %q is unavailable: %w", harness, binary, err)
	}
	return nil
}

func (c *client) PublishOutput(ctx context.Context, output worker.OutputEvent) error {
	return c.publishEvent(ctx, "chat.assistant_delta", output)
}

func (c *client) ClaimTransport(ctx context.Context) (*worker.TransportRequest, error) {
	var response worker.ClaimTransportResponse
	if err := c.do(ctx, "/worker/transport/claim", struct{}{}, &response); err != nil {
		return nil, err
	}
	return response.Request, nil
}

func (c *client) CompleteTransport(
	ctx context.Context,
	requestID string,
	attempt int,
	result any,
) error {
	return c.do(
		ctx,
		"/worker/transport/"+url.PathEscape(requestID)+"/complete",
		worker.CompleteTransportRequest{Attempt: attempt, Response: result},
		nil,
	)
}

func (c *client) FailTransport(
	ctx context.Context,
	requestID string,
	attempt int,
	code, message string,
) error {
	return c.do(
		ctx,
		"/worker/transport/"+url.PathEscape(requestID)+"/fail",
		worker.FailTransportRequest{Attempt: attempt, Code: code, Message: message},
		nil,
	)
}

func (c *client) PublishTerminalOutput(
	ctx context.Context,
	terminalID string,
	data []byte,
) error {
	return c.do(
		ctx,
		"/worker/terminals/"+url.PathEscape(terminalID)+"/output",
		worker.TerminalOutputRequest{Data: data},
		nil,
	)
}

// DialTerminalStream opens the persistent duplex terminal stream. The token
// is presented at dial time; the socket then lives until the control plane
// retires it (worker epoch bump or terminal close).
func (c *client) DialTerminalStream(
	ctx context.Context,
	terminalID string,
) (*websocket.Conn, error) {
	streamURL := c.baseURL + "/worker/terminals/" + url.PathEscape(terminalID) + "/stream"
	if strings.HasPrefix(streamURL, "http") {
		streamURL = "ws" + strings.TrimPrefix(streamURL, "http")
	}
	header := http.Header{}
	if token := c.currentToken(); token != "" {
		header.Set("Authorization", "Worker "+token)
	}
	conn, _, err := websocket.Dial(ctx, streamURL, &websocket.DialOptions{
		HTTPClient: c.http,
		HTTPHeader: header,
	})
	return conn, err
}

func (c *client) ensureAgentTerminal(
	ctx context.Context,
) (worker.AgentTerminalResponse, error) {
	var response worker.AgentTerminalResponse
	err := c.do(ctx, "/worker/terminals/agent", struct{}{}, &response)
	if err != nil {
		return worker.AgentTerminalResponse{}, err
	}
	if response.TerminalID == "" {
		return worker.AgentTerminalResponse{}, errors.New("control plane returned no agent terminal")
	}
	return response, nil
}

func (c *client) PublishTerminalExit(
	ctx context.Context,
	terminalID string,
	exitCode int,
) error {
	return c.do(
		ctx,
		"/worker/terminals/"+url.PathEscape(terminalID)+"/exit",
		worker.TerminalExitRequest{ExitCode: exitCode},
		nil,
	)
}

func (c *client) CancellationRequested(
	ctx context.Context,
	turnID string,
	attempt int,
) (bool, error) {
	var response worker.CancellationResponse
	path := "/worker/turns/" + url.PathEscape(turnID) +
		"/cancellation?attempt=" + url.QueryEscape(fmt.Sprint(attempt))
	if err := c.doMethod(ctx, http.MethodGet, path, nil, &response); err != nil {
		return false, err
	}
	return response.Requested, nil
}

func (c *client) CompleteTurn(
	ctx context.Context,
	turnID string,
	attempt int,
	cancelled bool,
) error {
	return c.do(
		ctx,
		"/worker/turns/"+url.PathEscape(turnID)+"/complete",
		worker.FinishTurnRequest{Attempt: attempt, Cancelled: cancelled},
		nil,
	)
}

func (c *client) FailTurn(
	ctx context.Context,
	turnID string,
	attempt int,
	message string,
) error {
	return c.do(
		ctx,
		"/worker/turns/"+url.PathEscape(turnID)+"/fail",
		worker.FailTurnRequest{Attempt: attempt, Error: message},
		nil,
	)
}

func (c *client) publishEvent(ctx context.Context, eventType string, payload any) error {
	return c.do(ctx, "/worker/events", worker.EventRequest{Type: eventType, Payload: payload}, nil)
}

func (c *client) do(ctx context.Context, path string, body any, out any) error {
	return c.doMethod(ctx, http.MethodPost, path, body, out)
}

func (c *client) doMethod(
	ctx context.Context,
	method, path string,
	body any,
	out any,
) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s request: %w", path, err)
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token := c.currentToken(); token != "" {
		request.Header.Set("Authorization", "Worker "+token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if response.StatusCode == http.StatusUnauthorized &&
			bytes.Contains(snippet, []byte("STALE_WORKER_TOKEN")) {
			return errStaleWorker
		}
		if response.StatusCode == http.StatusForbidden &&
			bytes.Contains(snippet, []byte("CHECKOUT_NOT_AUTHORIZED")) {
			return errCheckoutForbidden
		}
		return fmt.Errorf("%s returned %d: %s", path, response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

func (c *client) setToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("control plane returned an empty worker token")
	}
	if c.tokenFile != "" {
		temporary := c.tokenFile + ".tmp"
		if err := os.WriteFile(temporary, []byte(token), 0o600); err != nil {
			return fmt.Errorf("write rotating worker credential: %w", err)
		}
		if err := os.Rename(temporary, c.tokenFile); err != nil {
			_ = os.Remove(temporary)
			return fmt.Errorf("replace rotating worker credential: %w", err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
	return nil
}

func (c *client) currentToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}
