// Package docker implements local AO sandbox lifecycle against the Docker
// Engine HTTP API. It deliberately uses the standard library rather than the
// full Docker SDK: the provider needs only containers and named volumes, and a
// smaller client keeps the control plane's privileged socket surface narrow.
package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

const (
	defaultHost     = "unix:///var/run/docker.sock"
	defaultTimeout  = 30 * time.Second
	maxResponseBody = 4 << 20
	maxErrorBody    = 64 << 10

	labelManaged   = "ao.managed"
	labelProvider  = "ao.provider"
	labelSessionID = "ao.session_id"
	labelOrgID     = "ao.org_id"
	labelNamespace = "ao.docker.namespace"
	labelWorkspace = "ao.docker.workspace"
)

var (
	environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	namespacePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
)

// Config configures one local Docker Engine provider.
type Config struct {
	Host        string
	WorkerImage string
	Network     string
	Namespace   string

	// HTTPClient is only for deterministic tests against an HTTP server. A
	// production client accepts unix:// only so an accidentally exposed,
	// unauthenticated TCP daemon can never become a control-plane target.
	HTTPClient *http.Client
	APIVersion string
}

// Client manages worker containers in one local Docker namespace.
type Client struct {
	baseURL   string
	image     string
	network   string
	namespace string
	http      *http.Client
}

var (
	_ sandbox.Provider  = (*Client)(nil)
	_ sandbox.Recreator = (*Client)(nil)
)

