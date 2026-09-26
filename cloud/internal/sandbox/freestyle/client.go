// Package freestyle implements AO's cloud sandbox contract with Freestyle VMs.
// The worker dials AO's public control plane; the VM needs no inbound port.
package freestyle

import (
	"bytes"
	"context"
	"crypto/sha256"
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
)

const (
	defaultBaseURL         = "https://api.freestyle.sh"
	maxResponseBody        = 4 << 20
	chunkSize              = 16 << 20
	backgroundPollInterval = 2 * time.Second
)

var environmentKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var linuxUserName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type Config struct {
	BaseURL    string
	APIKey     string
	SnapshotID string
	HTTPClient *http.Client
}

type Client struct {
	baseURL    string
	apiKey     string
	snapshotID string
	http       *http.Client
}

var (
	_ sandbox.Provider     = (*Client)(nil)
	_ sandbox.Bootstrapper = (*Client)(nil)
)

func New(config Config) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil ||
		(u.Scheme != "https" && u.Scheme != "http") ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("freestyle: base URL must be an absolute HTTP origin")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("freestyle: API key is required")
	}
	if strings.TrimSpace(config.SnapshotID) == "" {
		return nil, errors.New("freestyle: snapshot is required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 6 * time.Minute}
	}
	return &Client{baseURL: baseURL, apiKey: config.APIKey, snapshotID: config.SnapshotID, http: client}, nil
}

type vmView struct {
	ID        string            `json:"id"`
	Slug      string            `json:"slug"`
	State     string            `json:"state"`
	Metadata  map[string]string `json:"metadata"`
	Resources struct {
		CPU     int `json:"cpu"`
		Memory  int `json:"memory"`
		Storage int `json:"storage"`
	} `json:"resources"`
}

func slug(sessionID string) string { return "ao-" + strings.ToLower(sessionID) }

func toEnvironment(vm vmView) sandbox.Environment {
	state := sandbox.StateProvisioning
	switch vm.State {
	case "running":
		state = sandbox.StateRunning
	case "paused":
		state = sandbox.StatePaused
	case "stopped":
		state = sandbox.StateStopped
	}
	return sandbox.Environment{
		ID: sandbox.ID(vm.ID), Name: vm.Slug, State: state,
		Resource: domain.ResourceProfile{
			CPU: vm.Resources.CPU, Memory: vm.Resources.Memory / 1024,
			Disk: vm.Resources.Storage / 1024,
		},
	}
}

func (c *Client) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Environment, error) {
	if spec.SessionID == "" || spec.OrgID == "" {
		return sandbox.Environment{}, errors.New("freestyle: session and organization IDs are required")
	}
	if existing, found, err := c.FindBySession(ctx, spec.SessionID); err != nil {
		return sandbox.Environment{}, err
	} else if found {
		if err := c.verifyOrg(ctx, existing.ID, spec.OrgID); err != nil {
			return sandbox.Environment{}, err
		}
		return existing, nil
	}
	// A slug is unique to the Freestyle account; never take it from another VM.
	// Metadata makes adoption fail closed if a slug is somehow reassigned.
	snapshotID := c.snapshotID
	if strings.TrimSpace(spec.RootFS) != "" {
		snapshotID = strings.TrimSpace(spec.RootFS)
	}
	body := map[string]any{
		"slug": slug(spec.SessionID), "displayName": spec.Name,
		"snapshotId":         snapshotID,
		"idleTimeoutSeconds": -1,
		"autoDeleteSeconds":  -1,
		"metadata": map[string]string{
			"ao-session-id": spec.SessionID, "ao-org-id": spec.OrgID,
		},
		"firewall": map[string]any{"rules": []any{
			map[string]any{"action": "allow", "source": map[string]any{},
				"destination": map[string]any{"public": true}},
		}},
	}
	var vm vmView
	if err := c.request(ctx, http.MethodPost, "/v5/vms", body, nil, &vm); err != nil {
		var status *statusError
		if errors.As(err, &status) && status.code == http.StatusConflict {
			if existing, found, lookupErr := c.FindBySession(ctx, spec.SessionID); lookupErr != nil {
				return sandbox.Environment{}, lookupErr
			} else if found {
				if err := c.verifyOrg(ctx, existing.ID, spec.OrgID); err != nil {
					return sandbox.Environment{}, err
				}
				return existing, nil
			}
		}
		return sandbox.Environment{}, err
	}
	if vm.ID == "" || vm.Slug != slug(spec.SessionID) ||
		vm.Metadata["ao-session-id"] != spec.SessionID || vm.Metadata["ao-org-id"] != spec.OrgID {
		return sandbox.Environment{}, errors.New("freestyle: created VM did not match requested session identity")
	}
	return toEnvironment(vm), nil
}

