package proxyhost

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// callbacks are the loopback port and path a provider's browser sign-in returns to.
var callbacks = map[string][2]string{"codex": {"1455", "/auth/callback"}, "claude": {"54545", "/callback"}}

// StartLogin starts a sign-in in the mode the request names; browser when none.
func (c *Client) StartLogin(ctx context.Context, id string, request ports.ProviderLoginRequest) (ports.ProviderLogin, error) {
	login := ports.ProviderLogin{ID: id, Provider: request.Provider, Mode: request.Mode, Status: "waiting"}
	var err error
	switch request.Mode {
	case "device":
		err = c.call(ctx, http.MethodPost, "/ao/login/device/start", map[string]string{"id": id}, &login)
	case "import":
		var file map[string]any
		if json.Unmarshal([]byte(request.CredentialJSON), &file) != nil || file["type"] != request.Provider {
			return login, ports.ErrProviderAccountIncompatible
		}
		err = c.call(ctx, http.MethodPost, "/v8/management/credentials?name="+url.QueryEscape("ao-"+id+".json"), json.RawMessage(request.CredentialJSON), nil, id)
	case "api_key":
		err = c.addAPIKey(ctx, id, request)
	default:
		return c.browserLogin(ctx, login)
	}
	return login, err
}

// baseHost is the host of a plain HTTP(S) address a key can be used against.
func baseHost(base string) string {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return u.Host
}

// addAPIKey adds a key beside the ones held, and takes it out if it cannot be tagged.
func (c *Client) addAPIKey(ctx context.Context, id string, request ports.ProviderLoginRequest) error {
	base := strings.TrimRight(strings.TrimSpace(request.BaseURL), "/")
	if baseHost(base) == "" {
		return errors.New("a valid HTTP(S) base URL is required")
	}
	keys, err := c.apiKeys(ctx, request.Provider)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if at(key, "api-key") == request.APIKey && strings.TrimRight(text(key, "base-url"), "/") == base {
			return ports.ErrProviderAccountConflict
		}
	}
	path := "/v0/management/" + request.Provider + "-api-key"
	if err := c.call(ctx, http.MethodPut, path, append(keys, map[string]any{"api-key": request.APIKey, "base-url": base}), nil, id); err != nil {
		return err
	}
	tag := map[string]string{"id": id, "provider": request.Provider, "api_key": request.APIKey, "base_url": base, "label": request.Label}
	if err = c.call(ctx, http.MethodPost, "/ao/tag-api-key", tag, nil); err != nil {
		_ = c.call(ctx, http.MethodDelete, path+"?api-key="+url.QueryEscape(request.APIKey)+"&base-url="+url.QueryEscape(base), nil, nil)
	}
	return err
}

// browserLogin reserves the callback port first, then relays only the matching callback.
func (c *Client) browserLogin(ctx context.Context, login ports.ProviderLogin) (ports.ProviderLogin, error) {
	id, provider, callback := login.ID, login.Provider, callbacks[login.Provider]
	listener, err := net.Listen("tcp", "127.0.0.1:"+callback[0])
	if err != nil {
		return login, ports.ErrProviderLoginCallbackBusy
	}
	err = c.call(ctx, http.MethodGet, "/v8/management/oauth/auth-url?provider="+url.QueryEscape(provider), nil, &login, id)
	login.ID, login.Provider, login.Mode, login.Status = id, provider, "browser", "waiting"
	if err == nil && (login.State == "" || login.URL == "") {
		err = errors.New("provider did not return a login link")
	}
	if err != nil {
		_ = listener.Close()
		return login, err
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != callback[1] || query.Get("state") != login.State {
			http.Error(w, "Unknown login attempt", http.StatusBadRequest)
			return
		}
		ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		body := map[string]string{"provider": provider, "state": login.State, "code": query.Get("code"), "error": query.Get("error")}
		if c.call(ctx, http.MethodPost, "/v8/management/oauth/callback", body, nil) != nil {
			http.Error(w, "Login could not be completed. Return to AO and retry.", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("Login received. You can return to AO."))
	})}
	// The listener is closed too: the server may not be serving it yet.
	c.relays.Store(id, func() { _, _ = server.Close(), listener.Close() })
	go func() { _ = server.Serve(listener) }()
	time.AfterFunc(6*time.Minute, func() { c.closeRelay(id) })
	return login, nil
}

func (c *Client) closeRelay(id string) {
	stop, _ := c.relays.LoadAndDelete(id)
	if stop, ok := stop.(func()); ok {
		stop()
	}
}

// LoginStatus reports waiting, complete or failed for one attempt.
func (c *Client) LoginStatus(ctx context.Context, login ports.ProviderLogin) (string, error) {
	var result struct{ Status string }
	switch login.Mode {
	case "import", "api_key":
		if _, err := c.LoginResult(ctx, login.ID); notFound(err) {
			return "waiting", nil
		} else if err != nil {
			return "", err
		}
		return "complete", nil
	case "device":
		err := c.call(ctx, http.MethodGet, "/ao/login/status?id="+url.QueryEscape(login.ID), nil, &result)
		return result.Status, err
	}
	if err := c.call(ctx, http.MethodGet, "/v8/management/oauth/status?state="+url.QueryEscape(login.State), nil, &result); err != nil {
		return "", err
	}
	status, known := map[string]string{"wait": "waiting", "ok": "complete", "error": "failed"}[result.Status]
	if !known {
		return "", errors.New("unknown login status")
	}
	if status != "waiting" {
		c.closeRelay(login.ID)
	}
	return status, nil
}

// CancelLogin abandons an attempt and releases its callback port.
func (c *Client) CancelLogin(ctx context.Context, login ports.ProviderLogin) error {
	switch login.Mode {
	case "import":
		return c.DeleteCredential(ctx, "ao-"+login.ID+".json")
	case "api_key":
		return nil
	case "device":
		return c.call(ctx, http.MethodDelete, "/ao/login/status?id="+url.QueryEscape(login.ID), nil, nil)
	}
	defer c.closeRelay(login.ID)
	return c.call(ctx, http.MethodDelete, "/v8/management/oauth/session?state="+url.QueryEscape(login.State), nil, nil)
}

// LoginResult names the sign-in a finished attempt produced.
func (c *Client) LoginResult(ctx context.Context, id string) (result ports.VerifiedProviderLogin, err error) {
	err = c.call(ctx, http.MethodGet, "/ao/login-result/"+url.PathEscape(id), nil, &result)
	return result, err
}
