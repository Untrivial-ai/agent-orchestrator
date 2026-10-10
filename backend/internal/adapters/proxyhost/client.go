// Package proxyhost talks to AO's account helper, the detached process that holds provider sign-ins.
package proxyhost

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var errDown = errors.New("account helper is not running")

// statusError is the HTTP status the helper refused a call with.
type statusError int

func (e statusError) Error() string {
	return "account helper call failed: HTTP " + strconv.Itoa(int(e))
}

func code(err error) int {
	status, _ := errors.AsType[statusError](err)
	return int(status)
}

func notFound(err error) bool { return code(err) == http.StatusNotFound }

type identity struct {
	Port         int    `json:"port"`
	ControlKey   string `json:"control_key"`
	InferenceKey string `json:"inference_key"`
	TicketKey    string `json:"ticket_key"`
}

// Client is the daemon's side of the account helper.
type Client struct {
	mu           sync.Mutex
	root, binary string
	id           identity
	http         *http.Client
	relays       sync.Map
}

// New loads or creates the helper's identity under root; an empty binary is the one beside the daemon.
func New(root, binary string) (*Client, error) {
	if binary == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate account helper: %w", err)
		}
		binary = filepath.Join(filepath.Dir(self), "ao-proxy-host")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
	}
	c := &Client{root: root, binary: binary, http: &http.Client{Timeout: time.Minute, Transport: &http.Transport{Proxy: nil}}}
	path := filepath.Join(root, "run", "host.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, err = newIdentity(path)
	}
	if err == nil {
		err = json.Unmarshal(data, &c.id)
	}
	if err != nil {
		return nil, fmt.Errorf("account helper identity: %w", err)
	}
	return c, nil
}

func newIdentity(path string) ([]byte, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address, _ := listener.Addr().(*net.TCPAddr)
	id := identity{Port: address.Port}
	_ = listener.Close()
	for _, key := range []*string{&id.ControlKey, &id.InferenceKey, &id.TicketKey} {
		var raw [32]byte
		_, _ = rand.Read(raw[:])
		*key = hex.EncodeToString(raw[:])
	}
	data, _ := json.Marshal(id)
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	return data, os.WriteFile(path, data, 0o600)
}

// Endpoint is the loopback address sessions send model requests to.
func (c *Client) Endpoint() string { return "http://127.0.0.1:" + strconv.Itoa(c.id.Port) }

// TicketKey is the private key session tickets are derived from.
func (c *Client) TicketKey() ([]byte, error) { return hex.DecodeString(c.id.TicketKey) }