func (c *Client) verifyOrg(ctx context.Context, id sandbox.ID, orgID string) error {
	var vm vmView
	if err := c.request(ctx, http.MethodGet, vmPath(string(id)), nil, nil, &vm); err != nil {
		return err
	}
	if vm.Metadata["ao-org-id"] != orgID {
		return errors.New("freestyle: VM belongs to another organization")
	}
	return nil
}

func (c *Client) Get(ctx context.Context, id sandbox.ID) (sandbox.Environment, error) {
	var vm vmView
	err := c.request(ctx, http.MethodGet, vmPath(string(id)), nil, nil, &vm)
	if isNotFound(err) {
		return sandbox.Environment{}, sandbox.ErrNotFound
	}
	if err != nil {
		return sandbox.Environment{}, err
	}
	if vm.ID != string(id) {
		return sandbox.Environment{}, errors.New("freestyle: VM ID mismatch")
	}
	return toEnvironment(vm), nil
}

func (c *Client) FindBySession(ctx context.Context, sessionID string) (sandbox.Environment, bool, error) {
	var vm vmView
	err := c.request(ctx, http.MethodGet, vmPath(slug(sessionID)), nil, nil, &vm)
	if isNotFound(err) {
		return sandbox.Environment{}, false, nil
	}
	if err != nil {
		return sandbox.Environment{}, false, err
	}
	if vm.ID == "" || vm.Slug != slug(sessionID) || vm.Metadata["ao-session-id"] != sessionID {
		return sandbox.Environment{}, false, fmt.Errorf("freestyle: VM slug %q belongs to another session", slug(sessionID))
	}
	return toEnvironment(vm), true, nil
}

func (c *Client) Start(ctx context.Context, id sandbox.ID) error { return c.Resume(ctx, id) }
func (c *Client) Stop(ctx context.Context, id sandbox.ID) error  { return c.Pause(ctx, id) }

func (c *Client) Pause(ctx context.Context, id sandbox.ID) error {
	var vm vmView
	if err := c.request(ctx, http.MethodGet, vmPath(string(id)), nil, nil, &vm); err != nil {
		return err
	}
	if vm.State == "starting" {
		var err error
		vm, err = c.waitForState(ctx, id, "starting")
		if err != nil {
			return err
		}
	}
	if vm.State == "paused" || vm.State == "pausing" {
		return nil
	}
	if vm.State != "running" {
		return fmt.Errorf("freestyle: cannot pause VM in state %q", vm.State)
	}
	return c.request(ctx, http.MethodPost, vmPath(string(id))+"/pause", map[string]any{}, nil, nil)
}

func (c *Client) Resume(ctx context.Context, id sandbox.ID) error {
	var vm vmView
	if err := c.request(ctx, http.MethodGet, vmPath(string(id)), nil, nil, &vm); err != nil {
		return err
	}
	if vm.State == "pausing" {
		var err error
		vm, err = c.waitForState(ctx, id, "pausing")
		if err != nil {
			return err
		}
	}
	if vm.State == "running" || vm.State == "starting" {
		return nil
	}
	if vm.State != "paused" && vm.State != "stopped" {
		return fmt.Errorf("freestyle: cannot start VM in state %q", vm.State)
	}
	return c.request(ctx, http.MethodPost, vmPath(string(id))+"/start", map[string]any{}, nil, nil)
}

