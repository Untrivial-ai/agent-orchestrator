package controllers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type accountAdminFake struct {
	accounts []domain.ProviderAccountView
	route    domain.ProviderSessionRoute
	managed  bool
	login    ports.ProviderLogin
	outcome  string
	err      error
	calls    []string
}

func (f *accountAdminFake) Accounts(_ context.Context, usage, refresh bool) ([]domain.ProviderAccountView, error) {
	f.calls = append(f.calls, fmt.Sprintf("accounts usage=%t refresh=%t", usage, refresh))
	return f.accounts, f.err
}

func (f *accountAdminFake) Act(_ context.Context, id string, action ports.ProviderAccountAction) (string, error) {
	f.calls = append(f.calls, fmt.Sprintf("act %s %+v", id, action))
	return f.outcome, f.err
}

func (f *accountAdminFake) SessionAccount(_ context.Context, id domain.SessionID) (domain.ProviderSessionRoute, bool, error) {
	f.calls = append(f.calls, "session "+string(id))
	return f.route, f.managed, f.err
}

func (f *accountAdminFake) StartLogin(_ context.Context, request ports.ProviderLoginRequest) (ports.ProviderLogin, error) {
	f.calls = append(f.calls, fmt.Sprintf("login %+v", request))
	return f.login, f.err
}

func (f *accountAdminFake) LoginStatus(_ context.Context, id string) (ports.ProviderLogin, error) {
	f.calls = append(f.calls, "status "+id)
	return f.login, f.err
}

func (f *accountAdminFake) CancelLogin(_ context.Context, id string) error {
	f.calls = append(f.calls, "cancel "+id)
	return f.err
}

func accountHTTP(t *testing.T, f *accountAdminFake, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	(&controllers.ProviderAccountsController{Svc: f}).Register(r)
	out := httptest.NewRecorder()
	r.ServeHTTP(out, httptest.NewRequest(method, path, strings.NewReader(body)))
	return out
}

func TestProviderAccountsListReturnsTheServiceViewAndForwardsItsFlags(t *testing.T) {
	credits := int64(2)
	f := &accountAdminFake{accounts: []domain.ProviderAccountView{{
		ID: "a", Provider: "codex", DisplayName: "Cedar Codex", Email: "a@example.test", Kind: "oauth", Global: true, SignedIn: true, Primary: true,
		Sessions: []string{"s1"}, Usage: &domain.ProviderAccountUsage{Status: "available", Plan: "pro", ResetCredits: &credits},
	}}}
	for query, want := range map[string]string{
		"":                                 "accounts usage=true refresh=false",
		"?includeUsage=false":              "accounts usage=false refresh=false",
		"?includeUsage=true&refresh=true":  "accounts usage=true refresh=true",
		"?includeUsage=false&refresh=true": "accounts usage=false refresh=true",
	} {
		f.calls = nil
		out := accountHTTP(t, f, "GET", "/provider-accounts"+query, "")
		if out.Code != 200 || !reflect.DeepEqual(f.calls, []string{want}) {
			t.Fatalf("%q: status=%d calls=%v body=%s", query, out.Code, f.calls, out.Body)
		}
	}
	var got map[string][]map[string]any
	out := accountHTTP(t, f, "GET", "/provider-accounts", "")
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	account := got["accounts"][0]
	if account["primary"] != true || account["signedIn"] != true || account["global"] != true || account["displayName"] != "Cedar Codex" ||
		!reflect.DeepEqual(account["sessions"], []any{"s1"}) || account["usage"].(map[string]any)["resetCredits"] != float64(2) {
		t.Fatalf("account=%v", account)
	}
	for _, private := range []string{"credential", "auth_id", "authId", "ticket", "resetOutcome"} {
		if strings.Contains(out.Body.String(), private) {
			t.Errorf("list response contains %q: %s", private, out.Body)
		}
	}
}

func TestProviderAccountActionsReachTheServiceExactlyAndAnswerWithTheList(t *testing.T) {
	f := &accountAdminFake{accounts: []domain.ProviderAccountView{{ID: "a", Provider: "codex", Sessions: []string{}}}, outcome: domain.ProviderResetDone}
	for body, want := range map[string]ports.ProviderAccountAction{
		`{"action":"primary"}`:                                {Action: "primary"},
		`{"action":"sign-out","replacementPrimaryId":"b"}`:    {Action: "sign-out", ReplacementPrimaryID: "b"},
		`{"action":"remove","replacementPrimaryId":"b"}`:      {Action: "remove", ReplacementPrimaryID: "b"},
		`{"action":"rename","displayName":"  Work  "}`:        {Action: "rename", DisplayName: "  Work  "},
		`{"action":"resume"}`:                                 {Action: "resume"},
		`{"action":"refresh-sign-in"}`:                        {Action: "refresh-sign-in"},
		`{"action":"reset"}`:                                  {Action: "reset"},
		`{"action":"assign-session","sessionId":"session-1"}`: {Action: "assign-session", SessionID: "session-1"},
	} {
		f.calls = nil
		out := accountHTTP(t, f, "POST", "/provider-accounts/account-1/actions", body)
		wantCalls := []string{fmt.Sprintf("act account-1 %+v", want), "accounts usage=false refresh=false"}
		if out.Code != 200 || !reflect.DeepEqual(f.calls, wantCalls) {
			t.Fatalf("%s: status=%d calls=%v body=%s", body, out.Code, f.calls, out.Body)
		}
		if !strings.Contains(out.Body.String(), `"accounts":[{"id":"a"`) || !strings.Contains(out.Body.String(), `"resetOutcome":"reset"`) {
			t.Fatalf("%s: body=%s", body, out.Body)
		}
	}
	for _, body := range []string{`{`, `{"action":"primary","moveExisting":true}`} {
		f.calls = nil
		out := accountHTTP(t, f, "POST", "/provider-accounts/account-1/actions", body)
		if out.Code != 400 || !strings.Contains(out.Body.String(), "INVALID_JSON") || len(f.calls) != 0 {
			t.Fatalf("%s: status=%d calls=%v body=%s", body, out.Code, f.calls, out.Body)
		}
	}
}

