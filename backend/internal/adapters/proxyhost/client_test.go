package proxyhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AccountHelper = (*Client)(nil)

var (
	ctx        = context.Background()
	controlKey = strings.Repeat("c", 64)
)

// call is one request a fake helper received.
type call struct {
	Method, Path, Query, Body, LoginID string
}

func (c call) line() string { return c.Method + " " + c.Path }

// fakeHelper answers the helper's control HTTP. It is always ready; every
// other request is recorded and handed to answer. Status 0 loses the connection.
type fakeHelper struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []call
	answer func(call) (status int, body string)
}

func (h *fakeHelper) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") != "Bearer "+controlKey || r.URL.Host != "127.0.0.1:12345" {
		h.t.Errorf("request without the control key or off the helper: %s %s", r.Method, r.URL.Host)
	}
	status, body := http.StatusOK, `{"protocol_version":3}`
	if r.URL.Path != "/ao/status" {
		received := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, LoginID: r.Header.Get("X-AO-Login-ID")}
		if r.Body != nil {
			data, _ := io.ReadAll(r.Body)
			received.Body = string(data)
		}
		h.mu.Lock()
		h.calls = append(h.calls, received)
		h.mu.Unlock()
		if status, body = h.answer(received); status == 0 {
			return nil, errors.New("connection reset")
		}
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func (h *fakeHelper) lines() (lines []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, received := range h.calls {
		lines = append(lines, received.line())
	}
	return lines
}

func helperClient(t *testing.T, answer func(call) (int, string)) (*Client, *fakeHelper) {
	t.Helper()
	helper := &fakeHelper{t: t, answer: answer}
	id := identity{Port: 12345, ControlKey: controlKey, InferenceKey: strings.Repeat("b", 64), TicketKey: strings.Repeat("a", 64)}
	return &Client{root: t.TempDir(), binary: "/must-never-run", id: id, http: &http.Client{Transport: helper, Timeout: 5 * time.Second}}, helper
}

func TestIdentityIsCreatedOncePrivateAndReloaded(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proxy")
	first, err := New(root, "")
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if filepath.Dir(first.binary) != filepath.Dir(self) || !strings.HasPrefix(filepath.Base(first.binary), "ao-proxy-host") {
		t.Fatalf("helper binary=%s", first.binary)
	}
	path := filepath.Join(root, "run", "host.json")
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(saved, &fields); err != nil || len(fields) != 4 || fields["pid"] != nil {
		t.Fatalf("identity fields=%d err=%v", len(fields), err)
	}
	if stat, _ := os.Stat(path); stat.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode=%v", stat.Mode())
	}
	id := first.id
	key, err := first.TicketKey()
	if err != nil || len(key) != 32 || id.Port <= 0 || len(id.ControlKey) != 64 || id.ControlKey == id.InferenceKey || id.ControlKey == id.TicketKey || id.InferenceKey == id.TicketKey {
		t.Fatalf("identity port=%d ticket bytes=%d err=%v", id.Port, len(key), err)
	}
	if first.Endpoint() != "http://127.0.0.1:"+strconv.Itoa(id.Port) {
		t.Fatalf("endpoint=%s", first.Endpoint())
	}
	second, err := New(root, "/another/binary")
	if err != nil || second.id != id || second.binary != "/another/binary" {
		t.Fatalf("a second daemon changed the identity: err=%v", err)
	}
	if again, _ := os.ReadFile(path); string(again) != string(saved) {
		t.Fatal("the identity file was rewritten")
	}
	if err = os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = New(root, ""); err == nil {
		t.Fatal("a corrupt identity was accepted")
	}
}

func TestErrorsNeverCarryTheHelpersAnswerOrTheRequestAddress(t *testing.T) {
	status := http.StatusServiceUnavailable
	c, _ := helperClient(t, func(call) (int, string) { return status, `{"error":"token must-not-leak"}` })
	for _, lost := range []bool{false, true} {
		if lost {
			status = 0
		}
		_, err := c.LoginStatus(ctx, ports.ProviderLogin{ID: "attempt", State: "must-not-leak"})
		if err == nil || strings.Contains(err.Error(), "must-not-leak") {
			t.Fatalf("lost=%v err=%v", lost, err)
		}
		if errors.Is(err, errDown) != lost {
			t.Fatalf("lost=%v but err=%v", lost, err)
		}
	}
}

