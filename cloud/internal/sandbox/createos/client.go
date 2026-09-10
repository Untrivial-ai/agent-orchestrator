// Package createos implements AO's provider-neutral sandbox lifecycle against
// the NodeOps CreateOS Sandbox API — Firecracker micro-VMs created from a
// shape and a rootfs. No CreateOS type escapes this package.
package createos

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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

const (
	// defaultTimeout bounds a single control-plane call. Creation waits are
	// handled by the reconciler's retry loop, not by a long HTTP request.
	defaultTimeout = 2 * time.Minute
	// maxResponseBody bounds decoding so a hostile or broken response cannot
	// exhaust control-plane memory.
	maxResponseBody = 1 << 20
	// maxErrorBody bounds the error text retained on a failed call.
	maxErrorBody = 64 << 10
	// listPageLimit is the page size requested when scanning sandboxes.
	listPageLimit = 100
	// maxListPages bounds pagination so a broken cursor cannot loop forever.
	maxListPages = 50
	// deletePollInterval paces the wait for an accepted delete to finish.
	deletePollInterval = 2 * time.Second
	// deletePollTimeout bounds that wait. Giving up is not fatal on its own:
	// the reconciler retries the whole recreate on a later tick.
	deletePollTimeout = 2 * time.Minute
	// maxSandboxNameLength is the CreateOS limit on a sandbox name. Exceeding it
	// fails the create with 400 "too long", which the reconciler can only retry
	// into the same rejection.
	maxSandboxNameLength = 22
	sandboxNamePrefix    = "ao-"
	// sandboxNameDigits is how much of the session id fits after the prefix.
	sandboxNameDigits = maxSandboxNameLength - len(sandboxNamePrefix)
)

// HTTPError is a non-2xx response from the CreateOS API. The body is truncated
// and never contains the API key, which is only ever sent as a header.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("createos api returned %d", e.StatusCode)
	}
	return fmt.Sprintf("createos api returned %d: %s", e.StatusCode, e.Body)
}

// Client talks to one CreateOS control plane with one API key.
type Client struct {
	baseURL      string
	apiKey       string
	defaultShape string
	defaultRoot  string
	region       string
	sshPubKeys   []string
	http         *http.Client
	// deletePoll is how long Recreate waits between checks that a deleted
	// sandbox has actually gone away. A field rather than a constant so tests
	// can drive the poll loop without sleeping.
	deletePoll time.Duration
}

var (
	_ sandbox.Provider     = (*Client)(nil)
	_ sandbox.Recreator    = (*Client)(nil)
	_ sandbox.Bootstrapper = (*Client)(nil)
)

// Config configures a CreateOS client.
type Config struct {
	BaseURL      string
	APIKey       string
	DefaultShape string
	DefaultRoot  string
	Region       string
	SSHPubKeys   []string
	HTTPClient   *http.Client
}

// New creates a CreateOS sandbox provider.
func New(config Config) *Client {
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		baseURL:      strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"),
		apiKey:       strings.TrimSpace(config.APIKey),
		defaultShape: strings.TrimSpace(config.DefaultShape),
		defaultRoot:  strings.TrimSpace(config.DefaultRoot),
		region:       strings.TrimSpace(config.Region),
		sshPubKeys:   append([]string(nil), config.SSHPubKeys...),
		http:         httpClient,
		deletePoll:   deletePollInterval,
	}
}

// sandboxView is the CreateOS projection of one sandbox.
type sandboxView struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Name           string `json:"name"`
	Shape          string `json:"shape"`
	RootFS         string `json:"rootfs"`
	Region         string `json:"region"`
	VCPU           int    `json:"vcpu"`
	MemMiB         int    `json:"mem_mib"`
	DiskMiB        int    `json:"disk_mib"`
	IngressEnabled bool   `json:"ingress_enabled"`
}

type createSandboxRequest struct {
	Shape                 string            `json:"shape"`
	RootFS                string            `json:"rootfs,omitempty"`
	Name                  string            `json:"name,omitempty"`
	Envs                  map[string]string `json:"envs,omitempty"`
	SSHPubKeys            []string          `json:"ssh_pubkeys,omitempty"`
	Region                string            `json:"region,omitempty"`
	IngressEnabled        bool              `json:"ingress_enabled,omitempty"`
	AutoPauseAfterSeconds int               `json:"auto_pause_after_seconds,omitempty"`
}