// do sends one control request. Errors never carry the helper's answer.
func (c *Client) do(ctx context.Context, method, path string, body, out any, loginID ...string) error {
	var payload io.Reader
	if data, _ := json.Marshal(body); body != nil {
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint()+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.id.ControlKey)
	req.Header.Set("Content-Type", "application/json")
	for _, id := range loginID {
		req.Header.Set("X-AO-Login-ID", id)
	}
	response, err := c.http.Do(req)
	if err != nil {
		// Unwrapped, because the request address can carry a secret.
		return errors.Join(errDown, errors.Unwrap(err))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode > 299 {
		return statusError(response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(out)
}

func (c *Client) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var status any
	if err := c.do(ctx, http.MethodGet, "/ao/status", nil, &status); err != nil {
		return err
	}
	if at(status, "protocol_version") != 3.0 {
		return errors.New("account helper protocol is incompatible; update AO after stopping its sessions")
	}
	return nil
}

// call starts the helper when nothing is listening, then sends the request.
func (c *Client) call(ctx context.Context, method, path string, body, out any, loginID ...string) error {
	c.mu.Lock()
	err := c.probe(ctx)
	if errors.Is(err, errDown) && ctx.Err() == nil {
		err = c.start(ctx)
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.do(ctx, method, path, body, out, loginID...)
}

func (c *Client) start(ctx context.Context) error {
	_ = os.MkdirAll(filepath.Join(c.root, "logs"), 0o700)
	log, err := os.OpenFile(filepath.Join(c.root, "logs", "host.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	cmd := process.Command(c.binary, "--data-dir", c.root, "--port", strconv.Itoa(c.id.Port))
	cmd.Dir, cmd.Stdout, cmd.Stderr = c.root, log, log
	cmd.Env = append(os.Environ(), "AO_PROXY_CONTROL_KEY="+c.id.ControlKey, "AO_PROXY_INFERENCE_KEY="+c.id.InferenceKey, "WRITABLE_PATH="+c.root, "MANAGEMENT_PASSWORD=")
	cmd.SysProcAttr = detached
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start account helper: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(100 * time.Millisecond) {
		if c.probe(ctx) == nil {
			return nil
		}
	}
	return cmp.Or(ctx.Err(), errors.New("account helper did not become ready"))
}

// ApplyRoutes replaces the helper's whole table; its one conflict is an account still in use.
func (c *Client) ApplyRoutes(ctx context.Context, routes []ports.ProviderRoute, authIDs []string) error {
	err := c.call(ctx, http.MethodPut, "/ao/routes", map[string]any{"routes": routes, "auth_ids": authIDs}, nil)
	if code(err) == http.StatusConflict {
		return ports.ErrProviderAccountBusy
	}
	return err
}

// AccountModels lists the models a person can pick for one account.
func (c *Client) AccountModels(ctx context.Context, a domain.ProviderAccount) ([]ports.AgentModelInfo, error) {
	var out struct{ Models []ports.AgentModelInfo }
	err := c.call(ctx, http.MethodPost, "/ao/account-models", on(a), &out)
	// The catalogue also names the models Codex calls for itself.
	return slices.DeleteFunc(out.Models, func(m ports.AgentModelInfo) bool {
		return strings.HasPrefix(m.ID, "gpt-image-") || m.ID == "codex-auto-review"
	}), err
}

// Credentials lists the sign-ins held as files, with the helper's verdict on a refused one.
func (c *Client) Credentials(ctx context.Context) (credentials []ports.ProviderCredential, err error) {
	var listing any
	if err := c.call(ctx, http.MethodGet, "/v8/management/credentials", nil, &listing); err != nil {
		return nil, err
	}
	files, _ := at(listing, "files").([]any)
	for _, file := range files {
		if text(file, "source") != "file" {
			continue
		}
		modified, _ := time.Parse(time.RFC3339, text(file, "modtime"))
		reason := strings.ToLower(text(file, "status_message"))
		credentials = append(credentials, ports.ProviderCredential{AuthID: text(file, "id"), Name: text(file, "name"), Provider: text(file, "provider"), ModifiedAt: modified,
			Failed: at(file, "disabled") == true || text(file, "status") == "disabled" || strings.Contains(reason, "unauthorized") || strings.Contains(reason, "invalid grant") || strings.Contains(reason, "invalid_grant")})
	}
	for _, provider := range domain.AccountProviders {
		keys, _ := c.apiKeys(ctx, provider)
		for _, key := range keys {
			if index := text(key, "auth-index"); index != "" {
				credentials = append(credentials, ports.ProviderCredential{Name: "config-index:" + provider + ":" + index, Provider: provider})
			}
		}
	}
	return credentials, nil
}

// DeleteCredential removes a sign-in file, or an API key by its reference.
func (c *Client) DeleteCredential(ctx context.Context, ref string) error {
	rest, apiKey := strings.CutPrefix(ref, "config-index:")
	if !apiKey {
		err := c.call(ctx, http.MethodDelete, "/v8/management/credentials?name="+url.QueryEscape(ref), nil, nil)
		if notFound(err) {
			return nil
		}
		return err
	}
	provider, index, _ := strings.Cut(rest, ":")
	keys, err := c.apiKeys(ctx, provider)
	// The helper deletes by position, so the position is read just before.
	if i := slices.IndexFunc(keys, func(key any) bool { return at(key, "auth-index") == index }); i >= 0 {
		return c.call(ctx, http.MethodDelete, "/v0/management/"+provider+"-api-key?index="+strconv.Itoa(i), nil, nil)
	}
	return err
}

func (c *Client) apiKeys(ctx context.Context, provider string) ([]any, error) {
	var listing any
	err := c.call(ctx, http.MethodGet, "/v0/management/"+provider+"-api-key", nil, &listing)
	keys, ok := at(listing, provider+"-api-key").([]any)
	if err == nil && !ok {
		err = errors.New("account helper returned no API-key list")
	}
	return keys, err
}