// New creates a fail-closed Docker Engine client.
func New(config Config) (*Client, error) {
	image := strings.TrimSpace(config.WorkerImage)
	if image == "" {
		return nil, errors.New("docker: worker image is required")
	}
	if containsUnsafeText(image) {
		return nil, errors.New("docker: worker image contains whitespace or control characters")
	}
	namespace := strings.TrimSpace(config.Namespace)
	if !namespacePattern.MatchString(namespace) {
		return nil, fmt.Errorf("docker: namespace %q is invalid", namespace)
	}
	network := strings.TrimSpace(config.Network)
	if strings.ContainsRune(network, '\x00') {
		return nil, errors.New("docker: network contains a NUL byte")
	}

	host := strings.TrimSpace(config.Host)
	if host == "" {
		host = defaultHost
	}
	var (
		baseURL    string
		httpClient *http.Client
	)
	if config.HTTPClient != nil {
		endpoint, err := url.Parse(host)
		if err != nil || endpoint.Host == "" ||
			(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return nil, errors.New("docker: test HTTP endpoint must be an absolute http or https URL")
		}
		baseURL = strings.TrimRight(host, "/")
		httpClient = config.HTTPClient
	} else {
		if !strings.HasPrefix(host, "unix://") {
			return nil, errors.New("docker: only unix:// engine endpoints are supported")
		}
		socketPath := strings.TrimPrefix(host, "unix://")
		if !filepath.IsAbs(socketPath) || strings.ContainsRune(socketPath, '\x00') {
			return nil, errors.New("docker: engine socket must be an absolute path")
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		}
		baseURL = "http://docker"
		httpClient = &http.Client{Transport: transport, Timeout: defaultTimeout}
	}
	client := &Client{
		image:     image,
		network:   network,
		namespace: namespace,
		http:      httpClient,
	}
	version := strings.TrimSpace(config.APIVersion)
	if version == "" {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		var err error
		version, err = negotiateAPIVersion(ctx, baseURL, httpClient)
		if err != nil {
			return nil, err
		}
	} else if config.HTTPClient == nil {
		return nil, errors.New("docker: an explicit API version is only supported by tests")
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+$`).MatchString(version) {
		return nil, fmt.Errorf("docker: engine returned invalid API version %q", version)
	}
	client.baseURL = baseURL + "/v" + version
	return client, nil
}

func negotiateAPIVersion(ctx context.Context, baseURL string, httpClient *http.Client) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/version", nil)
	if err != nil {
		return "", err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("docker: negotiate engine API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
		return "", fmt.Errorf(
			"docker: negotiate engine API returned %d: %s",
			response.StatusCode,
			truncate(strings.TrimSpace(string(snippet)), 512),
		)
	}
	var version struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(&version); err != nil {
		return "", fmt.Errorf("docker: decode engine version: %w", err)
	}
	if strings.TrimSpace(version.APIVersion) == "" {
		return "", errors.New("docker: engine version response carried no API version")
	}
	return strings.TrimSpace(version.APIVersion), nil
}

type mount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly,omitempty"`
}

type restartPolicy struct {
	Name string `json:"Name"`
}

type hostConfig struct {
	Mounts        []mount       `json:"Mounts"`
	NetworkMode   string        `json:"NetworkMode,omitempty"`
	RestartPolicy restartPolicy `json:"RestartPolicy"`
	SecurityOpt   []string      `json:"SecurityOpt"`
}

type createContainerRequest struct {
	Image      string            `json:"Image"`
	Env        []string          `json:"Env"`
	Labels     map[string]string `json:"Labels"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type createContainerResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

type volumeView struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

type createVolumeRequest struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

type containerState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Paused     bool   `json:"Paused"`
	Restarting bool   `json:"Restarting"`
	Dead       bool   `json:"Dead"`
}

type inspectHostConfig struct {
	NetworkMode string  `json:"NetworkMode"`
	Memory      int64   `json:"Memory"`
	NanoCPUs    int64   `json:"NanoCpus"`
	Mounts      []mount `json:"Mounts"`
}

type inspectConfig struct {
	Labels map[string]string `json:"Labels"`
}

type containerView struct {
	ID         string            `json:"Id"`
	Name       string            `json:"Name"`
	State      containerState    `json:"State"`
	Config     inspectConfig     `json:"Config"`
	HostConfig inspectHostConfig `json:"HostConfig"`
}

type listedContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
}

// Create creates and starts one worker container. Its workspace is a named,
// labeled volume whose deterministic name survives container recreation.
func (c *Client) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Environment, error) {
	labels, workspace, err := c.labelsFor(spec)
	if err != nil {
		return sandbox.Environment{}, err
	}
	if err := c.ensureWorkspace(ctx, workspace, labels); err != nil {
		return sandbox.Environment{}, err
	}
	request := createContainerRequest{
		Image:  c.image,
		Env:    sortedEnvironment(spec.Environment),
		Labels: labels,
		HostConfig: hostConfig{
			Mounts: []mount{{
				Type:   "volume",
				Source: workspace,
				Target: "/workspace",
			}},
			NetworkMode: c.network,
			// Bootstrap tickets are single-use. Docker must not restart the
			// same container with a spent ticket; the reconciler recreates it
			// with a fresh epoch-scoped ticket instead.
			RestartPolicy: restartPolicy{Name: "no"},
			SecurityOpt:   []string{"no-new-privileges:true"},
		},
	}
	var created createContainerResponse
	path := "/containers/create?name=" + url.QueryEscape(containerName(c.namespace, spec.SessionID))
	if err := c.do(ctx, http.MethodPost, path, request, &created); err != nil {
		return sandbox.Environment{}, err
	}
	if strings.TrimSpace(created.ID) == "" {
		return sandbox.Environment{}, errors.New("docker: create response carried no container id")
	}
	if err := c.start(ctx, sandbox.ID(created.ID)); err != nil {
		return sandbox.Environment{}, fmt.Errorf("docker: start created container %s: %w", created.ID, err)
	}
	return c.Get(ctx, sandbox.ID(created.ID))
}

// Get inspects a worker container and refuses handles outside this provider's
// namespace even if a compromised database points at one.
func (c *Client) Get(ctx context.Context, id sandbox.ID) (sandbox.Environment, error) {
	view, err := c.inspect(ctx, id)
	if err != nil {
		return sandbox.Environment{}, err
	}
	if err := c.validateOwnership(view.Config.Labels, "container"); err != nil {
		return sandbox.Environment{}, err
	}
	return toEnvironment(view), nil
}

// FindBySession adopts a worker container left behind when the control plane
// crashed between Docker create and the durable observation write.
func (c *Client) FindBySession(
	ctx context.Context,
	sessionID string,
) (sandbox.Environment, bool, error) {
	if err := validateIdentity("session id", sessionID); err != nil {
		return sandbox.Environment{}, false, err
	}
	filters, err := json.Marshal(map[string][]string{
		"label": {
			labelManaged + "=true",
			labelProvider + "=" + sandbox.ProviderDocker,
			labelNamespace + "=" + c.namespace,
			labelSessionID + "=" + sessionID,
		},
	})
	if err != nil {
		return sandbox.Environment{}, false, err
	}
	path := "/containers/json?all=1&filters=" + url.QueryEscape(string(filters))
	var matches []listedContainer
	if err := c.do(ctx, http.MethodGet, path, nil, &matches); err != nil {
		return sandbox.Environment{}, false, err
	}
	switch len(matches) {
	case 0:
		return sandbox.Environment{}, false, nil
	case 1:
		environment, err := c.Get(ctx, sandbox.ID(matches[0].ID))
		return environment, err == nil, err
	default:
		return sandbox.Environment{}, false, fmt.Errorf(
			"docker: found %d managed containers for session %s",
			len(matches),
			sessionID,
		)
	}
}

// Start starts a stopped container. Docker workers are never auto-paused.
func (c *Client) Start(ctx context.Context, id sandbox.ID) error {
	if _, err := c.ownedContainer(ctx, id); err != nil {
		return err
	}
	return c.start(ctx, id)
}

// Stop stops a worker without deleting its persistent workspace.
func (c *Client) Stop(ctx context.Context, id sandbox.ID) error {
	if _, err := c.ownedContainer(ctx, id); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/containers/"+pathID(id)+"/stop?t=10", nil, nil)
}

// Pause is intentionally an alias for Stop. Local Docker workers have no idle
// auto-pause policy, and stopping keeps the state model deterministic.
func (c *Client) Pause(ctx context.Context, id sandbox.ID) error {
	return c.Stop(ctx, id)
}

// Resume starts the stopped worker container.
func (c *Client) Resume(ctx context.Context, id sandbox.ID) error {
	return c.Start(ctx, id)
}

// Delete permanently removes a worker and its per-session workspace.
func (c *Client) Delete(ctx context.Context, id sandbox.ID) error {
	view, err := c.ownedContainer(ctx, id)
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	workspace := view.Config.Labels[labelWorkspace]
	if workspace == "" {
		return errors.New("docker: managed container has no workspace label")
	}
	if err := c.removeContainer(ctx, id); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return err
	}
	err = c.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(workspace), nil, nil)
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil
	}
	return err
}

// Recreate replaces only compute. The deterministic workspace volume remains
// intact and the replacement starts with the fresh bootstrap token in spec.
func (c *Client) Recreate(
	ctx context.Context,
	id sandbox.ID,
	spec sandbox.Spec,
) (sandbox.Environment, error) {
	view, err := c.ownedContainer(ctx, id)
	if err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return sandbox.Environment{}, err
	}
	if err == nil && view.Config.Labels[labelSessionID] != spec.SessionID {
		return sandbox.Environment{}, errors.New("docker: refusing to recreate a container for another session")
	}
	if err == nil {
		if err := c.removeContainer(ctx, id); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
			return sandbox.Environment{}, err
		}
	}
	return c.Create(ctx, spec)
}

func (c *Client) start(ctx context.Context, id sandbox.ID) error {
	return c.do(ctx, http.MethodPost, "/containers/"+pathID(id)+"/start", nil, nil)
}

func (c *Client) removeContainer(ctx context.Context, id sandbox.ID) error {
	return c.do(ctx, http.MethodDelete, "/containers/"+pathID(id)+"?force=1&v=0", nil, nil)
}

func (c *Client) inspect(ctx context.Context, id sandbox.ID) (containerView, error) {
	if strings.TrimSpace(string(id)) == "" {
		return containerView{}, errors.New("docker: empty container id")
	}
	var view containerView
	if err := c.do(ctx, http.MethodGet, "/containers/"+pathID(id)+"/json", nil, &view); err != nil {
		return containerView{}, err
	}
	if view.ID == "" {
		return containerView{}, errors.New("docker: inspect response carried no container id")
	}
	return view, nil
}

func (c *Client) ownedContainer(ctx context.Context, id sandbox.ID) (containerView, error) {
	view, err := c.inspect(ctx, id)
	if err != nil {
		return containerView{}, err
	}
	if err := c.validateOwnership(view.Config.Labels, "container"); err != nil {
		return containerView{}, err
	}
	return view, nil
}

func (c *Client) ensureWorkspace(
	ctx context.Context,
	name string,
	labels map[string]string,
) error {
	var existing volumeView
	err := c.do(ctx, http.MethodGet, "/volumes/"+url.PathEscape(name), nil, &existing)
	if err == nil {
		return c.validateWorkspace(existing, labels)
	}
	if !errors.Is(err, sandbox.ErrNotFound) {
		return err
	}
	var created volumeView
	if err := c.do(ctx, http.MethodPost, "/volumes/create", createVolumeRequest{
		Name: name, Labels: labels,
	}, &created); err != nil {
		return err
	}
	return c.validateWorkspace(created, labels)
}

func (c *Client) validateWorkspace(volume volumeView, labels map[string]string) error {
	if volume.Name != labels[labelWorkspace] {
		return fmt.Errorf("docker: workspace response named %q, want %q", volume.Name, labels[labelWorkspace])
	}
	for _, key := range []string{
		labelManaged, labelProvider, labelSessionID, labelOrgID, labelNamespace, labelWorkspace,
	} {
		if volume.Labels[key] != labels[key] {
			return fmt.Errorf("docker: refusing workspace %s with mismatched label %s", volume.Name, key)
		}
	}
	return nil
}

func (c *Client) labelsFor(spec sandbox.Spec) (map[string]string, string, error) {
	if err := validateIdentity("session id", spec.SessionID); err != nil {
		return nil, "", err
	}
	if err := validateIdentity("organization id", spec.OrgID); err != nil {
		return nil, "", err
	}
	labels := make(map[string]string, len(spec.Labels)+6)
	for key, value := range spec.Labels {
		if strings.ContainsRune(key, '\x00') || strings.ContainsRune(value, '\x00') {
			return nil, "", errors.New("docker: label contains a NUL byte")
		}
		labels[key] = value
	}
	required := map[string]string{
		labelManaged:   "true",
		labelProvider:  sandbox.ProviderDocker,
		labelSessionID: spec.SessionID,
		labelOrgID:     spec.OrgID,
		labelNamespace: c.namespace,
	}
	for key, value := range required {
		if configured, ok := labels[key]; ok && configured != value {
			return nil, "", fmt.Errorf("docker: label %s conflicts with the managed value", key)
		}
		labels[key] = value
	}
	workspace := workspaceName(c.namespace, spec.SessionID)
	labels[labelWorkspace] = workspace
	for key, value := range spec.Environment {
		if !environmentKeyPattern.MatchString(key) || strings.ContainsRune(value, '\x00') {
			return nil, "", fmt.Errorf("docker: invalid environment variable %q", key)
		}
	}
	return labels, workspace, nil
}

func (c *Client) validateOwnership(labels map[string]string, resource string) error {
	if labels[labelManaged] != "true" ||
		labels[labelProvider] != sandbox.ProviderDocker ||
		labels[labelNamespace] != c.namespace ||
		labels[labelSessionID] == "" {
		return fmt.Errorf("docker: refusing unmanaged %s", resource)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("docker: encode %s request: %w", path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("docker: %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return sandbox.ErrNotFound
	}
	if (response.StatusCode < 200 || response.StatusCode > 299) &&
		response.StatusCode != http.StatusNotModified {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
		var daemonError struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(snippet, &daemonError)
		message := strings.TrimSpace(daemonError.Message)
		if message == "" {
			message = strings.TrimSpace(string(snippet))
		}
		return &HTTPError{StatusCode: response.StatusCode, Message: truncate(message, maxErrorBody)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(out); err != nil {
		return fmt.Errorf("docker: decode %s response: %w", path, err)
	}
	return nil
}

// HTTPError is a non-success response from the Docker daemon.
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("docker engine returned %d", e.StatusCode)
	}
	return fmt.Sprintf("docker engine returned %d: %s", e.StatusCode, e.Message)
}

func toEnvironment(view containerView) sandbox.Environment {
	cpu := int(view.HostConfig.NanoCPUs / 1_000_000_000)
	memory := int(view.HostConfig.Memory / (1 << 30))
	return sandbox.Environment{
		ID:     sandbox.ID(view.ID),
		Name:   strings.TrimPrefix(view.Name, "/"),
		State:  normalizeState(view.State),
		Target: view.HostConfig.NetworkMode,
		Resource: domain.ResourceProfile{
			CPU:    cpu,
			Memory: memory,
		},
	}
}

func normalizeState(state containerState) string {
	switch {
	case state.Paused:
		return sandbox.StatePaused
	case state.Running:
		return sandbox.StateRunning
	case state.Restarting:
		return sandbox.StateProvisioning
	case strings.EqualFold(state.Status, "removing"):
		return sandbox.StateDeleting
	case state.Dead,
		strings.EqualFold(state.Status, "created"),
		strings.EqualFold(state.Status, "exited"):
		return sandbox.StateStopped
	default:
		return sandbox.StateProvisioning
	}
}

func sortedEnvironment(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+environment[key])
	}
	return values
}

func containerName(namespace, sessionID string) string {
	return "ao-worker-" + shortHash(namespace+"\x00"+sessionID)
}

func workspaceName(namespace, sessionID string) string {
	return "ao-workspace-" + shortHash(namespace+"\x00"+sessionID)
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func pathID(id sandbox.ID) string {
	return url.PathEscape(string(id))
}

func validateIdentity(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("docker: %s is invalid", name)
	}
	return nil
}

func containsUnsafeText(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return r <= ' ' || r == '\x7f'
	}) >= 0
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
