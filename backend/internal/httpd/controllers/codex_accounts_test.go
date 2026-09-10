package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

type fakeCodexAccounts struct {
	result              agentsvc.CodexAccounts
	ensureIDs           []string
	includeUsage        bool
	resetAccountID      string
	resetIdempotencyKey string
	events              chan agentsvc.CodexAccounts
	loginStart          agentsvc.CodexAccountLoginTerminalStart
	verifiedOperation   string
	cancelledOperation  string
	reauthenticatedID   string
	loggedOutID         string
	deletedID           string
	switchConfig        ports.CodexAccountSwitchConfig
	switchResult        domain.CodexAccountSwitch
	switchErr           error
}

func (f *fakeCodexAccounts) CachedCodexAccounts(context.Context) (agentsvc.CodexAccounts, error) {
	return f.result, nil
}
func (f *fakeCodexAccounts) EnsureCodexAccounts(_ context.Context, ids []string, includeUsage bool) (agentsvc.CodexAccounts, error) {
	f.ensureIDs, f.includeUsage = ids, includeUsage
	return f.result, nil
}
func (f *fakeCodexAccounts) ConsumeCodexAccountResetCredit(_ context.Context, accountID, idempotencyKey string) (agentsvc.CodexAccounts, error) {
	f.resetAccountID, f.resetIdempotencyKey = accountID, idempotencyKey
	return f.result, nil
}
func (f *fakeCodexAccounts) SubscribeCodexAccounts(ctx context.Context) (<-chan agentsvc.CodexAccounts, error) {
	if f.events != nil {
		return f.events, nil
	}
	ch := make(chan agentsvc.CodexAccounts)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}
func (f *fakeCodexAccounts) OpenCodexAccountLoginTerminal(context.Context) (agentsvc.CodexAccountLoginTerminalStart, error) {
	return f.loginStart, nil
}
func (f *fakeCodexAccounts) OpenCodexAccountReauthenticationTerminal(_ context.Context, id string) (agentsvc.CodexAccountLoginTerminalStart, error) {
	f.reauthenticatedID = id
	return f.loginStart, nil
}
func (f *fakeCodexAccounts) LogoutCodexAccount(_ context.Context, id string) (agentsvc.CodexAccounts, error) {
	f.loggedOutID = id
	return f.result, nil
}
func (f *fakeCodexAccounts) DeleteCodexAccount(_ context.Context, id string) (agentsvc.CodexAccounts, error) {
	f.deletedID = id
	return f.result, nil
}
func (f *fakeCodexAccounts) VerifyCodexAccountLogin(_ context.Context, id string) (domain.CodexAccountLoginOperation, error) {
	f.verifiedOperation = id
	return domain.CodexAccountLoginOperation{OperationID: id, Status: domain.CodexAccountLoginUnverified, ReasonCode: domain.CodexAccountLoginReasonUnverified, Reason: "unverified"}, nil
}
func (f *fakeCodexAccounts) CancelCodexAccountLogin(_ context.Context, id string) (domain.CodexAccountLoginOperation, error) {
	f.cancelledOperation = id
	return domain.CodexAccountLoginOperation{OperationID: id, Status: domain.CodexAccountLoginCancelled, ReasonCode: domain.CodexAccountLoginReasonCancelled, Reason: "cancelled"}, nil
}
func (f *fakeCodexAccounts) StartCodexAccountSwitch(_ context.Context, cfg ports.CodexAccountSwitchConfig) (domain.CodexAccountSwitch, error) {
	f.switchConfig = cfg
	return f.switchResult, f.switchErr
}
func (f *fakeCodexAccounts) RecoverCodexAccountSwitch(context.Context, string) (domain.CodexAccountSwitch, error) {
	return f.switchResult, nil
}