// Create provisions one sandbox for a session.
func (c *Client) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Environment, error) {
	shape := firstNonEmpty(spec.Shape, c.defaultShape)
	if shape == "" {
		return sandbox.Environment{}, errors.New("createos: no shape configured for this sandbox")
	}
	// The caller names a sandbox for AO's own vocabulary, which is longer than
	// CreateOS accepts. Deriving the name here keeps that limit inside the one
	// package that knows about it, and keeps it identical to what FindBySession
	// will later look for.
	name := strings.TrimSpace(spec.Name)
	if spec.SessionID != "" {
		name = SandboxName(spec.SessionID)
	}
	body := createSandboxRequest{
		Shape:          shape,
		RootFS:         firstNonEmpty(spec.RootFS, c.defaultRoot),
		Name:           name,
		Envs:           spec.Environment,
		SSHPubKeys:     c.sshPubKeys,
		Region:         c.region,
		IngressEnabled: strings.EqualFold(strings.TrimSpace(spec.Ingress), "enabled"),
	}
	if spec.AutoPauseSeconds > 0 {
		body.AutoPauseAfterSeconds = spec.AutoPauseSeconds
	}
	var view sandboxView
	if err := c.do(ctx, http.MethodPost, "/v1/sandboxes", body, &view); err != nil {
		return sandbox.Environment{}, err
	}
	return toEnvironment(view), nil
}

// Get returns the current provider view of one sandbox.
func (c *Client) Get(ctx context.Context, id sandbox.ID) (sandbox.Environment, error) {
	var view sandboxView
	if err := c.do(ctx, http.MethodGet, "/v1/sandboxes/"+url.PathEscape(string(id)), nil, &view); err != nil {
		return sandbox.Environment{}, err
	}
	return toEnvironment(view), nil
}

// FindBySession looks up the sandbox belonging to a session. CreateOS sandboxes
// carry no user metadata, so the correlation key is the name AO assigns at
// create time, which the API keeps unique per account.
func (c *Client) FindBySession(ctx context.Context, sessionID string) (sandbox.Environment, bool, error) {
	wanted := SandboxName(sessionID)
	offset := 0
	for page := 0; page < maxListPages; page++ {
		path := "/v1/sandboxes?limit=" + strconv.Itoa(listPageLimit) +
			"&offset=" + strconv.Itoa(offset)
		var response listResponse
		if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
			return sandbox.Environment{}, false, err
		}
		items := response.items()
		for _, view := range items {
			if view.Name != wanted {
				continue
			}
			// A destroyed row is history, not a live environment to adopt.
			if normalizeState(view.Status) == sandbox.StateDeleted {
				continue
			}
			return toEnvironment(view), true, nil
		}
		if len(items) == 0 || response.exhausted(offset, len(items)) {
			break
		}
		offset += len(items)
	}
	return sandbox.Environment{}, false, nil
}

// Start resumes a paused sandbox. CreateOS has no separate stopped state.
func (c *Client) Start(ctx context.Context, id sandbox.ID) error {
	return c.Resume(ctx, id)
}

// Stop pauses a sandbox, which stops compute billing.
func (c *Client) Stop(ctx context.Context, id sandbox.ID) error {
	return c.Pause(ctx, id)
}

// Pause snapshots memory and disk, then suspends the VM.
func (c *Client) Pause(ctx context.Context, id sandbox.ID) error {
	return c.do(ctx, http.MethodPost, "/v1/sandboxes/"+url.PathEscape(string(id))+"/pause", nil, nil)
}

// Resume restores a paused sandbox in place — same id, same filesystem, same
// memory — so a worker suspended by idle auto-pause picks up where it left off.
func (c *Client) Resume(ctx context.Context, id sandbox.ID) error {
	return c.do(ctx, http.MethodPost, "/v1/sandboxes/"+url.PathEscape(string(id))+"/resume", nil, nil)
}

// Delete reclaims a sandbox.
func (c *Client) Delete(ctx context.Context, id sandbox.ID) error {
	err := c.do(ctx, http.MethodDelete, "/v1/sandboxes/"+url.PathEscape(string(id)), nil, nil)
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil
	}
	return err
}

