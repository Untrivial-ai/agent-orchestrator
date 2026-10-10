package proxyhost

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func relayAddress(provider string) string { return "127.0.0.1:" + callbacks[provider][0] }

func relayRequest(t *testing.T, provider, method, path string, query url.Values) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+relayAddress(provider)+path+"?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(data)
}

func assertRelayClosed(t *testing.T, provider string) {
	t.Helper()
	listener, err := net.Listen("tcp", relayAddress(provider))
	if err != nil {
		t.Fatalf("callback port is still held: %v", err)
	}
	_ = listener.Close()
}

// browserHelper is a helper that hands out one sign-in link and reports status.
func browserHelper(status *string) func(call) (int, string) {
	return func(received call) (int, string) {
		switch received.Path {
		case "/v8/management/oauth/auth-url":
			return http.StatusOK, `{"status":"ok","state":"private-state","url":"https://provider.test/sign-in"}`
		case "/v8/management/oauth/status":
			return http.StatusOK, `{"status":"` + *status + `"}`
		case "/v8/management/oauth/session":
			return http.StatusInternalServerError, `{}`
		}
		return http.StatusOK, `{}`
	}
}

func TestBrowserLoginRelaysOnlyTheMatchingCallback(t *testing.T) {
	for provider, callback := range callbacks {
		t.Run(provider, func(t *testing.T) {
			status := "wait"
			c, helper := helperClient(t, browserHelper(&status))
			login, err := c.StartLogin(ctx, "attempt", ports.ProviderLoginRequest{Provider: provider})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.closeRelay("attempt") })
			want := ports.ProviderLogin{ID: "attempt", Provider: provider, Mode: "browser", State: "private-state", URL: "https://provider.test/sign-in", Status: "waiting"}
			if start := helper.calls[0]; login != want || start.line() != "GET /v8/management/oauth/auth-url" || start.Query != "provider="+provider || start.LoginID != "attempt" {
				t.Fatalf("login=%+v start=%+v", login, start)
			}
			for name, wrong := range map[string][3]string{
				"another path":  {http.MethodGet, "/other", "private-state"},
				"another state": {http.MethodGet, callback[1], "other-state"},
				"no state":      {http.MethodGet, callback[1], ""},
				"a post":        {http.MethodPost, callback[1], "private-state"},
			} {
				code, body := relayRequest(t, provider, wrong[0], wrong[1], url.Values{"state": {wrong[2]}, "code": {"secret-code"}})
				if code != http.StatusBadRequest || strings.Contains(body, "secret-code") || strings.Contains(body, "private-state") {
					t.Errorf("%s: status=%d body=%q", name, code, body)
				}
			}
			if len(helper.calls) != 1 {
				t.Fatalf("a rejected callback reached the helper: %v", helper.lines())
			}
			code, body := relayRequest(t, provider, http.MethodGet, callback[1], url.Values{"state": {"private-state"}, "code": {"secret-code"}})
			if code != http.StatusOK || !strings.Contains(body, "return to AO") || strings.Contains(body, "secret-code") || strings.Contains(body, "private-state") {
				t.Fatalf("status=%d body=%q", code, body)
			}
			forwarded := helper.calls[1]
			if forwarded.line() != "POST /v8/management/oauth/callback" || forwarded.Body != `{"code":"secret-code","error":"","provider":"`+provider+`","state":"private-state"}` {
				t.Fatalf("forwarded %s %s", forwarded.line(), forwarded.Body)
			}
			// The relay stays up while the provider is still deciding, and closes when it has.
			if got, err := c.LoginStatus(ctx, login); got != "waiting" || err != nil {
				t.Fatalf("status=%q err=%v", got, err)
			}
			if code, _ = relayRequest(t, provider, http.MethodGet, "/other", nil); code != http.StatusBadRequest {
				t.Fatal("the relay closed while the login was waiting")
			}
			status = "ok"
			if got, err := c.LoginStatus(ctx, login); got != "complete" || err != nil || helper.calls[len(helper.calls)-1].Query != "state=private-state" {
				t.Fatalf("status=%q err=%v", got, err)
			}
			assertRelayClosed(t, provider)
		})
	}
}