func TestProviderAccountErrorsKeepTheirStatusCodeAndMessage(t *testing.T) {
	for err, status := range map[*apierr.Error]int{
		ports.ErrProviderAccountUnknown:           404,
		ports.ErrProviderAccountConflict:          409,
		ports.ErrProviderAccountBusy:              409,
		ports.ErrProviderAccountIncompatible:      400,
		ports.ErrProviderAccountNameInvalid:       400,
		ports.ErrProviderAccountActionUnavailable: 409,
		ports.ErrProviderPrimaryRequired:          409,
		ports.ErrProviderLoginRequired:            409,
		ports.ErrProviderLoginUnknown:             404,
		ports.ErrProviderLoginCallbackBusy:        409,
	} {
		f := &accountAdminFake{err: fmt.Errorf("private detail: %w", err)}
		for _, request := range [][3]string{
			{"GET", "/provider-accounts", ""},
			{"POST", "/provider-accounts/a/actions", `{"action":"remove"}`},
			{"POST", "/provider-accounts/login", `{"provider":"codex"}`},
			{"GET", "/provider-accounts/login/l", ""},
			{"DELETE", "/provider-accounts/login/l", ""},
			{"GET", "/provider-accounts/sessions/s", ""},
		} {
			out := accountHTTP(t, f, request[0], request[1], request[2])
			var body struct{ Code, Message string }
			_ = json.Unmarshal(out.Body.Bytes(), &body)
			if out.Code != status || body.Code != err.Code || body.Message != err.Message || strings.Contains(out.Body.String(), "private detail") {
				t.Errorf("%s %s with %s: status=%d body=%s", request[0], request[1], err.Code, out.Code, out.Body)
			}
		}
	}
}

func TestProviderAccountLoginStartPollAndCancelNeverExposeOAuthState(t *testing.T) {
	f := &accountAdminFake{login: ports.ProviderLogin{ID: "login-1", Provider: "codex", Mode: "device", State: "PRIVATE-STATE", URL: "https://example.test/device", Code: "ABCD-1234", Status: "waiting", AccountID: "a"}}
	const start = `{"provider":"codex","accountId":"a","mode":"api_key","apiKey":"key","baseUrl":"https://example.test","label":"Work","credentialJson":"{}"}`
	want := `{"id":"login-1","provider":"codex","mode":"device","url":"https://example.test/device","code":"ABCD-1234","status":"waiting","accountId":"a"}`
	for _, request := range [][3]string{{"POST", "/provider-accounts/login", start}, {"GET", "/provider-accounts/login/login-1", ""}} {
		out := accountHTTP(t, f, request[0], request[1], request[2])
		if out.Code != 200 || strings.TrimSpace(out.Body.String()) != want {
			t.Fatalf("%s: status=%d body=%s", request[0], out.Code, out.Body)
		}
	}
	if out := accountHTTP(t, f, "DELETE", "/provider-accounts/login/login-1", ""); out.Code != 204 || out.Body.Len() != 0 {
		t.Fatalf("cancel: status=%d body=%s", out.Code, out.Body)
	}
	request := ports.ProviderLoginRequest{Provider: "codex", AccountID: "a", Mode: "api_key", APIKey: "key", BaseURL: "https://example.test", Label: "Work", CredentialJSON: "{}"}
	if want := []string{fmt.Sprintf("login %+v", request), "status login-1", "cancel login-1"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls=%v", f.calls)
	}
	if out := accountHTTP(t, f, "POST", "/provider-accounts/login", `{"provider":"codex","state":"x"}`); out.Code != 400 || len(f.calls) != 3 {
		t.Fatalf("unknown field: status=%d calls=%v", out.Code, f.calls)
	}
}

func TestSessionProviderAccountStates(t *testing.T) {
	for want, f := range map[string]*accountAdminFake{
		`{"managed":false,"accountId":""}`: {},
		`{"managed":true,"accountId":"a"}`: {managed: true, route: domain.ProviderSessionRoute{SessionID: "s", Provider: "codex", AccountID: "a"}},
		`{"managed":true,"accountId":""}`:  {managed: true, route: domain.ProviderSessionRoute{SessionID: "s", Provider: "claude"}},
	} {
		out := accountHTTP(t, f, "GET", "/provider-accounts/sessions/s", "")
		if out.Code != 200 || strings.TrimSpace(out.Body.String()) != want || !reflect.DeepEqual(f.calls, []string{"session s"}) {
			t.Fatalf("want %s: status=%d body=%s calls=%v", want, out.Code, out.Body, f.calls)
		}
	}
}

func TestSpawnAndDelegateForwardTheChosenAccount(t *testing.T) {
	svc := newFakeSessionService()
	srv := newSessionTestServer(t, svc)
	if body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions", `{"projectId":"ao","harness":"codex","prompt":"fix","providerAccountId":"account-a"}`); status != 201 || svc.lastSpawn.AccountID != "account-a" {
		t.Fatalf("spawn=%d account=%q body=%s", status, svc.lastSpawn.AccountID, body)
	}
	if body, status, _ := doRequest(t, srv, "POST", "/api/v1/orchestrators/delegate", `{"projectId":"ao","brief":"Fix it","providerAccountId":"account-b"}`); status != 202 || svc.delegationInput.AccountID != "account-b" {
		t.Fatalf("delegate=%d account=%q body=%s", status, svc.delegationInput.AccountID, body)
	}
}