// Recreate replaces a sandbox with fresh compute. CreateOS names are unique per
// account, so the old sandbox must be gone before the replacement can take its
// name. No disk is attached in this configuration, so the replacement starts
// from a clean workspace and uncommitted work is lost.
func (c *Client) Recreate(
	ctx context.Context,
	id sandbox.ID,
	spec sandbox.Spec,
) (sandbox.Environment, error) {
	if err := c.Delete(ctx, id); err != nil {
		return sandbox.Environment{}, err
	}
	if err := c.awaitDeleted(ctx, id); err != nil {
		return sandbox.Environment{}, err
	}
	return c.Create(ctx, spec)
}

// awaitDeleted blocks until a deleted sandbox is really gone. CreateOS accepts
// a DELETE and returns immediately, leaving the VM in destroying until it
// reaches destroyed; creating the replacement before then collides with a name
// the API still considers taken, which fails the recreate and leaves the next
// tick to repeat the same collision.
func (c *Client) awaitDeleted(ctx context.Context, id sandbox.ID) error {
	deadline := time.Now().Add(deletePollTimeout)
	for {
		environment, err := c.Get(ctx, id)
		switch {
		case errors.Is(err, sandbox.ErrNotFound):
			return nil
		case err != nil:
			return err
		case environment.State == sandbox.StateDeleted:
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf(
				"createos: sandbox %s was still %s %s after its delete was accepted",
				id,
				environment.State,
				deletePollTimeout,
			)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.deletePoll):
		}
	}
}

type execRequest struct {
	Cmd  string   `json:"cmd"`
	Args []string `json:"args,omitempty"`
}

type execResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error"`
}

type execResponse struct {
	Result execResult `json:"result"`
}

// BootstrapWorker uploads the AO worker into a live sandbox and launches it.
// Using exec instead of a baked-in entrypoint keeps the rootfs generic and lets
// the reconciler repair a sandbox without replacing its compute.
func (c *Client) BootstrapWorker(
	ctx context.Context,
	id sandbox.ID,
	bootstrap sandbox.WorkerBootstrap,
) error {
	destination := strings.TrimSpace(bootstrap.Destination)
	if destination == "" || !strings.HasPrefix(destination, "/") {
		return fmt.Errorf("createos: worker destination %q must be an absolute path", bootstrap.Destination)
	}
	if len(bootstrap.Binary) == 0 {
		return errors.New("createos: worker binary is empty")
	}
	helperDestination := strings.TrimSpace(bootstrap.HelperDestination)
	if len(bootstrap.HelperBinary) > 0 &&
		(helperDestination == "" || !strings.HasPrefix(helperDestination, "/")) {
		return fmt.Errorf("createos: worker helper destination %q must be an absolute path", bootstrap.HelperDestination)
	}

	// Fast path: a template that pre-bakes these exact binaries lets the whole
	// bootstrap collapse to one exec (hash check + launch), skipping both
	// multi-megabyte uploads - the dominant cost of a fresh-sandbox bootstrap.
	// Any miss (older template, corrupted file, exec hiccup) falls through to
	// the plain upload path below, so a stale template self-heals.
	if c.launchBakedWorker(ctx, id, bootstrap, destination, helperDestination) {
		return nil
	}

	// The binaries go up as file PUTs and stay their own requests. Their parents
	// must exist before those PUTs, so create each distinct parent first. Every
	// remaining shell step is collapsed into the single exec built below: each
	// exec is a separate control-plane round-trip, and the old one-step-per-call
	// form cost six of them on a plain launch (eleven with a helper and a run-as
	// user).
	// Each binary is staged beside its destination because on the repair path
	// the previous worker is usually still running (a partitioned worker only
	// exits after its heartbeats fail) and Linux refuses to write a running
	// executable with ETXTBSY; renaming over it swaps the directory entry
	// instead, which the kernel allows while the old inode is still mapped.
	_, workerParent := path(destination)
	if workerParent != "" {
		if err := c.exec(ctx, id, "mkdir", []string{"-p", workerParent}); err != nil {
			return err
		}
	}
	staging := destination + ".new"
	if err := c.uploadFile(ctx, id, staging, bootstrap.Binary); err != nil {
		return err
	}
	helperStaging := ""
	if len(bootstrap.HelperBinary) > 0 {
		_, helperParent := path(helperDestination)
		if helperParent != "" && helperParent != workerParent {
			if err := c.exec(ctx, id, "mkdir", []string{"-p", helperParent}); err != nil {
				return err
			}
		}
		helperStaging = helperDestination + ".new"
		if err := c.uploadFile(ctx, id, helperStaging, bootstrap.HelperBinary); err != nil {
			return err
		}
	}

	// One exec does the whole install and launch: mark the staged binaries
	// executable, stop any old worker, swap them into place, optionally hand
	// /workspace to the run-as user, and launch. set -e fails the exec if any
	// synchronous step fails; only the final launch is backgrounded, so the exec
	// still returns promptly. The pkill pattern is anchored at the start of the
	// command line because an unanchored one also matches this very shell (whose
	// arguments contain the destination) so it would kill itself; "|| true"
	// tolerates the normal first-boot no-match.
	var script strings.Builder
	script.WriteString("set -e; ")
	script.WriteString("chmod 0755 " + shellQuote(staging) + "; ")
	if helperStaging != "" {
		script.WriteString("chmod 0755 " + shellQuote(helperStaging) + "; ")
	}
	script.WriteString("{ pkill -f " + shellQuote("^"+destination+"( |$)") + " || true; }; ")
	script.WriteString("mv -f " + shellQuote(staging) + " " + shellQuote(destination) + "; ")
	if helperStaging != "" {
		script.WriteString("mv -f " + shellQuote(helperStaging) + " " + shellQuote(helperDestination) + "; ")
	}

	// Repair issues a new one-time bootstrap ticket, so pass the fresh
	// environment to this process rather than the one stored at sandbox
	// creation, whose ticket is already consumed.
	command := launchEnvironment(bootstrap.Environment) + shellQuote(destination)
	if user := strings.TrimSpace(bootstrap.User); user != "" {
		quotedUser := shellQuote(user)
		// Create the run-as user if the image does not already provide it. A
		// template that lacks it (github-v4 has no ao-worker) would otherwise
		// wedge every bootstrap on "id: no such user", which the reconciler
		// retries forever as a recreate loop that burns sandbox credits. Under
		// set -e, id succeeding skips useradd; id failing runs useradd, and if
		// useradd itself fails the bootstrap still fails rather than launching
		// as the wrong identity. The account matches the worker image: an
		// unprivileged user that owns /workspace.
		script.WriteString("id -u " + quotedUser + " >/dev/null 2>&1 || " +
			"useradd --create-home --home-dir /workspace/.ao/home --shell /bin/bash " +
			quotedUser + "; ")
		script.WriteString("chown -R " + quotedUser + ":" + quotedUser + " /workspace; ")
		command = "runuser --user " + quotedUser + " -- " + command
	}
	script.WriteString("nohup " + command + " >> /var/log/ao-worker.log 2>&1 &")
	return c.exec(ctx, id, "bash", []string{"-c", script.String()})
}

