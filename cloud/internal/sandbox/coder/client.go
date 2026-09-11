// Package coder implements AO's provider-neutral sandbox lifecycle against a
// customer-operated Coder deployment. It uses only Coder's authenticated HTTP
// and workspace-agent PTY APIs; AO never needs the customer's cloud account.
package coder

import (
	"archive/tar"
	"bufio"
	"bytes"
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
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

const (
	defaultTimeout      = 2 * time.Minute
	maxResponseBody     = 4 << 20
	maxErrorBody        = 64 << 10
	maxPTYOutput        = 1 << 20
	workspaceNamePrefix = "ao-"
	bootstrapReady      = "__AO_BOOTSTRAP_READY__"
	bootstrapOK         = "__AO_BOOTSTRAP_OK__"
	bootstrapFailed     = "__AO_BOOTSTRAP_FAILED__"
	bootstrapUploadACK  = "__AO_UPLOAD_ACK__"
	bootstrapUploadDone = "__AO_UPLOAD_DONE__"
	preinstalledMiss    = "__AO_PREINSTALLED_MISS__"
	bootstrapResultWait = 2 * time.Minute
)

var (
	userPattern                       = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	errPreinstalledWorkerDoesNotMatch = errors.New("coder: preinstalled AO worker does not match")
)

// Config describes one Coder deployment and the template AO is allowed to use.
type Config struct {
	BaseURL    string
	Token      string
	Owner      string
	TemplateID string
	AgentName  string
	Parameters map[string]string
	HTTPClient *http.Client
}

// Client manages AO-owned workspaces through one dedicated Coder user.
type Client struct {
	baseURL               string
	token                 string
	owner                 string
	templateID            string
	agentName             string
	parameters            map[string]string
	expectedWorkspaceName string
	http                  *http.Client
}

var (
	_ sandbox.Provider     = (*Client)(nil)
	_ sandbox.Bootstrapper = (*Client)(nil)
)

// New creates a fail-closed Coder provider client.
func New(config Config) (*Client, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || endpoint.Host == "" || endpoint.User != nil ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		(endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("coder: base URL must be an absolute http or https origin")
	}
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("coder: API token is required")
	}
	if strings.TrimSpace(config.Owner) == "" {
		return nil, errors.New("coder: workspace owner is required")
	}
	templateID, err := uuid.Parse(strings.TrimSpace(config.TemplateID))
	if err != nil {
		return nil, errors.New("coder: template ID must be a UUID")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	parameters := make(map[string]string, len(config.Parameters))
	for name, value := range config.Parameters {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("coder: template parameter name must not be empty")
		}
		parameters[name] = value
	}
	return &Client{
		baseURL:    strings.TrimRight(endpoint.String(), "/"),
		token:      strings.TrimSpace(config.Token),
		owner:      strings.TrimSpace(config.Owner),
		templateID: templateID.String(),
		agentName:  strings.TrimSpace(config.AgentName),
		parameters: parameters,
		http:       httpClient,
	}, nil
}

// ForSandbox binds the deployment-scoped connection credential to the
// non-secret Coder contract stored on one session. The returned client is safe
// to use only for that session's deterministic workspace identity.
func (c *Client) ForSandbox(record domain.Sandbox) (sandbox.Provider, error) {
	if strings.TrimSpace(record.SessionID) == "" {
		return nil, errors.New("coder: durable session ID is required")
	}
	profile, err := sandbox.DecodeCoderSessionProfile(record.ResourceProfile)
	if err != nil {
		return nil, fmt.Errorf("coder: resolve durable session profile: %w", err)
	}
	if profile.BaseURL != c.baseURL {
		return nil, fmt.Errorf(
			"coder: durable session deployment %q does not match configured deployment %q",
			profile.BaseURL, c.baseURL,
		)
	}
	templateID, err := uuid.Parse(profile.TemplateID)
	if err != nil {
		return nil, errors.New("coder: durable session template ID must be a UUID")
	}
	parameters := make(map[string]string, len(profile.Parameters))
	for name, value := range profile.Parameters {
		parameters[name] = value
	}
	sessionClient := *c
	sessionClient.owner = profile.Owner
	sessionClient.templateID = templateID.String()
	sessionClient.agentName = profile.AgentName
	sessionClient.parameters = parameters
	sessionClient.expectedWorkspaceName = WorkspaceName(record.SessionID)
	return &sessionClient, nil
}