func TestApplyRoutesSendsTheWholeTable(t *testing.T) {
	status, body := http.StatusNoContent, ""
	c, helper := helperClient(t, func(call) (int, string) { return status, body })
	routes := []ports.ProviderRoute{{TicketHash: "hash", Provider: "codex", AuthID: "auth-1"}}
	if err := c.ApplyRoutes(ctx, routes, []string{"auth-1", "auth-2"}); err != nil {
		t.Fatal(err)
	}
	want := `{"auth_ids":["auth-1","auth-2"],"routes":[{"ticket_hash":"hash","provider":"codex","auth_id":"auth-1"}]}`
	if sent := helper.calls[0]; sent.line() != "PUT /ao/routes" || sent.Body != want {
		t.Fatalf("sent %s %s", sent.line(), sent.Body)
	}
	status, body = http.StatusConflict, `{"code":"SESSION_BUSY"}`
	if err := c.ApplyRoutes(ctx, nil, nil); !errors.Is(err, ports.ErrProviderAccountBusy) {
		t.Fatalf("busy err=%v", err)
	}
	status, body = http.StatusInternalServerError, `{}`
	if err := c.ApplyRoutes(ctx, routes, nil); err == nil || errors.Is(err, ports.ErrProviderAccountBusy) {
		t.Fatalf("failed push err=%v", err)
	}
}