// launchBakedWorker launches template-baked binaries when their content hashes
// match exactly what this control plane would upload. One exec verifies and
// launches; the stop/user/launch tail mirrors BootstrapWorker's upload path
// (kept in step by hand - the upload path additionally stages and swaps files,
// so the two cannot share one literal script). Returns true only when the
// baked launch actually happened; every other outcome (hash miss, missing
// binary, exec error) reports false so the caller uploads as before.
func (c *Client) launchBakedWorker(
	ctx context.Context,
	id sandbox.ID,
	bootstrap sandbox.WorkerBootstrap,
	destination, helperDestination string,
) bool {
	hashGuard := func(path string, binary []byte) string {
		sum := sha256.Sum256(binary)
		return "[ -x " + shellQuote(path) + " ] && " +
			"[ \"$(sha256sum " + shellQuote(path) + " | cut -d\" \" -f1)\" = " +
			shellQuote(hex.EncodeToString(sum[:])) + " ]"
	}
	var script strings.Builder
	script.WriteString("set -e; ")
	script.WriteString("if ! { " + hashGuard(destination, bootstrap.Binary))
	if len(bootstrap.HelperBinary) > 0 {
		script.WriteString(" && " + hashGuard(helperDestination, bootstrap.HelperBinary))
	}
	script.WriteString("; }; then echo AO_BAKED_MISS; exit 0; fi; ")
	script.WriteString("{ pkill -f " + shellQuote("^"+destination+"( |$)") + " || true; }; ")
	command := launchEnvironment(bootstrap.Environment) + shellQuote(destination)
	if user := strings.TrimSpace(bootstrap.User); user != "" {
		quotedUser := shellQuote(user)
		script.WriteString("id -u " + quotedUser + " >/dev/null 2>&1 || " +
			"useradd --create-home --home-dir /workspace/.ao/home --shell /bin/bash " +
			quotedUser + "; ")
		script.WriteString("chown -R " + quotedUser + ":" + quotedUser + " /workspace; ")
		command = "runuser --user " + quotedUser + " -- " + command
	}
	script.WriteString("nohup " + command + " >> /var/log/ao-worker.log 2>&1 & ")
	script.WriteString("echo AO_BAKED_OK")
	result, err := c.execCapture(ctx, id, "bash", []string{"-c", script.String()})
	if err != nil {
		return false
	}
	return strings.Contains(result.Stdout, "AO_BAKED_OK")
}