type workspace struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	OwnerName   string          `json:"owner_name"`
	TemplateID  string          `json:"template_id"`
	LatestBuild workspaceBuild  `json:"latest_build"`
	Health      workspaceHealth `json:"health"`
}

type workspaceHealth struct {
	Healthy bool `json:"healthy"`
}

type workspaceBuild struct {
	Status    string              `json:"status"`
	Resources []workspaceResource `json:"resources"`
}

type workspaceResource struct {
	Agents []workspaceAgent `json:"agents"`
}

type workspaceAgent struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	LifecycleState string          `json:"lifecycle_state"`
	Health         workspaceHealth `json:"health"`
}

type buildParameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type createWorkspaceRequest struct {
	TemplateID          string           `json:"template_id"`
	Name                string           `json:"name"`
	RichParameterValues []buildParameter `json:"rich_parameter_values,omitempty"`
	AutomaticUpdates    string           `json:"automatic_updates"`
}

// WorkspaceName is the stable Coder workspace name for one AO session.
func WorkspaceName(sessionID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID)))
	return workspaceNamePrefix + hex.EncodeToString(sum[:])[:24]
}

// Create provisions a Coder workspace from the configured template.
func (c *Client) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Environment, error) {
	name := strings.TrimSpace(spec.Name)
	if spec.SessionID != "" {
		name = WorkspaceName(spec.SessionID)
	}
	if name == "" {
		return sandbox.Environment{}, errors.New("coder: workspace name is required")
	}
	if c.expectedWorkspaceName != "" && name != c.expectedWorkspaceName {
		return sandbox.Environment{}, fmt.Errorf(
			"coder: session workspace name mismatch: got %q, want %q",
			name, c.expectedWorkspaceName,
		)
	}
	parameterNames := make([]string, 0, len(c.parameters))
	for parameterName := range c.parameters {
		parameterNames = append(parameterNames, parameterName)
	}
	sort.Strings(parameterNames)
	parameters := make([]buildParameter, 0, len(parameterNames))
	for _, parameterName := range parameterNames {
		parameters = append(parameters, buildParameter{Name: parameterName, Value: c.parameters[parameterName]})
	}
	body := createWorkspaceRequest{
		TemplateID: c.templateID, Name: name,
		RichParameterValues: parameters, AutomaticUpdates: "never",
	}
	var view workspace
	if err := c.do(ctx, http.MethodPost, "/api/v2/users/"+url.PathEscape(c.owner)+"/workspaces", body, &view); err != nil {
		return sandbox.Environment{}, err
	}
	if err := c.validateWorkspaceIdentity(view, name); err != nil {
		return sandbox.Environment{}, err
	}
	return c.toEnvironment(view), nil
}

// Get returns the current provider view of one Coder workspace.
func (c *Client) Get(ctx context.Context, id sandbox.ID) (sandbox.Environment, error) {
	var view workspace
	if err := c.do(ctx, http.MethodGet, "/api/v2/workspaces/"+url.PathEscape(string(id)), nil, &view); err != nil {
		return sandbox.Environment{}, err
	}
	if c.expectedWorkspaceName != "" {
		if err := c.validateWorkspaceIdentity(view, c.expectedWorkspaceName); err != nil {
			return sandbox.Environment{}, err
		}
	}
	return c.toEnvironment(view), nil
}

// FindBySession recovers a workspace after a control-plane crash between
// provider creation and persistence of the returned Coder workspace ID.
func (c *Client) FindBySession(ctx context.Context, sessionID string) (sandbox.Environment, bool, error) {
	expectedName := WorkspaceName(sessionID)
	if c.expectedWorkspaceName != "" && expectedName != c.expectedWorkspaceName {
		return sandbox.Environment{}, false, fmt.Errorf(
			"coder: session workspace name mismatch: got %q, want %q",
			expectedName, c.expectedWorkspaceName,
		)
	}
	var view workspace
	requestPath := "/api/v2/users/" + url.PathEscape(c.owner) + "/workspace/" +
		url.PathEscape(expectedName)
	err := c.do(ctx, http.MethodGet, requestPath, nil, &view)
	if errors.Is(err, sandbox.ErrNotFound) {
		return sandbox.Environment{}, false, nil
	}
	if err != nil {
		return sandbox.Environment{}, false, err
	}
	if err := c.validateWorkspaceIdentity(view, expectedName); err != nil {
		return sandbox.Environment{}, false, err
	}
	if normalizeState(view.LatestBuild.Status) == sandbox.StateDeleted {
		return sandbox.Environment{}, false, nil
	}
	return c.toEnvironment(view), true, nil
}