func TestAccountModelsKeepsReasoningLevelsAndOffersOnlyChatModels(t *testing.T) {
	c, helper := helperClient(t, func(call) (int, string) {
		return http.StatusOK, `{"models":[
			{"id":"gpt-5.5","label":"GPT-5.5","provider":"openai","efforts":["low","medium","high"],"is_default":true},
			{"id":"gpt-image-2","label":"Image"},{"id":"codex-auto-review","label":"Review"},{"id":"plain","label":"Plain"}]}`
	})
	models, err := c.AccountModels(ctx, domain.ProviderAccount{Provider: "codex", AuthID: "auth-1"})
	want := []ports.AgentModelInfo{{ID: "gpt-5.5", Label: "GPT-5.5", Provider: "openai", Efforts: []string{"low", "medium", "high"}}, {ID: "plain", Label: "Plain"}}
	if err != nil || !reflect.DeepEqual(models, want) {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	if sent := helper.calls[0]; sent.line() != "POST /ao/account-models" || sent.Body != `{"auth_id":"auth-1","provider":"codex"}` {
		t.Fatalf("sent %s %s", sent.line(), sent.Body)
	}
}

func TestCredentialsAlsoListsAPIKeysByTheirIndex(t *testing.T) {
	c, _ := helperClient(t, func(request call) (int, string) {
		switch request.Path {
		case "/v0/management/codex-api-key":
			return http.StatusOK, `{"codex-api-key":[{"api-key":"k1","auth-index":"abc"},{"api-key":"no-index-yet"}]}`
		case "/v0/management/claude-api-key":
			return http.StatusInternalServerError, ``
		}
		return http.StatusOK, `{"files":[]}`
	})
	credentials, err := c.Credentials(ctx)
	if want := []ports.ProviderCredential{{Name: "config-index:codex:abc", Provider: "codex"}}; err != nil || !reflect.DeepEqual(credentials, want) {
		t.Fatalf("credentials=%+v err=%v", credentials, err)
	}
}

func TestCredentialsListsFileSignInsWithTheHelpersVerdict(t *testing.T) {
	c, _ := helperClient(t, func(call) (int, string) {
		return http.StatusOK, `{"files":[
			{"id":"ok","name":"ok.json","provider":"codex","source":"file","status":"active","modtime":"2030-01-02T03:04:05.5Z"},
			{"id":"revoked","name":"revoked.json","provider":"claude","source":"file","status":"error","status_message":"Unauthorized: token revoked"},
			{"id":"refused","name":"refused.json","provider":"codex","source":"file","status_message":"refresh failed: invalid_grant"},
			{"id":"off","name":"off.json","provider":"codex","source":"file","disabled":true},
			{"id":"limited","name":"limited.json","provider":"codex","source":"file","status":"error","status_message":"quota exceeded"},
			{"id":"memory","name":"gone.json","provider":"codex","source":"memory","status_message":"unauthorized"}]}`
	})
	credentials, err := c.Credentials(ctx)
	if err != nil || len(credentials) != 5 {
		t.Fatalf("credentials=%+v err=%v", credentials, err)
	}
	if first := credentials[0]; first != (ports.ProviderCredential{AuthID: "ok", Name: "ok.json", Provider: "codex", ModifiedAt: time.Date(2030, 1, 2, 3, 4, 5, 5e8, time.UTC)}) {
		t.Fatalf("first=%+v", first)
	}
	failed := map[string]bool{}
	for _, credential := range credentials {
		failed[credential.AuthID] = credential.Failed
	}
	// A rate limit is not a sign-in failure.
	want := map[string]bool{"ok": false, "revoked": true, "refused": true, "off": true, "limited": false}
	if !reflect.DeepEqual(failed, want) {
		t.Fatalf("failed=%v", failed)
	}
}

func TestDeleteCredential(t *testing.T) {
	keys := `{"claude-api-key":[{"auth-index":123},{"auth-index":"other"},{"auth-index":"stable","api-key":"k"}]}`
	for name, tc := range map[string]struct {
		ref, listing  string
		listed, final int
		want          []string
		wantQuery     string
		fails         bool
	}{
		"a file by its escaped name":        {ref: "my file&x=1.json", final: 200, want: []string{"DELETE /v8/management/credentials"}, wantQuery: "name=my+file%26x%3D1.json"},
		"a file already gone":               {ref: "gone.json", final: 404, want: []string{"DELETE /v8/management/credentials"}, wantQuery: "name=gone.json"},
		"a file the helper cannot delete":   {ref: "kept.json", final: 500, want: []string{"DELETE /v8/management/credentials"}, wantQuery: "name=kept.json", fails: true},
		"an API key at its current place":   {ref: "config-index:claude:stable", listing: keys, listed: 200, final: 200, want: []string{"GET /v0/management/claude-api-key", "DELETE /v0/management/claude-api-key"}, wantQuery: "index=2"},
		"an API key no longer held":         {ref: "config-index:claude:missing", listing: keys, listed: 200, want: []string{"GET /v0/management/claude-api-key"}},
		"an API key when the list fails":    {ref: "config-index:claude:stable", listing: `{}`, listed: 503, want: []string{"GET /v0/management/claude-api-key"}, fails: true},
		"an API key when no list came back": {ref: "config-index:claude:stable", listing: `{"codex-api-key":[]}`, listed: 200, want: []string{"GET /v0/management/claude-api-key"}, fails: true},
		"an API key when the list is odd":   {ref: "config-index:claude:stable", listing: `{"claude-api-key":{}}`, listed: 200, want: []string{"GET /v0/management/claude-api-key"}, fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			c, helper := helperClient(t, func(received call) (int, string) {
				if received.Method == http.MethodGet {
					return tc.listed, tc.listing
				}
				return tc.final, `{}`
			})
			err := c.DeleteCredential(ctx, tc.ref)
			if (err != nil) != tc.fails || !reflect.DeepEqual(helper.lines(), tc.want) {
				t.Fatalf("err=%v calls=%v", err, helper.lines())
			}
			if last := helper.calls[len(helper.calls)-1]; last.Method == http.MethodDelete && last.Query != tc.wantQuery {
				t.Fatalf("deleted %q, want %q", last.Query, tc.wantQuery)
			}
		})
	}
}