// execCapture runs a command and returns its captured result, failing on a
// nonzero exit like exec does.
func (c *Client) execCapture(
	ctx context.Context,
	id sandbox.ID,
	cmd string,
	args []string,
) (execResult, error) {
	var response execResponse
	if err := c.do(
		ctx,
		http.MethodPost,
		"/v1/sandboxes/"+url.PathEscape(string(id))+"/exec",
		execRequest{Cmd: cmd, Args: args},
		&response,
	); err != nil {
		return execResult{}, err
	}
	if response.Result.Error != "" {
		return execResult{}, fmt.Errorf("createos: %s could not start: %s", cmd, response.Result.Error)
	}
	if response.Result.ExitCode != 0 {
		return execResult{}, fmt.Errorf(
			"createos: %s exited %d: %s",
			cmd,
			response.Result.ExitCode,
			truncate(strings.TrimSpace(response.Result.Stderr), 512),
		)
	}
	return response.Result, nil
}

func (c *Client) exec(ctx context.Context, id sandbox.ID, cmd string, args []string) error {
	var response execResponse
	if err := c.do(
		ctx,
		http.MethodPost,
		"/v1/sandboxes/"+url.PathEscape(string(id))+"/exec",
		execRequest{Cmd: cmd, Args: args},
		&response,
	); err != nil {
		return err
	}
	if response.Result.Error != "" {
		return fmt.Errorf("createos: %s could not start: %s", cmd, response.Result.Error)
	}
	if response.Result.ExitCode != 0 {
		return fmt.Errorf(
			"createos: %s exited %d: %s",
			cmd,
			response.Result.ExitCode,
			truncate(strings.TrimSpace(response.Result.Stderr), 512),
		)
	}
	return nil
}

func (c *Client) uploadFile(ctx context.Context, id sandbox.ID, guestPath string, data []byte) error {
	target := c.baseURL + "/v1/sandboxes/" + url.PathEscape(string(id)) +
		"/files?path=" + url.QueryEscape(guestPath)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Api-Key", c.apiKey)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("createos: upload %s: %w", guestPath, err)
	}
	defer response.Body.Close()
	if err := statusError(response); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("createos: encode %s request: %w", path, err)
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
	request.Header.Set("X-Api-Key", c.apiKey)

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("createos: %s %s: %w", method, path, err)
	}
	defer response.Body.Close()

	if err := statusError(response); err != nil {
		return err
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
		return nil
	}
	// CreateOS wraps every payload in a JSend envelope, so the fields callers
	// want sit one level down under "data". Decoding the body straight into the
	// caller's struct would silently yield a zero value: a sandbox with no id,
	// or an exec result reporting exit code 0 for a command that never ran.
	var wrapped envelope
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(&wrapped); err != nil {
		return fmt.Errorf("createos: decode %s response: %w", path, err)
	}
	if len(wrapped.Data) == 0 {
		return fmt.Errorf("createos: %s response carried no data", path)
	}
	if err := json.Unmarshal(wrapped.Data, out); err != nil {
		return fmt.Errorf("createos: decode %s response: %w", path, err)
	}
	return nil
}