func (c *Client) validateWorkspaceIdentity(view workspace, expectedName string) error {
	if view.Name != expectedName {
		return fmt.Errorf(
			"coder: workspace name mismatch: got %q, want %q", view.Name, expectedName,
		)
	}
	if view.OwnerName != c.owner {
		return fmt.Errorf(
			"coder: workspace owner mismatch: got %q, want %q", view.OwnerName, c.owner,
		)
	}
	if view.TemplateID != c.templateID {
		return fmt.Errorf(
			"coder: workspace template mismatch: got %q, want %q", view.TemplateID, c.templateID,
		)
	}
	return nil
}

func (c *Client) Start(ctx context.Context, id sandbox.ID) error {
	return c.transition(ctx, id, "start")
}

func (c *Client) Stop(ctx context.Context, id sandbox.ID) error {
	return c.transition(ctx, id, "stop")
}

func (c *Client) Pause(ctx context.Context, id sandbox.ID) error {
	return c.Stop(ctx, id)
}

func (c *Client) Resume(ctx context.Context, id sandbox.ID) error {
	return c.Start(ctx, id)
}

func (c *Client) Delete(ctx context.Context, id sandbox.ID) error {
	err := c.transition(ctx, id, "delete")
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil
	}
	return err
}

func (c *Client) transition(ctx context.Context, id sandbox.ID, transition string) error {
	return c.do(ctx, http.MethodPost, "/api/v2/workspaces/"+url.PathEscape(string(id))+"/builds",
		map[string]string{"transition": transition}, nil)
}

func (c *Client) toEnvironment(view workspace) sandbox.Environment {
	agent, _ := c.selectAgent(view)
	state := normalizeState(view.LatestBuild.Status)
	if state == sandbox.StateRunning &&
		(agent.ID == "" || agent.Status != "connected" || agent.LifecycleState != "ready" ||
			!agent.Health.Healthy || !view.Health.Healthy) {
		state = sandbox.StateProvisioning
	}
	return sandbox.Environment{
		ID: sandbox.ID(view.ID), Name: view.Name, State: state, Target: agent.ID,
		Resource: domain.ResourceProfile{},
	}
}

func normalizeState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		return sandbox.StateRunning
	case "stopped":
		return sandbox.StateStopped
	case "deleting":
		return sandbox.StateDeleting
	case "deleted":
		return sandbox.StateDeleted
	default:
		return sandbox.StateProvisioning
	}
}

func (c *Client) selectAgent(view workspace) (workspaceAgent, bool) {
	for _, resource := range view.LatestBuild.Resources {
		for _, agent := range resource.Agents {
			if c.agentName == "" || agent.Name == c.agentName {
				return agent, true
			}
		}
	}
	return workspaceAgent{}, false
}

// BootstrapWorker installs and starts AO through the Coder agent PTY. The
// bootstrap archive travels as terminal input, so worker credentials never
// appear in the Coder request URL, process arguments, or control-plane logs.
func (c *Client) BootstrapWorker(ctx context.Context, id sandbox.ID, bootstrap sandbox.WorkerBootstrap) error {
	if err := validateBootstrap(bootstrap); err != nil {
		return err
	}
	var view workspace
	if err := c.do(ctx, http.MethodGet, "/api/v2/workspaces/"+url.PathEscape(string(id)), nil, &view); err != nil {
		return err
	}
	expectedName := c.expectedWorkspaceName
	if expectedName == "" {
		expectedName = WorkspaceName(bootstrap.DurableIdentity)
	}
	if err := c.validateWorkspaceIdentity(view, expectedName); err != nil {
		return err
	}
	agent, ok := c.selectAgent(view)
	if !ok || agent.ID == "" || agent.Status != "connected" || !agent.Health.Healthy {
		return errors.New("coder: workspace agent is not connected and healthy")
	}
	// Fast path: an approved template can bake the exact worker and helper from
	// the control-plane image. Verify both hashes inside the workspace, then send
	// only the small launch environment through the PTY. A stale or unmodified
	// customer template explicitly falls through to the full binary upload.
	launchPayload, err := bootstrapLaunchArchive(bootstrap)
	if err != nil {
		return err
	}
	ptyURL, err := url.Parse(c.baseURL + "/api/v2/workspaceagents/" + url.PathEscape(agent.ID) + "/pty")
	if err != nil {
		return fmt.Errorf("coder: build PTY URL: %w", err)
	}
	if err := c.bootstrapWorkerThroughPTY(ctx, ptyURL, bootstrap, launchPayload, true); err == nil {
		return nil
	} else if !errors.Is(err, errPreinstalledWorkerDoesNotMatch) {
		return fmt.Errorf("coder: launch preinstalled worker: %w", err)
	}

	payload, err := bootstrapArchive(bootstrap)
	if err != nil {
		return err
	}
	if err := c.bootstrapWorkerThroughPTY(ctx, ptyURL, bootstrap, payload, false); err != nil {
		return fmt.Errorf("coder: bootstrap worker after PTY retries: %w", err)
	}
	return nil
}