func codexAccountsFixture() agentsvc.CodexAccounts {
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported, ReasonCode: "supported", Reason: "available"}
	remaining := 95.0
	used := 5.0
	email := "person@example.com"
	plan := "pro"
	bucketName := "Code review"
	windowMinutes := int64(300)
	resetsAt := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	return agentsvc.CodexAccounts{
		ActiveAccountID: "72d4db6e-da2c-414c-a6a9-fdbd09a006b6",
		AccountRevision: 3,
		Accounts: []domain.CodexAccountSnapshot{{
			ID: "72d4db6e-da2c-414c-a6a9-fdbd09a006b6", Label: "person@example.com", Source: domain.CodexAccountSourceManaged,
			Status: domain.CodexAccountStatusValid, ReasonCode: domain.CodexAccountReasonValid, Reason: "available", Active: true,
			Authentication: domain.AgentAuthenticationObservation{State: domain.AgentAuthenticationAuthorized, Freshness: domain.AgentReadinessFresh, ReasonCode: domain.AgentReadinessReasonAuthorized, Reason: "signed in"},
			AuthMethod:     domain.CodexAuthMethodChatGPT,
			AccountEmail:   &email,
			Capacity: domain.CodexCapacitySnapshot{
				State: domain.CodexCapacityAvailable, Freshness: domain.AgentReadinessFresh, Plan: &plan,
				UsedPercent: &used, RemainingPercent: &remaining, ResetsAt: &resetsAt,
				ReasonCode: domain.CodexCapacityReasonAvailable, Reason: "available",
				AdditionalBuckets: []domain.CodexCapacityBucket{{
					LimitID: "provider-limit-secret", DisplayName: &bucketName, Reached: domain.CodexCapacityNotReached,
					Primary: &domain.CodexCapacityWindow{UsedPercent: 12, WindowDurationMinutes: &windowMinutes, ResetsAt: &resetsAt},
				}},
				ResetCredits: &domain.CodexResetCreditsSummary{AvailableCount: 2, NearestExpiresAt: &resetsAt},
			},
		}},
		Capabilities: domain.CodexAccountCapabilities{AccountRead: supported, NativeLogin: supported, CapacityRead: supported, UsageRead: supported, ResetCreditConsume: supported, ThreadResume: supported, AccountManagement: supported, GlobalSwitch: supported},
	}
}

func newCodexAccountServer(t *testing.T, fake *fakeCodexAccounts) *httptest.Server {
	t.Helper()
	return httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.DiscardHandler), nil, httpd.APIDeps{CodexAccounts: fake}, httpd.ControlDeps{}))
}