func TestBrowserLoginReservesItsPortFirstAndReleasesItOnFailure(t *testing.T) {
	answer := `{"state":"private-state","url":"https://provider.test/sign-in"}`
	answerStatus := http.StatusOK
	c, helper := helperClient(t, func(call) (int, string) { return answerStatus, answer })
	listener, err := net.Listen("tcp", relayAddress("codex"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.StartLogin(ctx, "blocked", ports.ProviderLoginRequest{Provider: "codex", Mode: "browser"}); !errors.Is(err, ports.ErrProviderLoginCallbackBusy) || len(helper.calls) != 0 {
		t.Fatalf("err=%v helper calls=%v", err, helper.lines())
	}
	_ = listener.Close()
	for name, failure := range map[string]struct {
		status int
		body   string
	}{
		"the helper refuses": {http.StatusServiceUnavailable, `{"token":"must-not-leak"}`},
		"no link":            {http.StatusOK, `{"state":"state-only"}`},
		"no state":           {http.StatusOK, `{"url":"https://provider.test/sign-in"}`},
		"junk":               {http.StatusOK, `{`},
		"connection lost":    {0, ``},
	} {
		answerStatus, answer = failure.status, failure.body
		_, err = c.StartLogin(ctx, "failed", ports.ProviderLoginRequest{Provider: "codex"})
		if _, registered := c.relays.Load("failed"); err == nil || strings.Contains(err.Error(), "must-not-leak") || registered {
			t.Fatalf("%s: err=%v registered=%v", name, err, registered)
		}
		assertRelayClosed(t, "codex")
	}
}

func TestBrowserLoginClosesItsRelayOnFailureAndCancel(t *testing.T) {
	status := "error"
	c, helper := helperClient(t, browserHelper(&status))
	request := ports.ProviderLoginRequest{Provider: "claude"}
	login, err := c.StartLogin(ctx, "refused", request)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.LoginStatus(ctx, login); got != "failed" || err != nil {
		t.Fatalf("status=%q err=%v", got, err)
	}
	assertRelayClosed(t, "claude")
	status = "surprise"
	if _, err = c.LoginStatus(ctx, login); err == nil {
		t.Fatal("an unknown status was passed on")
	}
	if login, err = c.StartLogin(ctx, "cancelled", request); err != nil {
		t.Fatal(err)
	}
	// The port is released even when the helper cannot cancel its side.
	if err = c.CancelLogin(ctx, login); err == nil {
		t.Fatal("a failed cancel was reported as done")
	}
	if last := helper.calls[len(helper.calls)-1]; last.line() != "DELETE /v8/management/oauth/session" || last.Query != "state=private-state" {
		t.Fatalf("cancel sent %s?%s", last.line(), last.Query)
	}
	assertRelayClosed(t, "claude")
}

func TestDeviceLoginRunsInsideTheHelper(t *testing.T) {
	c, helper := helperClient(t, func(received call) (int, string) {
		if received.Method == http.MethodDelete {
			return http.StatusNoContent, ``
		}
		return http.StatusOK, `{"id":"device-1","provider":"codex","mode":"device","status":"waiting","url":"https://provider.test/device","code":"ABCD-1234"}`
	})
	login, err := c.StartLogin(ctx, "device-1", ports.ProviderLoginRequest{Provider: "codex", Mode: "device"})
	want := ports.ProviderLogin{ID: "device-1", Provider: "codex", Mode: "device", Status: "waiting", URL: "https://provider.test/device", Code: "ABCD-1234"}
	if err != nil || login != want || helper.calls[0].Body != `{"id":"device-1"}` {
		t.Fatalf("login=%+v err=%v", login, err)
	}
	if status, err := c.LoginStatus(ctx, login); status != "waiting" || err != nil {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if err = c.CancelLogin(ctx, login); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"POST /ao/login/device/start", "GET /ao/login/status", "DELETE /ao/login/status"}
	if !reflect.DeepEqual(helper.lines(), wantCalls) || helper.calls[1].Query != "id=device-1" || helper.calls[2].Query != "id=device-1" {
		t.Fatalf("calls=%v", helper.lines())
	}
}

func TestCredentialFileLoginUploadsOnlyAFileOfTheProvider(t *testing.T) {
	result := http.StatusNotFound
	c, helper := helperClient(t, func(received call) (int, string) {
		if strings.HasPrefix(received.Path, "/ao/login-result/") {
			return result, `{"provider":"codex","email":"person@example.test","kind":"imported","credential_ref":"ao-file-1.json","auth_id":"auth-1"}`
		}
		return http.StatusOK, `{}`
	})
	for _, bad := range []string{``, `{`, `[]`, `"codex"`, `{"type":"claude"}`, `{"type":7}`, `{"access_token":"a"}`} {
		if _, err := c.StartLogin(ctx, "file-1", ports.ProviderLoginRequest{Provider: "codex", Mode: "import", CredentialJSON: bad}); !errors.Is(err, ports.ErrProviderAccountIncompatible) {
			t.Fatalf("%q: err=%v", bad, err)
		}
	}
	if len(helper.calls) != 0 {
		t.Fatalf("a bad file reached the helper: %v", helper.lines())
	}
	file := `{"type":"codex","access_token":"a"}`
	login, err := c.StartLogin(ctx, "file-1", ports.ProviderLoginRequest{Provider: "codex", Mode: "import", CredentialJSON: file})
	if err != nil || login != (ports.ProviderLogin{ID: "file-1", Provider: "codex", Mode: "import", Status: "waiting"}) {
		t.Fatalf("login=%+v err=%v", login, err)
	}
	if upload := helper.calls[0]; upload.line() != "POST /v8/management/credentials" || upload.Query != "name=ao-file-1.json" || upload.Body != file || upload.LoginID != "file-1" {
		t.Fatalf("upload=%+v", upload)
	}
	// Not loaded yet is waiting; a helper that cannot say is an error, not a failed login.
	for want, status := range map[string]int{"waiting": http.StatusNotFound, "complete": http.StatusOK, "": http.StatusBadGateway} {
		result = status
		if got, err := c.LoginStatus(ctx, login); got != want || (err != nil) != (want == "") {
			t.Fatalf("result %d: status=%q err=%v", status, got, err)
		}
	}
	result = http.StatusOK
	verified, err := c.LoginResult(ctx, "file-1")
	if err != nil || verified != (ports.VerifiedProviderLogin{Provider: "codex", Email: "person@example.test", Kind: "imported", CredentialRef: "ao-file-1.json", AuthID: "auth-1"}) {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	if err = c.CancelLogin(ctx, login); err != nil {
		t.Fatal(err)
	}
	if last := helper.calls[len(helper.calls)-1]; last.line() != "DELETE /v8/management/credentials" || last.Query != "name=ao-file-1.json" {
		t.Fatalf("cancel sent %s?%s", last.line(), last.Query)
	}
}

func TestAPIKeyLoginKeepsTheKeysHeldAndTakesBackAnUntaggedOne(t *testing.T) {
	held := `{"claude-api-key":[{"api-key":"existing","base-url":"https://gateway.example/","proxy-url":"socks5://kept"}]}`
	tagged := http.StatusOK
	c, helper := helperClient(t, func(received call) (int, string) {
		switch {
		case received.Method == http.MethodGet:
			return http.StatusOK, held
		case received.Path == "/ao/tag-api-key":
			return tagged, `{"auth_id":"key-auth"}`
		}
		return http.StatusOK, `{}`
	})
	request := ports.ProviderLoginRequest{Provider: "claude", Mode: "api_key", APIKey: "sk-new", BaseURL: " https://api.anthropic.com/ ", Label: "Work key"}
	login, err := c.StartLogin(ctx, "key-1", request)
	if err != nil || login != (ports.ProviderLogin{ID: "key-1", Provider: "claude", Mode: "api_key", Status: "waiting"}) {
		t.Fatalf("login=%+v err=%v", login, err)
	}
	wantCalls := []string{"GET /v0/management/claude-api-key", "PUT /v0/management/claude-api-key", "POST /ao/tag-api-key"}
	wantKeys := `[{"api-key":"existing","base-url":"https://gateway.example/","proxy-url":"socks5://kept"},{"api-key":"sk-new","base-url":"https://api.anthropic.com"}]`
	wantTag := `{"api_key":"sk-new","base_url":"https://api.anthropic.com","id":"key-1","label":"Work key","provider":"claude"}`
	if put, tag := helper.calls[1], helper.calls[2]; !reflect.DeepEqual(helper.lines(), wantCalls) || put.Body != wantKeys || put.LoginID != "key-1" || tag.Body != wantTag {
		t.Fatalf("calls=%v put=%s tag=%s", helper.lines(), put.Body, tag.Body)
	}
	if err = c.CancelLogin(ctx, login); err != nil || len(helper.calls) != 3 {
		t.Fatalf("cancel err=%v calls=%v", err, helper.lines())
	}
	helper.calls = nil
	// A key the helper already holds is a conflict, and an address that is not one never gets there.
	if _, err = c.StartLogin(ctx, "key-2", ports.ProviderLoginRequest{Provider: "claude", Mode: "api_key", APIKey: "existing", BaseURL: "https://gateway.example"}); !errors.Is(err, ports.ErrProviderAccountConflict) || len(helper.calls) != 1 {
		t.Fatalf("duplicate err=%v calls=%v", err, helper.lines())
	}
	helper.calls = nil
	for _, base := range []string{"", "gateway.example", "ftp://gateway.example", "https://user:pass@gateway.example", "https://gateway.example?key=1", "https://gateway.example#part"} {
		request.BaseURL = base
		if _, err = c.StartLogin(ctx, "key-3", request); err == nil || len(helper.calls) != 0 {
			t.Fatalf("%q: err=%v calls=%v", base, err, helper.lines())
		}
	}
	tagged, request.BaseURL = http.StatusNotFound, "https://api.anthropic.com"
	if _, err = c.StartLogin(ctx, "key-4", request); !notFound(err) {
		t.Fatalf("untagged err=%v", err)
	}
	if last := helper.calls[len(helper.calls)-1]; last.line() != "DELETE /v0/management/claude-api-key" || last.Query != "api-key=sk-new&base-url=https%3A%2F%2Fapi.anthropic.com" {
		t.Fatalf("rollback sent %s?%s", last.line(), last.Query)
	}
}