func (c *Client) bootstrapWorkerThroughPTY(
	ctx context.Context,
	ptyURL *url.URL,
	bootstrap sandbox.WorkerBootstrap,
	payload []byte,
	preinstalled bool,
) error {
	encoded := base64.StdEncoding.EncodeToString(payload)
	attemptURL := *ptyURL
	query := attemptURL.Query()
	query.Set("width", "120")
	query.Set("height", "40")
	query.Set("command", bootstrapCommandForArchive(bootstrap, len(encoded), preinstalled))
	// Bootstrap is a short-lived, non-interactive command. The buffered backend
	// preserves the final result after the upload while AO keeps the PTY open.
	query.Set("backend_type", "buffered")
	attemptURL.RawQuery = query.Encode()
	const bootstrapAttempts = 5
	var lastErr error
	for attempt := 0; attempt < bootstrapAttempts; attempt++ {
		if err := c.bootstrapThroughPTY(ctx, &attemptURL, encoded); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if errors.Is(lastErr, errPreinstalledWorkerDoesNotMatch) {
			return lastErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt+1 < bootstrapAttempts {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return lastErr
}

func (c *Client) bootstrapThroughPTY(ctx context.Context, ptyURL *url.URL, encoded string) error {
	attemptURL := *ptyURL
	query := attemptURL.Query()
	query.Set("reconnect", uuid.NewString())
	attemptURL.RawQuery = query.Encode()

	headers := http.Header{"Coder-Session-Token": []string{c.token}}
	conn, response, err := websocket.Dial(ctx, attemptURL.String(), &websocket.DialOptions{
		HTTPClient: c.http, HTTPHeader: headers,
	})
	if err != nil {
		if response != nil {
			defer response.Body.Close()
			snippet, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
			return fmt.Errorf("coder: open workspace PTY returned %d: %s", response.StatusCode,
				strings.TrimSpace(string(snippet)))
		}
		return fmt.Errorf("coder: open workspace PTY: %w", err)
	}
	streamCtx, stopStream := context.WithCancel(ctx)
	netConn := websocket.NetConn(streamCtx, conn, websocket.MessageBinary)
	output, outputDone := streamPTYOutput(streamCtx, netConn)
	defer func() {
		stopStream()
		_ = conn.CloseNow()
		<-outputDone
	}()

	if err := waitForBootstrapReady(ctx, output); err != nil {
		return err
	}
	encoder := json.NewEncoder(netConn)
	// Coder's reconnecting PTY writes each decoded Data field to the OS PTY but
	// does not retry a short write. Frame the archive as canonical terminal lines
	// with sequence and length metadata. The receiver acknowledges only a
	// complete frame for the expected sequence; retrying an unacknowledged frame
	// makes Coder's unreported short writes recoverable without tripling the
	// shell work and exceeding the reconnecting-PTY lifetime.
	const (
		chunkSize    = 3_000 // below the canonical terminal's 4 KiB line ceiling
		uploadWindow = 8     // bound PTY buffering while amortizing WAN round trips
	)
	sequence := 0
	for offset := 0; offset < len(encoded); {
		frames := make([]string, 0, uploadWindow)
		for len(frames) < uploadWindow && offset < len(encoded) {
			end := min(offset+chunkSize, len(encoded))
			chunk := encoded[offset:end]
			frames = append(frames, fmt.Sprintf("data:%d:%d:%s\n", sequence, len(chunk), chunk))
			sequence++
			offset = end
		}
		wanted := fmt.Sprintf("%s:%d", bootstrapUploadACK, sequence)
		if err := sendBootstrapWindow(ctx, encoder, output, frames, wanted); err != nil {
			return err
		}
	}
	if err := sendBootstrapWindow(ctx, encoder, output,
		[]string{fmt.Sprintf("done:%d:0:\n", sequence)}, bootstrapUploadDone); err != nil {
		return err
	}
	result, err := readBootstrapResult(ctx, output, bootstrapResultWait)
	if err != nil {
		return err
	}
	if strings.Contains(result, bootstrapOK) {
		return nil
	}
	return fmt.Errorf("coder: worker bootstrap failed: %s", sanitizePTYOutput(result))
}

func waitForBootstrapReady(ctx context.Context, output <-chan ptyOutput) error {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("coder: workspace PTY did not become ready for worker upload")
		case response, ok := <-output:
			if !ok {
				return errors.New("coder: workspace PTY closed before worker upload was ready")
			}
			if strings.Contains(response.data, bootstrapReady) {
				return nil
			}
			if strings.Contains(response.data, preinstalledMiss) {
				return errPreinstalledWorkerDoesNotMatch
			}
			if strings.Contains(response.data, bootstrapFailed) {
				return fmt.Errorf("coder: worker bootstrap failed before upload: %s", sanitizePTYOutput(response.data))
			}
			if response.err != nil {
				return fmt.Errorf("coder: read workspace PTY before worker upload: %w", response.err)
			}
		}
	}
}

// sendBootstrapWindow pipelines a bounded group of canonical PTY lines, then
// waits for the receiver's cumulative acknowledgement. If Coder short-writes a
// frame, replaying the whole window is safe: the shell ignores already-accepted
// sequence numbers, accepts the first missing one, and then continues through
// the replayed suffix. This retains the upload's loss recovery without paying
// one WAN round trip per 3 KiB frame.
func sendBootstrapWindow(
	ctx context.Context,
	encoder *json.Encoder,
	output <-chan ptyOutput,
	lines []string,
	wanted string,
) error {
	const (
		maxAttempts = 12
		ackTimeout  = 2 * time.Second
	)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		for _, line := range lines {
			if err := encoder.Encode(struct {
				Data string `json:"data"`
			}{Data: line}); err != nil {
				return fmt.Errorf("coder: upload worker through PTY: %w", err)
			}
		}
		timer := time.NewTimer(ackTimeout)
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				goto retry
			case response, ok := <-output:
				if !ok {
					timer.Stop()
					if err := ctx.Err(); err != nil {
						return err
					}
					return errors.New("coder: workspace PTY closed during worker upload")
				}
				if strings.TrimSpace(response.data) == wanted {
					timer.Stop()
					return nil
				}
				if strings.Contains(response.data, bootstrapFailed) {
					timer.Stop()
					return fmt.Errorf("coder: worker bootstrap failed during upload: %s", sanitizePTYOutput(response.data))
				}
				if response.err != nil {
					timer.Stop()
					return fmt.Errorf("coder: read workspace PTY upload acknowledgement: %w", response.err)
				}
			}
		}
	retry:
	}
	return errors.New("coder: workspace PTY repeatedly dropped an upload window")
}