func TestCodexAccountRoutesExposeSafeCachedAndEnsureShapes(t *testing.T) {
	fixture := codexAccountsFixture()
	fixture.CurrentSwitch = &domain.CodexAccountSwitch{
		ID: "switch-1", SourceAccountID: "source-account", TargetAccountID: "target-account",
		Phase: domain.CodexAccountSwitchRestartingSessions,
		Sessions: []domain.CodexAccountSwitchSession{{
			SessionID: "session-1", InterfaceMode: domain.SessionModeTUI, WasRunning: true,
			RestartState: "in_progress", ErrorCode: "restart_in_progress:private-generation-id",
		}},
	}
	fake := &fakeCodexAccounts{result: fixture}
	srv := newCodexAccountServer(t, fake)
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/codex/accounts", "")
	text := string(body)
	if status != http.StatusOK || !strings.Contains(text, `"activeAccountId"`) || !strings.Contains(text, `"accountEmail":"person@example.com"`) || !strings.Contains(text, `"plan":"pro"`) || !strings.Contains(text, `"remainingPercent":95`) || !strings.Contains(text, `"displayName":"Code review"`) {
		t.Fatalf("GET status=%d body=%s", status, body)
	}
	for _, forbidden := range []string{"provider-limit-secret", "private-generation-id", "limitId", "auth.json", "credential-home", "codexHome", "nativeSessionId", "generationId", "idempotencyKey"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("GET leaked %q: %s", forbidden, body)
		}
	}
	for _, unusedCapability := range []string{"accountRead", "capacityRead", "usageRead", "threadResume", "accountManagement"} {
		if strings.Contains(text, `"`+unusedCapability+`"`) {
			t.Fatalf("GET exposed unused capability %q: %s", unusedCapability, body)
		}
	}
	for _, usedCapability := range []string{"nativeLogin", "resetCreditConsume", "globalSwitch"} {
		if !strings.Contains(text, `"`+usedCapability+`"`) {
			t.Fatalf("GET omitted UI capability %q: %s", usedCapability, body)
		}
	}
	var response controllers.CodexAccountsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode redacted response: %v", err)
	}
	if len(response.Accounts) != 1 {
		t.Fatalf("decoded accounts = %#v", response.Accounts)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/accounts/ensure", `{"accountIds":["a","a"],"includeUsage":true}`)
	if status != http.StatusOK || len(fake.ensureIDs) != 2 || !fake.includeUsage {
		t.Fatalf("ensure status=%d ids=%#v includeUsage=%v body=%s", status, fake.ensureIDs, fake.includeUsage, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/accounts/ensure", `{"accountIds":[],"force":true}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"INVALID_JSON"`) {
		t.Fatalf("strict ensure status=%d body=%s", status, body)
	}
}

func TestCodexAccountLoginTerminalAndVerificationRoutesExposeNoCommandOrPath(t *testing.T) {
	fake := &fakeCodexAccounts{result: codexAccountsFixture(), loginStart: agentsvc.CodexAccountLoginTerminalStart{
		Operation:     domain.CodexAccountLoginOperation{OperationID: "op-1", Status: domain.CodexAccountLoginPending, ReasonCode: domain.CodexAccountLoginReasonPending, Reason: "waiting", ExpiresAt: time.Now().Add(time.Minute)},
		ShellTerminal: shellterm.ShellTerminal{HandleID: "shellterm-login-1", WorkingDir: "/private/secret", Title: "Add Codex account", CreatedAt: time.Now()},
	}}
	srv := newCodexAccountServer(t, fake)
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/accounts/login-terminal", "")
	text := string(body)
	if status != http.StatusAccepted || !strings.Contains(text, `"operationId":"op-1"`) || !strings.Contains(text, `"handleId":"shellterm-login-1"`) {
		t.Fatalf("login status=%d body=%s", status, body)
	}
	for _, forbidden := range []string{"workingDir", "/private/secret", "argv", "CODEX_HOME"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("login response leaked %q: %s", forbidden, body)
		}
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/accounts/login-terminal", `{}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"INVALID_REQUEST_BODY"`) {
		t.Fatalf("body rejection status=%d body=%s", status, body)
	}
	_, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/accounts/login-operations/op-1/verify", "")
	if status != http.StatusOK || fake.verifiedOperation != "op-1" {
		t.Fatalf("verify status=%d id=%q", status, fake.verifiedOperation)
	}
}

func TestCodexAccountCachedAndEventResponsesExposeSafeActiveLogin(t *testing.T) {
	result := codexAccountsFixture()
	result.ActiveLogin = &agentsvc.CodexActiveLogin{
		OperationID: "login-op-safe", AccountID: result.ActiveAccountID,
		Status: domain.CodexAccountLoginPending, ReasonCode: domain.CodexAccountLoginReasonPending,
		Reason: "waiting", ExpiresAt: time.Date(2026, time.September, 2, 12, 15, 0, 0, time.UTC),
		ShellTerminal: agentsvc.CodexLoginTerminalDisplay{
			HandleID: "shellterm-login-safe", Title: "Sign in to Codex account",
			CreatedAt: time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC),
		},
	}
	events := make(chan agentsvc.CodexAccounts, 1)
	events <- result
	close(events)
	fake := &fakeCodexAccounts{result: result, events: events}
	srv := newCodexAccountServer(t, fake)
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/codex/accounts", "")
	if status != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", status, body)
	}
	assertSafeActiveLogin := func(t *testing.T, text string) {
		t.Helper()
		for _, want := range []string{`"activeLogin"`, `"operationId":"login-op-safe"`, `"accountId":"` + result.ActiveAccountID + `"`, `"handleId":"shellterm-login-safe"`, `"title":"Sign in to Codex account"`} {
			if !strings.Contains(text, want) {
				t.Fatalf("active login missing %s: %s", want, text)
			}
		}
		for _, forbidden := range []string{"pendingDir", "workingDir", "CODEX_HOME", "argv", "env", "credential"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("active login leaked %q: %s", forbidden, text)
			}
		}
	}
	assertSafeActiveLogin(t, string(body))

	response, err := http.Get(srv.URL + "/api/v1/agents/codex/accounts/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	eventBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	assertSafeActiveLogin(t, string(eventBody))
}