// envelope is the JSend wrapper CreateOS puts around every response:
// {"status":"success","data":{...}}. A non-2xx status is already rejected by
// statusError, so only the success shape needs unwrapping here.
type envelope struct {
	Status  string          `json:"status"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
}

// statusError converts a non-2xx response. A 404 becomes ErrNotFound, which is
// the only error the reconciler treats as proof an environment is gone; a
// quota rejection becomes ErrAtCapacity, which the reconciler retries rather
// than fails; everything else stays a transport failure that leaves observed
// state alone.
func statusError(response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode <= 299 {
		return nil
	}
	if response.StatusCode == http.StatusNotFound {
		return sandbox.ErrNotFound
	}
	snippet, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	body := truncate(strings.TrimSpace(string(snippet)), maxErrorBody)
	if response.StatusCode == http.StatusTooManyRequests ||
		(response.StatusCode == http.StatusForbidden &&
			strings.Contains(strings.ToLower(body), "quota")) {
		return fmt.Errorf("%w: createos %d: %s", sandbox.ErrAtCapacity, response.StatusCode, body)
	}
	return &HTTPError{
		StatusCode: response.StatusCode,
		Body:       body,
	}
}

// listResponse tolerates both a bare array and the common envelope shapes, so a
// change in pagination style does not silently return an empty page.
type listResponse struct {
	bare       []sandboxView
	Sandboxes  []sandboxView `json:"sandboxes"`
	Items      []sandboxView `json:"items"`
	Data       []sandboxView `json:"data"`
	Pagination struct {
		Total  int `json:"total"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
		Count  int `json:"count"`
	} `json:"pagination"`
}

func (l *listResponse) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return json.Unmarshal(trimmed, &l.bare)
	}
	type alias listResponse
	var decoded alias
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return err
	}
	*l = listResponse(decoded)
	return nil
}

func (l *listResponse) items() []sandboxView {
	switch {
	case len(l.bare) > 0:
		return l.bare
	case len(l.Sandboxes) > 0:
		return l.Sandboxes
	case len(l.Items) > 0:
		return l.Items
	default:
		return l.Data
	}
}

// exhausted reports whether the page just read was the last one. CreateOS pages
// by offset and reports the unpaged total, so the caller stops as soon as it has
// walked past that total rather than asking for a page that cannot exist.
func (l *listResponse) exhausted(offset, read int) bool {
	if l.Pagination.Total <= 0 {
		return true
	}
	return offset+read >= l.Pagination.Total
}

// SandboxName is the correlation key between an AO session and its sandbox.
// CreateOS caps a sandbox name at maxSandboxNameLength, which a full session
// UUID overruns, so the name carries the leading hex of the session instead:
// still unique in practice, still eyeball-matchable against a session id in a
// log line, and stable for the lifetime of the session because FindBySession
// has nothing else to correlate on.
func SandboxName(sessionID string) string {
	compact := strings.Map(func(r rune) rune {
		if r == '-' {
			return -1
		}
		return r
	}, strings.TrimSpace(sessionID))
	if len(compact) > sandboxNameDigits {
		compact = compact[:sandboxNameDigits]
	}
	return sandboxNamePrefix + compact
}

// normalizeState maps CreateOS lifecycle states onto AO's provider-neutral
// vocabulary. Any state this build does not know becomes provisioning — never
// running — because calling a sandbox ready before its worker has checked in
// suppresses the startup deadline and strands the session in silence.
func normalizeState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		return sandbox.StateRunning
	case "paused":
		return sandbox.StatePaused
	case "destroying":
		return sandbox.StateDeleting
	case "destroyed":
		return sandbox.StateDeleted
	case "creating", "pausing", "resuming", "forking":
		return sandbox.StateProvisioning
	case "error", "failed":
		// A failed VM is not gone, so it is not StateDeleted; reporting it as
		// not-yet-ready lets the startup deadline drive the repair.
		return sandbox.StateProvisioning
	default:
		return sandbox.StateProvisioning
	}
}

func toEnvironment(view sandboxView) sandbox.Environment {
	return sandbox.Environment{
		ID:     sandbox.ID(view.ID),
		Name:   view.Name,
		State:  normalizeState(view.Status),
		Target: view.Region,
		Resource: domain.ResourceProfile{
			CPU:    view.VCPU,
			Memory: view.MemMiB / 1024,
			Disk:   view.DiskMiB / 1024,
		},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// path splits an absolute guest path into its file name and parent directory.
func path(absolute string) (string, string) {
	index := strings.LastIndex(absolute, "/")
	if index <= 0 {
		return absolute, ""
	}
	return absolute[index+1:], absolute[:index]
}

// launchEnvironment renders "env K=V ... " for the worker launch, or the empty
// string when there is nothing to set. Keys are sorted so a relaunch produces a
// byte-identical command, which keeps the pkill pattern and the logs stable.
func launchEnvironment(environment map[string]string) string {
	if len(environment) == 0 {
		return ""
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+1)
	parts = append(parts, "env")
	for _, key := range keys {
		parts = append(parts, shellQuote(key+"="+environment[key]))
	}
	return strings.Join(parts, " ") + " "
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