func validateBootstrap(bootstrap sandbox.WorkerBootstrap) error {
	if len(bootstrap.Binary) == 0 {
		return errors.New("coder: worker binary is empty")
	}
	if !safeAbsolutePath(bootstrap.Destination) {
		return fmt.Errorf("coder: worker destination %q must be a safe absolute path", bootstrap.Destination)
	}
	if len(bootstrap.HelperBinary) > 0 && !safeAbsolutePath(bootstrap.HelperDestination) {
		return fmt.Errorf("coder: helper destination %q must be a safe absolute path", bootstrap.HelperDestination)
	}
	if !userPattern.MatchString(strings.TrimSpace(bootstrap.User)) {
		return fmt.Errorf("coder: worker user %q is invalid", bootstrap.User)
	}
	if _, err := sandbox.NewCoderWorkspaceLayout(bootstrap.DurableRoot); err != nil {
		return fmt.Errorf("coder: durable workspace root: %w", err)
	}
	identity := strings.TrimSpace(bootstrap.DurableIdentity)
	if identity == "" || len(identity) > 200 || strings.IndexFunc(identity, func(character rune) bool {
		return character < ' ' || character == 0x7f
	}) >= 0 {
		return errors.New("coder: durable workspace identity is invalid")
	}
	for key := range bootstrap.Environment {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(key) {
			return fmt.Errorf("coder: environment key %q is invalid", key)
		}
	}
	return nil
}