func TestCodexAccountReauthenticationAndLogoutRoutesTargetAnExistingAccount(t *testing.T) {
	accountID := "72d4db6e-da2c-414c-a6a9-fdbd09a006b6"
	fake := &fakeCodexAccounts{result: codexAccountsFixture(), loginStart: agentsvc.CodexAccountLoginTerminalStart{
		Operation: domain.CodexAccountLoginOperation{
			OperationID: "op-reauth", AccountID: accountID, Status: domain.CodexAccountLoginPending,
			ReasonCode: domain.CodexAccountLoginReasonPending, Reason: "waiting", ExpiresAt: time.Now().Add(time.Minute),
		},
		ShellTerminal: shellterm.ShellTerminal{HandleID: "shellterm-reauth", WorkingDir: "/private/secret", Title: "Sign in to Codex account", CreatedAt: time.Now()},
	}}
	srv := newCodexAccountServer(t, fake)
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/accounts/"+accountID+"/login-terminal", "")
	if status != http.StatusAccepted || fake.reauthenticatedID != accountID || !strings.Contains(string(body), `"accountId":"`+accountID+`"`) || strings.Contains(string(body), "/private/secret") {
		t.Fatalf("reauthentication status=%d id=%q body=%s", status, fake.reauthenticatedID, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/accounts/"+accountID+"/logout", "")
	if status != http.StatusOK || fake.loggedOutID != accountID || !strings.Contains(string(body), `"accountRevision":3`) {
		t.Fatalf("logout status=%d id=%q body=%s", status, fake.loggedOutID, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/accounts/"+accountID+"/logout", `{}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"INVALID_REQUEST_BODY"`) {
		t.Fatalf("logout body rejection status=%d body=%s", status, body)
	}
}

func TestCodexAccountDeleteRouteTargetsSignedOutAccount(t *testing.T) {
	accountID := "72d4db6e-da2c-414c-a6a9-fdbd09a006b6"
	fake := &fakeCodexAccounts{result: codexAccountsFixture()}
	srv := newCodexAccountServer(t, fake)
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodDelete, "/api/v1/agents/codex/accounts/"+accountID, "")
	if status != http.StatusOK || fake.deletedID != accountID || !strings.Contains(string(body), `"accountRevision":3`) {
		t.Fatalf("delete status=%d id=%q body=%s", status, fake.deletedID, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodDelete, "/api/v1/agents/codex/accounts/"+accountID, `{}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"INVALID_REQUEST_BODY"`) {
		t.Fatalf("delete body rejection status=%d body=%s", status, body)
	}
}

func TestCodexAccountResetCreditRouteRequiresIdempotencyAndReturnsRefreshedAccounts(t *testing.T) {
	fake := &fakeCodexAccounts{result: codexAccountsFixture()}
	srv := newCodexAccountServer(t, fake)
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/accounts/account-1/reset-credit/consume", `{"idempotencyKey":""}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"IDEMPOTENCY_KEY_REQUIRED"`) {
		t.Fatalf("missing key status=%d body=%s", status, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/accounts/account-1/reset-credit/consume", `{"idempotencyKey":"reset-request-1"}`)
	if status != http.StatusOK || fake.resetAccountID != "account-1" || fake.resetIdempotencyKey != "reset-request-1" || !strings.Contains(string(body), `"accountRevision":3`) {
		t.Fatalf("reset status=%d account=%q key=%q body=%s", status, fake.resetAccountID, fake.resetIdempotencyKey, body)
	}
}

func TestCodexAccountSwitchRequiresIdempotencyAndRedactsPrivateIdentity(t *testing.T) {
	fake := &fakeCodexAccounts{result: codexAccountsFixture(), switchResult: domain.CodexAccountSwitch{
		ID: "switch-1", SourceAccountID: "source", TargetAccountID: "target", Phase: domain.CodexAccountSwitchRequested,
		Sessions: []domain.CodexAccountSwitchSession{{
			SessionID: "ao-1", InterfaceMode: domain.SessionModeChat, WasRunning: true, StopState: "pending", RestartState: "pending",
			NativeSessionID: "native-secret", SourceHandleID: "source-handle-secret", SourceGeneration: "generation-secret",
			ReviewerSourceHandleID: "reviewer-handle-secret", ReviewerNativeSessionID: "reviewer-native-secret",
		}},
		IdempotencyKey: "private-key", RequestFingerprint: "private-fingerprint", ExpectedAccountRevision: 3,
	}}
	srv := newCodexAccountServer(t, fake)
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/account-switches", `{"targetAccountId":"target","expectedAccountRevision":3,"idempotencyKey":""}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"IDEMPOTENCY_KEY_REQUIRED"`) {
		t.Fatalf("missing key status=%d body=%s", status, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/account-switches", `{"targetAccountId":"target","expectedAccountRevision":3,"idempotencyKey":"request-key"}`)
	text := string(body)
	if status != http.StatusAccepted || fake.switchConfig.IdempotencyKey != "request-key" || !strings.Contains(text, `"sessionId":"ao-1"`) {
		t.Fatalf("switch status=%d config=%#v body=%s", status, fake.switchConfig, body)
	}
	for _, forbidden := range []string{"native-secret", "source-handle-secret", "generation-secret", "reviewer-handle-secret", "reviewer-native-secret", "private-key", "private-fingerprint"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("switch leaked %q: %s", forbidden, body)
		}
	}
}

func TestCodexAccountSwitchWithoutReconciledSourceReturnsTypedConflict(t *testing.T) {
	fake := &fakeCodexAccounts{
		result:    codexAccountsFixture(),
		switchErr: ports.ErrCodexActiveAccountUnavailable,
	}
	srv := newCodexAccountServer(t, fake)
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/account-switches", `{"targetAccountId":"target","expectedAccountRevision":3,"idempotencyKey":"request-key"}`)
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"CODEX_ACCOUNT_AUTH_UNVERIFIED"`) {
		t.Fatalf("unreconciled switch status=%d body=%s", status, body)
	}
}

func TestCodexAccountSwitchReadAndCancelRoutesAreNotRegistered(t *testing.T) {
	fake := &fakeCodexAccounts{result: codexAccountsFixture()}
	srv := newCodexAccountServer(t, fake)
	defer srv.Close()

	for _, request := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/agents/codex/account-switches/switch-1"},
		{method: http.MethodPost, path: "/api/v1/agents/codex/account-switches/switch-1/cancel"},
	} {
		_, status, _ := doRequest(t, srv, request.method, request.path, "")
		if status != http.StatusNotFound {
			t.Errorf("%s %s status = %d, want %d", request.method, request.path, status, http.StatusNotFound)
		}
	}
}

func TestCodexAccountEventStreamSendsNamedCachedState(t *testing.T) {
	events := make(chan agentsvc.CodexAccounts, 1)
	events <- codexAccountsFixture()
	close(events)
	fake := &fakeCodexAccounts{result: codexAccountsFixture(), events: events}
	srv := newCodexAccountServer(t, fake)
	defer srv.Close()
	response, err := http.Get(srv.URL + "/api/v1/agents/codex/accounts/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if response.StatusCode != http.StatusOK || !strings.Contains(text, "event: codex_account") || !strings.Contains(text, `"accountRevision":3`) || !strings.Contains(text, `"displayName":"Code review"`) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	for _, forbidden := range []string{"provider-limit-secret", "limitId"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("event leaked %q: %s", forbidden, body)
		}
	}
}