// A user can ask to pause while a create is still booting, or resume before a
// pause has completed. Wait for the provider transition instead of recording a
// failed AO reconciliation for an ordinary lifecycle race.
func (c *Client) waitForState(ctx context.Context, id sandbox.ID, transitional string) (vmView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return vmView{}, fmt.Errorf("freestyle: VM remained %s: %w", transitional, ctx.Err())
		case <-ticker.C:
			var vm vmView
			if err := c.request(ctx, http.MethodGet, vmPath(string(id)), nil, nil, &vm); err != nil {
				return vmView{}, err
			}
			if vm.State != transitional {
				return vm, nil
			}
		}
	}
}

func (c *Client) Delete(ctx context.Context, id sandbox.ID) error {
	err := c.request(ctx, http.MethodDelete, vmPath(string(id)), nil, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func vmPath(id string) string { return "/v5/vms/" + url.PathEscape(id) }

// BootstrapWorker installs the release binaries and launches the worker as an
// unprivileged guest user. The one-time ticket is passed only on the launch,
// never baked into a reusable snapshot or provider resource profile.
func (c *Client) BootstrapWorker(ctx context.Context, id sandbox.ID, bootstrap sandbox.WorkerBootstrap) error {
	if len(bootstrap.Binary) == 0 || !safePath(bootstrap.Destination) {
		return errors.New("freestyle: worker binary and safe absolute destination are required")
	}
	if len(bootstrap.HelperBinary) > 0 && !safePath(bootstrap.HelperDestination) {
		return errors.New("freestyle: helper destination must be a safe absolute path")
	}
	if bootstrap.User == "" || !linuxUserName.MatchString(bootstrap.User) {
		return errors.New("freestyle: worker user is invalid")
	}
	for key := range bootstrap.Environment {
		if !environmentKey.MatchString(key) {
			return fmt.Errorf("freestyle: invalid worker environment key %q", key)
		}
	}
	// An Ubuntu snapshot already has /usr/local/bin; prepare any custom paths
	// and the durable workspace before the file API writes the binaries.
	setup := "set -e; mkdir -p " + quote(path.Dir(bootstrap.Destination)) +
		" /workspace/repository /workspace/.ao/worker /workspace/.ao/home"
	if len(bootstrap.HelperBinary) > 0 {
		setup += " " + quote(path.Dir(bootstrap.HelperDestination))
	}
	if err := c.exec(ctx, id, setup); err != nil {
		return err
	}
	if err := c.writeFile(ctx, id, bootstrap.Destination, bootstrap.Binary); err != nil {
		return err
	}
	if len(bootstrap.HelperBinary) > 0 {
		if err := c.writeFile(ctx, id, bootstrap.HelperDestination, bootstrap.HelperBinary); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(bootstrap.Environment))
	for key := range bootstrap.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var env strings.Builder
	for _, key := range keys {
		env.WriteString(" " + key + "=" + quote(bootstrap.Environment[key]))
	}
	user := quote(bootstrap.User)
	script := "set -e; id -u " + user + " >/dev/null 2>&1 || useradd --home-dir /workspace/.ao/home --shell /bin/bash " + user + "; " +
		"chown -R " + user + ":" + user + " /workspace; " +
		"pkill -f " + quote("^"+bootstrap.Destination+"( |$)") + " || true; " +
		"nohup runuser --user " + user + " -- env" + env.String() + " " + quote(bootstrap.Destination) +
		" >> /var/log/ao-worker.log 2>&1 < /dev/null &"
	return c.exec(ctx, id, script)
}

func safePath(value string) bool {
	return strings.HasPrefix(value, "/") && value != "/" && path.Clean(value) == value &&
		!strings.ContainsAny(value, "\n\r\x00")
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (c *Client) exec(ctx context.Context, id sandbox.ID, script string) error {
	var result struct {
		StatusCode *int   `json:"statusCode"`
		Stderr     string `json:"stderr"`
	}
	err := c.request(ctx, http.MethodPost, vmPath(string(id))+"/exec-await",
		map[string]any{"command": script, "linuxUser": "root", "timeoutMs": 300000}, nil, &result)
	if err != nil {
		return err
	}
	if result.StatusCode == nil {
		return errors.New("freestyle: bootstrap command timed out")
	}
	if *result.StatusCode != 0 {
		return fmt.Errorf("freestyle: bootstrap command exited %d: %s", *result.StatusCode, truncate(result.Stderr, 512))
	}
	return nil
}

func (c *Client) writeFile(ctx context.Context, id sandbox.ID, destination string, data []byte) error {
	hash := sha256.Sum256(data)
	base := vmPath(string(id)) + "/fs"
	if len(data) <= 8<<20 {
		query := url.Values{"path": {destination}, "mode": {"493"}, "sha256": {hex.EncodeToString(hash[:])}}
		return c.request(ctx, http.MethodPut, base+"/write?"+query.Encode(), nil, data, nil)
	}
	var upload struct {
		UploadID      string `json:"uploadId"`
		ReceivedBytes int    `json:"receivedBytes"`
	}
	err := c.request(ctx, http.MethodPost, base+"/uploads",
		map[string]any{"path": destination, "size": len(data), "sha256": hex.EncodeToString(hash[:]), "mode": 493}, nil, &upload)
	if err != nil {
		return err
	}
	if upload.UploadID == "" {
		return errors.New("freestyle: upload response had no ID")
	}
	uploadPath := base + "/uploads/" + url.PathEscape(upload.UploadID)
	committed := false
	defer func() {
		if !committed {
			_ = c.request(context.Background(), http.MethodDelete, uploadPath, nil, nil, nil)
		}
	}()
	for offset := upload.ReceivedBytes; offset < len(data); {
		end := min(offset+chunkSize, len(data))
		partPath := uploadPath + "?offset=" + strconv.Itoa(offset)
		var progress struct {
			ReceivedBytes int `json:"receivedBytes"`
		}
		if err := c.request(ctx, http.MethodPut, partPath, nil, data[offset:end], &progress); err != nil {
			return err
		}
		if progress.ReceivedBytes != end {
			return fmt.Errorf("freestyle: upload advanced to %d, expected %d", progress.ReceivedBytes, end)
		}
		offset = end
	}
	if err := c.request(ctx, http.MethodPost, uploadPath+"/commit", map[string]any{}, nil, nil); err != nil {
		return err
	}
	committed = true
	return nil
}

type statusError struct {
	code    int
	message string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("freestyle API returned %d: %s", e.code, e.message)
}
func isNotFound(err error) bool {
	var e *statusError
	return errors.As(err, &e) && e.code == http.StatusNotFound
}
func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

func (c *Client) request(ctx context.Context, method, requestPath string, body any, raw []byte, out any) error {
	var content io.Reader
	contentType := ""
	if raw != nil {
		content = bytes.NewReader(raw)
		contentType = "application/octet-stream"
	} else if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		content = bytes.NewReader(encoded)
		contentType = "application/json"
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+requestPath, content)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("x-freestyle-background-after-secs", "5")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("freestyle: %s %s: %w", method, requestPath, err)
	}
	if response.StatusCode == http.StatusAccepted {
		var accepted struct {
			RequestID string `json:"requestId"`
			ResultURL string `json:"resultUrl"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(&accepted); err != nil {
			response.Body.Close()
			return fmt.Errorf("freestyle: background response: %w", err)
		}
		response.Body.Close()
		pollPath := accepted.ResultURL
		if pollPath == "" && accepted.RequestID != "" {
			pollPath = "/v5/background-requests/" + url.PathEscape(accepted.RequestID)
		}
		// Follow only this API's background-result route; no caller-supplied URL
		// can redirect the control plane's bearer credential to another origin.
		if !strings.HasPrefix(pollPath, "/v5/background-requests/") || strings.Contains(pollPath, "..") {
			return errors.New("freestyle: invalid background result path")
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backgroundPollInterval):
			}
			request, err = http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+pollPath, nil)
			if err != nil {
				return err
			}
			request.Header.Set("Authorization", "Bearer "+c.apiKey)
			response, err = c.http.Do(request)
			if err != nil {
				return fmt.Errorf("freestyle: poll background request: %w", err)
			}
			if response.StatusCode != http.StatusAccepted {
				break
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
			response.Body.Close()
		}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var detail struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&detail)
		return &statusError{code: response.StatusCode, message: truncate(detail.Code+": "+detail.Message, 512)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(out); err != nil {
		return fmt.Errorf("freestyle: decode %s: %w", requestPath, err)
	}
	return nil
}