func safeAbsolutePath(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "/") && path.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func bootstrapArchive(bootstrap sandbox.WorkerBootstrap) ([]byte, error) {
	return buildBootstrapArchive(bootstrap, true)
}

func bootstrapLaunchArchive(bootstrap sandbox.WorkerBootstrap) ([]byte, error) {
	return buildBootstrapArchive(bootstrap, false)
}

func buildBootstrapArchive(bootstrap sandbox.WorkerBootstrap, includeBinaries bool) ([]byte, error) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	files := []struct {
		name string
		mode int64
		data []byte
	}{
		{name: "worker.env", mode: 0o600, data: []byte(environmentFile(bootstrap.Environment))},
		{name: "launch.sh", mode: 0o700, data: []byte("#!/bin/sh\nset -eu\nprintf '%s\\n' \"$$\" >\"$3\"\nset -a\n. \"$1\"\nset +a\nrm -f \"$1\"\nexec \"$2\"\n")},
	}
	if includeBinaries {
		files = append(files, struct {
			name string
			mode int64
			data []byte
		}{name: "ao-worker", mode: 0o700, data: bootstrap.Binary})
	}
	if includeBinaries && len(bootstrap.HelperBinary) > 0 {
		files = append(files, struct {
			name string
			mode int64
			data []byte
		}{name: "ao", mode: 0o700, data: bootstrap.HelperBinary})
	}
	for _, file := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.data))}); err != nil {
			return nil, fmt.Errorf("coder: build worker archive: %w", err)
		}
		if _, err := tarWriter.Write(file.data); err != nil {
			return nil, fmt.Errorf("coder: build worker archive: %w", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("coder: close worker archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("coder: compress worker archive: %w", err)
	}
	return compressed.Bytes(), nil
}

func environmentFile(environment map[string]string) string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var content strings.Builder
	for _, key := range keys {
		content.WriteString(key)
		content.WriteByte('=')
		content.WriteString(shellQuote(environment[key]))
		content.WriteByte('\n')
	}
	return content.String()
}

func bootstrapCommand(bootstrap sandbox.WorkerBootstrap, encodedLength int) string {
	return bootstrapCommandForArchive(bootstrap, encodedLength, false)
}

func bootstrapCommandForArchive(
	bootstrap sandbox.WorkerBootstrap,
	encodedLength int,
	preinstalled bool,
) string {
	workerUser := strings.TrimSpace(bootstrap.User)
	workerDestination := strings.TrimSpace(bootstrap.Destination)
	layout, _ := sandbox.NewCoderWorkspaceLayout(bootstrap.DurableRoot)
	requireIdentity := "0"
	if bootstrap.RequireDurableIdentity {
		requireIdentity = "1"
	}
	binaryPreparation := "sudo -n install -m 0755 \"$stage/ao-worker\" " + shellQuote(workerDestination) + "\n"
	if len(bootstrap.HelperBinary) > 0 {
		binaryPreparation += "sudo -n install -m 0755 \"$stage/ao\" " + shellQuote(bootstrap.HelperDestination) + "\n"
	}
	preinstalledCheck := ""
	if preinstalled {
		binaryPreparation = ""
		workerHash := sha256.Sum256(bootstrap.Binary)
		guards := []string{
			"[ -x " + shellQuote(workerDestination) + " ]",
			"[ \"$(sha256sum " + shellQuote(workerDestination) + " | cut -d' ' -f1)\" = " +
				shellQuote(hex.EncodeToString(workerHash[:])) + " ]",
		}
		if len(bootstrap.HelperBinary) > 0 {
			helperHash := sha256.Sum256(bootstrap.HelperBinary)
			guards = append(guards,
				"[ -x "+shellQuote(bootstrap.HelperDestination)+" ]",
				"[ \"$(sha256sum "+shellQuote(bootstrap.HelperDestination)+" | cut -d' ' -f1)\" = "+
					shellQuote(hex.EncodeToString(helperHash[:]))+" ]",
			)
		}
		preinstalledCheck = "if ! { " + strings.Join(guards, " && ") + "; }; then echo " + preinstalledMiss + "; exit 0; fi\n"
	}
	workerEnvironment := path.Join(layout.WorkerData, "worker.env")
	workerLauncher := path.Join(layout.WorkerData, "launch.sh")
	workerLog := path.Join(layout.WorkerData, "worker.log")
	workerPID := path.Join(layout.WorkerData, "worker.pid")
	script := "set -eu\n" + preinstalledCheck +
		"stage=$(mktemp -d)\nencoded=\"$stage/payload.b64\"\n" +
		"trap 'code=$?; stty echo icanon 2>/dev/null || true; echo " + bootstrapFailed + ":$code' EXIT\n" +
		"target=" + strconv.Itoa(encodedLength) + "\nexpected=0\nreceived=0\n: >\"$encoded\"\nstty -echo icanon 2>/dev/null || true\necho " + bootstrapReady + "\n" +
		"while IFS=: read -r kind sequence declared chunk; do\n" +
		"  case \"$sequence\" in ''|*[!0-9]*) continue ;; esac\n" +
		"  case \"$declared\" in ''|*[!0-9]*) continue ;; esac\n" +
		"  if [ \"$kind\" = data ] && [ \"$sequence\" -eq \"$expected\" ] && [ \"${#chunk}\" -eq \"$declared\" ] && [ $((received + declared)) -le \"$target\" ]; then\n" +
		"    printf %s \"$chunk\" >>\"$encoded\"\n    received=$((received + declared))\n    expected=$((expected + 1))\n    echo " + bootstrapUploadACK + ":$expected\n" +
		"  elif [ \"$kind\" = done ] && [ \"$received\" -eq \"$target\" ]; then\n    echo " + bootstrapUploadDone + "\n    break\n  fi\ndone\nstty echo icanon 2>/dev/null || true\n" +
		"base64 -d \"$encoded\" | gzip -d | tar -xf - -C \"$stage\"\n" +
		"sudo -n id -u " + shellQuote(workerUser) + " >/dev/null 2>&1 || sudo -n useradd -m " + shellQuote(workerUser) + "\n" +
		"durable_root=" + shellQuote(layout.DurableRoot) + "\n" +
		"if [ ! -d \"$durable_root\" ] || [ -L \"$durable_root\" ] || ! mountpoint -q \"$durable_root\"; then\n" +
		"  echo 'configured Coder durable root is not a mounted directory' >&2\n  exit 1\nfi\n" +
		"sudo -n chmod o+x \"$durable_root\"\n" +
		"sudo -n mkdir -p " + shellQuote(layout.Repository) + " " + shellQuote(layout.WorkerData) + " " +
		shellQuote(layout.Home) + " " + shellQuote(layout.ClaudeConfig) + " " + shellQuote(layout.CodexHome) + "\n" +
		"identity_file=" + shellQuote(layout.DurableIdentity) + "\n" +
		"if sudo -n test -f \"$identity_file\"; then\n" +
		"  existing_identity=$(sudo -n cat \"$identity_file\")\n" +
		"  if [ \"$existing_identity\" != " + shellQuote(strings.TrimSpace(bootstrap.DurableIdentity)) + " ]; then\n" +
		"    echo 'Coder durable root belongs to a different AO session' >&2\n    exit 1\n  fi\n" +
		"elif [ " + requireIdentity + " -eq 1 ]; then\n" +
		"  echo 'Coder durable state did not survive workspace stop/start' >&2\n  exit 1\n" +
		"else\n" +
		"  printf '%s\\n' " + shellQuote(strings.TrimSpace(bootstrap.DurableIdentity)) + " | sudo -n tee \"$identity_file\" >/dev/null\nfi\n" +
		"sudo -n chown -R " + shellQuote(workerUser+":"+workerUser) + " " + shellQuote(layout.Repository) + " " + shellQuote(path.Dir(layout.WorkerData)) + "\n" +
		binaryPreparation +
		"sudo -n install -o " + shellQuote(workerUser) + " -g " + shellQuote(workerUser) + " -m 0600 \"$stage/worker.env\" " + shellQuote(workerEnvironment) + "\n" +
		"sudo -n install -o " + shellQuote(workerUser) + " -g " + shellQuote(workerUser) + " -m 0700 \"$stage/launch.sh\" " + shellQuote(workerLauncher) + "\n" +
		"sudo -n pkill -u " + shellQuote(workerUser) + " -f " + shellQuote(workerDestination) + " 2>/dev/null || true\n" +
		"sudo -n install -o " + shellQuote(workerUser) + " -g " + shellQuote(workerUser) + " -m 0600 /dev/null " + shellQuote(workerLog) + "\n" +
		"sudo -n install -o " + shellQuote(workerUser) + " -g " + shellQuote(workerUser) + " -m 0600 /dev/null " + shellQuote(workerPID) + "\n" +
		"sudo -n -b -u " + shellQuote(workerUser) + " sh -c " + shellQuote("exec nohup "+shellQuote(workerLauncher)+" "+shellQuote(workerEnvironment)+" "+shellQuote(workerDestination)+" "+shellQuote(workerPID)+" >"+shellQuote(workerLog)+" 2>&1 </dev/null") + "\n" +
		"attempt=0\nworker_pid=\nwhile [ \"$attempt\" -lt 5 ]; do\n" +
		"  if sudo -n test -s " + shellQuote(workerPID) + "; then worker_pid=$(sudo -n cat " + shellQuote(workerPID) + "); fi\n" +
		"  case \"$worker_pid\" in ''|*[!0-9]*) ;; *) if sudo -n -u " + shellQuote(workerUser) + " kill -0 \"$worker_pid\" 2>/dev/null; then break; fi ;; esac\n" +
		"  worker_pid=\nattempt=$((attempt + 1))\nsleep 1\ndone\n" +
		"case \"$worker_pid\" in ''|*[!0-9]*) echo 'AO worker did not start' >&2; exit 1 ;; esac\n" +
		"sleep 1\nsudo -n -u " + shellQuote(workerUser) + " kill -0 \"$worker_pid\" 2>/dev/null || { echo 'AO worker exited during startup' >&2; exit 1; }\n" +
		"rm -rf \"$stage\"\ntrap - EXIT\necho " + bootstrapOK + "\n"
	return "sh -lc " + shellQuote(script)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type ptyOutput struct {
	data string
	err  error
}

func streamPTYOutput(ctx context.Context, reader io.Reader) (<-chan ptyOutput, <-chan struct{}) {
	result := make(chan ptyOutput)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(result)
		buffered := bufio.NewReader(reader)
		total := 0
		for total < maxPTYOutput {
			line, err := buffered.ReadString('\n')
			total += len(line)
			if line != "" || err != nil {
				select {
				case result <- ptyOutput{data: line, err: err}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
		select {
		case result <- ptyOutput{err: errors.New("coder: PTY output exceeded limit")}:
		case <-ctx.Done():
		}
	}()
	return result, done
}

func readBootstrapResult(ctx context.Context, output <-chan ptyOutput, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var result strings.Builder
	for {
		select {
		case <-ctx.Done():
			return result.String(), ctx.Err()
		case <-timer.C:
			return result.String(), errors.New("coder: workspace PTY did not report the worker bootstrap result")
		case value, ok := <-output:
			if !ok {
				return result.String(), io.EOF
			}
			result.WriteString(value.data)
			text := result.String()
			if strings.Contains(text, bootstrapOK) || strings.Contains(text, bootstrapFailed) {
				return text, nil
			}
			if value.err != nil {
				return text, fmt.Errorf("coder: read workspace PTY: %w", value.err)
			}
		}
	}
}

func sanitizePTYOutput(output string) string {
	output = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' || character >= ' ' {
			return character
		}
		return -1
	}, output)
	if len(output) > 1024 {
		output = output[len(output)-1024:]
	}
	return strings.TrimSpace(output)
}

func (c *Client) do(ctx context.Context, method, requestPath string, body, output any) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("coder: encode request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+requestPath, requestBody)
	if err != nil {
		return fmt.Errorf("coder: create request: %w", err)
	}
	request.Header.Set("Coder-Session-Token", c.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("coder: %s %s: %w", method, requestPath, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		return sandbox.ErrNotFound
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
		return fmt.Errorf("coder: API returned %d: %s", response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(output); err != nil {
		return fmt.Errorf("coder: decode API response: %w", err)
	}
	return nil
}
