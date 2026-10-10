package provideraccounts

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (h *harness) start(provider, accountID string) ports.ProviderLogin {
	h.t.Helper()
	login, err := h.svc.StartLogin(h.ctx, ports.ProviderLoginRequest{Provider: provider, AccountID: accountID})
	if err != nil || login.ID == "" || login.Status != "waiting" || login.Mode != "browser" || login.URL == "" || login.Provider != provider || login.AccountID != accountID {
		h.t.Fatalf("start %s: login=%+v err=%v", provider, login, err)
	}
	return login
}

func (h *harness) status(id string) (ports.ProviderLogin, error) {
	return h.svc.LoginStatus(h.ctx, id)
}

func TestLoginRecordsTheVerifiedAccountExactlyOnce(t *testing.T) {
	h := setup(t)
	login := h.start("codex", "")
	if got, err := h.status(login.ID); err != nil || got.Status != "waiting" || len(h.store.get().Accounts) != 0 {
		t.Fatalf("while waiting: login=%+v err=%v", got, err)
	}
	h.helper.complete(login.ID, signIn("codex", "alice@example.com"))
	done, err := h.status(login.ID)
	if err != nil || done.Status != "complete" || done.AccountID == "" {
		t.Fatalf("login=%+v err=%v", done, err)
	}
	pushes := h.helper.applies
	if again, err := h.status(login.ID); err != nil || again != done || h.helper.applies != pushes {
		t.Fatalf("a second poll: login=%+v err=%v", again, err)
	}
	state := h.store.get()
	if len(state.Accounts) != 1 || state.Accounts[0].ID != done.AccountID || state.Defaults["codex"] != done.AccountID || !state.Accounts[0].SignedIn() {
		t.Fatalf("state=%+v", state)
	}
	if _, err := h.status("unknown"); !errors.Is(err, ports.ErrProviderLoginUnknown) {
		t.Fatalf("unknown attempt: %v", err)
	}
	if next := h.start("codex", ""); next.ID == login.ID {
		t.Fatal("a finished attempt was handed out again")
	}
}

func TestLoginRequestsAreValidatedBeforeTheHelperIsAsked(t *testing.T) {
	h := setup(t)
	for name, attempt := range map[string]struct {
		request       ports.ProviderLoginRequest
		code, message string
	}{
		"no provider":     {ports.ProviderLoginRequest{}, "PROVIDER_REQUIRED", "Choose Codex or Claude"},
		"other provider":  {ports.ProviderLoginRequest{Provider: "gemini"}, "PROVIDER_REQUIRED", "Choose Codex or Claude"},
		"claude device":   {ports.ProviderLoginRequest{Provider: "claude", Mode: "device"}, "LOGIN_MODE_UNSUPPORTED", "Device login is available for Codex only"},
		"empty import":    {ports.ProviderLoginRequest{Provider: "codex", Mode: "import", CredentialJSON: "  "}, "CREDENTIAL_JSON_REQUIRED", "Paste or choose a credential JSON file"},
		"key without URL": {ports.ProviderLoginRequest{Provider: "codex", Mode: "api_key", APIKey: "sk-test"}, "API_KEY_FIELDS_REQUIRED", "API key and base URL are required"},
		"URL without key": {ports.ProviderLoginRequest{Provider: "claude", Mode: "api_key", BaseURL: "https://api.example"}, "API_KEY_FIELDS_REQUIRED", "API key and base URL are required"},
		"unknown mode":    {ports.ProviderLoginRequest{Provider: "codex", Mode: "pigeon"}, "LOGIN_MODE_UNSUPPORTED", "Choose browser, device, API key, or JSON import"},
	} {
		var invalid *apierr.Error
		if _, err := h.svc.StartLogin(h.ctx, attempt.request); !errors.As(err, &invalid) || invalid.Kind != apierr.KindInvalid || invalid.Code != attempt.code || invalid.Message != attempt.message {
			t.Fatalf("%s: err=%#v", name, err)
		}
	}
	if len(h.helper.started) != 0 {
		t.Fatalf("an invalid request reached the helper: %+v", h.helper.started)
	}
	for _, request := range []ports.ProviderLoginRequest{
		{Provider: "codex", Mode: "device"},
		{Provider: "claude", Mode: "import", CredentialJSON: `{"type":"claude"}`},
	} {
		login, err := h.svc.StartLogin(h.ctx, request)
		if err != nil || login.Mode != request.Mode || h.helper.started[len(h.helper.started)-1] != request {
			t.Fatalf("%s: login=%+v err=%v", request.Mode, login, err)
		}
	}
}

func TestAProviderHasOneWaitingAttemptAndProvidersAreIndependent(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	if err := h.act(alice, "sign-out"); err != nil {
		t.Fatal(err)
	}
	codex := h.start("codex", "")
	claude := h.start("claude", "")
	if codex.ID == claude.ID || len(h.helper.started) != 2 {
		t.Fatalf("codex=%s claude=%s started=%d", codex.ID, claude.ID, len(h.helper.started))
	}
	// A renderer that lost its state asks again and gets the same attempt.
	if resumed := h.start("codex", ""); resumed != codex || len(h.helper.started) != 2 {
		t.Fatalf("resumed=%+v started=%d", resumed, len(h.helper.started))
	}
	for _, other := range []ports.ProviderLoginRequest{{Provider: "codex", AccountID: alice}, {Provider: "codex", Mode: "device"}} {
		if _, err := h.svc.StartLogin(h.ctx, other); !errors.Is(err, ports.ErrProviderAccountConflict) {
			t.Fatalf("another sign-in while one waits: %v", err)
		}
	}
	for range 2 {
		if err := h.svc.CancelLogin(h.ctx, codex.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.svc.CancelLogin(h.ctx, "unknown"); err != nil {
		t.Fatalf("cancelling nothing: %v", err)
	}
	if got, _ := h.status(codex.ID); got.Status != "cancelled" || !reflect.DeepEqual(h.helper.cancelled, []string{codex.ID}) {
		t.Fatalf("login=%+v cancelled=%v", got, h.helper.cancelled)
	}
	if got, _ := h.status(claude.ID); got.Status != "waiting" {
		t.Fatalf("cancelling Codex ended Claude's attempt: %+v", got)
	}
	if relogin := h.start("codex", alice); relogin.ID == codex.ID {
		t.Fatal("a cancelled attempt was handed out again")
	}
}

func TestConcurrentStartsShareOneAttempt(t *testing.T) {
	h := setup(t)
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Go(func() { ids[i] = h.start("claude", "").ID })
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("attempts=%v", ids)
		}
	}
	if len(h.helper.started) != 1 {
		t.Fatalf("the helper opened %d sign-ins", len(h.helper.started))
	}
}

func TestSigningInAgainIsRefusedBeforeTheBrowserOpensWhenItCannotWork(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	for name, request := range map[string]ports.ProviderLoginRequest{
		"already signed in": {Provider: "codex", AccountID: alice},
		"removed entry":     {Provider: "codex", AccountID: "gone"},
		"another provider":  {Provider: "claude", AccountID: alice},
	} {
		if _, err := h.svc.StartLogin(h.ctx, request); !errors.Is(err, ports.ErrProviderAccountConflict) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(h.helper.started) != 0 {
		t.Fatal("a sign-in that cannot be recorded was opened")
	}
}

func TestAnExpiredAttemptEndsEvenWhenTheHelperCannotBeReached(t *testing.T) {
	h := setup(t)
	login := h.start("codex", "")
	h.helper.statusErr = errInjected
	if got, err := h.status(login.ID); !errors.Is(err, errInjected) || got.Status != "waiting" {
		t.Fatalf("an outage before the deadline: login=%+v err=%v", got, err)
	}
	h.advance(6 * time.Minute)
	h.helper.cancelErr = errInjected
	if got, err := h.status(login.ID); !errors.Is(err, errInjected) || got.Status != "waiting" {
		t.Fatalf("a refused cancel must stay retryable: login=%+v err=%v", got, err)
	}
	h.helper.cancelErr = nil
	if got, err := h.status(login.ID); err != nil || got.Status != "failed" || !reflect.DeepEqual(h.helper.cancelled, []string{login.ID}) {
		t.Fatalf("login=%+v err=%v cancelled=%v", got, err, h.helper.cancelled)
	}
	h.helper.statusErr = nil
	slow := h.start("codex", "")
	h.advance(6 * time.Minute)
	if got, err := h.status(slow.ID); err != nil || got.Status != "failed" || len(h.store.get().Accounts) != 0 {
		t.Fatalf("still waiting at the deadline: login=%+v err=%v", got, err)
	}
	// A sign-in that completed in time is still recorded when it is asked about late.
	late := h.start("codex", "")
	h.helper.complete(late.ID, signIn("codex", "alice@example.com"))
	h.advance(time.Hour)
	if got, err := h.status(late.ID); err != nil || got.Status != "complete" || len(h.store.get().Accounts) != 1 {
		t.Fatalf("login=%+v err=%v", got, err)
	}
}

func TestAnEndedAttemptDiscardsASignInGrantedAMomentBefore(t *testing.T) {
	for _, ending := range []string{"cancel", "expiry"} {
		t.Run(ending, func(t *testing.T) {
			h := setup(t)
			alice := h.signIn("codex", "alice@example.com")
			end := func(id string) {
				t.Helper()
				if ending == "cancel" {
					if err := h.svc.CancelLogin(h.ctx, id); err != nil {
						t.Fatal(err)
					}
					return
				}
				h.advance(6 * time.Minute)
				if got, err := h.status(id); err != nil || got.Status != "failed" {
					t.Fatalf("login=%+v err=%v", got, err)
				}
			}
			login := h.start("codex", "")
			h.helper.results[login.ID] = signIn("codex", "late@example.com")
			end(login.ID)
			if !reflect.DeepEqual(h.helper.deleted, []string{"late@example.com.json"}) || len(h.store.get().Accounts) != 1 {
				t.Fatalf("deleted=%v accounts=%d", h.helper.deleted, len(h.store.get().Accounts))
			}
			// The same person signing in twice yields the credential their account uses.
			again := h.start("codex", "")
			h.helper.results[again.ID] = signIn("codex", "alice@example.com")
			end(again.ID)
			if len(h.helper.deleted) != 1 || !h.view(alice).SignedIn {
				t.Fatalf("an account's own credential was deleted: %v", h.helper.deleted)
			}
		})
	}
}

func TestAVerifiedIdentityThatDoesNotMatchFailsTheAttemptAndIsDiscarded(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	bob := h.signIn("codex", "bob@example.com")
	if err := h.act(bob, "sign-out"); err != nil {
		t.Fatal(err)
	}
	h.helper.deleted = nil
	eve := signIn("codex", "eve@example.com")
	wrongProvider := signIn("claude", "bob@example.com")
	wrongProvider.CredentialRef = "claude-bob.json"
	taken := signIn("codex", "alice@example.com")
	taken.CredentialRef, taken.AuthID = "second-alice.json", "second-alice-auth"
	for i, attempt := range []struct {
		accountID string
		result    ports.VerifiedProviderLogin
		want      error
	}{
		{bob, eve, ports.ErrProviderAccountIncompatible},
		{bob, wrongProvider, ports.ErrProviderAccountIncompatible},
		{"", taken, ports.ErrProviderAccountConflict},
	} {
		before := h.store.get()
		login := h.start("codex", attempt.accountID)
		h.helper.complete(login.ID, attempt.result)
		got, err := h.status(login.ID)
		if !errors.Is(err, attempt.want) || got.Status != "failed" {
			t.Fatalf("attempt %d: login=%+v err=%v", i, got, err)
		}
		if encode(h.store.get()) != encode(before) || len(h.helper.deleted) != i+1 || h.helper.deleted[i] != attempt.result.CredentialRef {
			t.Fatalf("attempt %d: deleted=%v", i, h.helper.deleted)
		}
		if again, err := h.status(login.ID); err != nil || again.Status != "failed" {
			t.Fatalf("attempt %d stays failed: login=%+v err=%v", i, again, err)
		}
		h.helper.status = "waiting"
	}
	if !h.view(alice).SignedIn || h.view(bob).SignedIn {
		t.Fatal("a rejected sign-in changed an account")
	}
	// A rejected sign-in that is the credential of an account is never deleted.
	login := h.start("codex", bob)
	h.helper.complete(login.ID, signIn("codex", "alice@example.com"))
	if _, err := h.status(login.ID); !errors.Is(err, ports.ErrProviderAccountIncompatible) || len(h.helper.deleted) != 3 {
		t.Fatalf("err=%v deleted=%v", err, h.helper.deleted)
	}
}

func TestHelperAndStorageFailuresLeaveTheAttemptWaiting(t *testing.T) {
	h := setup(t)
	h.helper.startErr = errInjected
	if login, err := h.svc.StartLogin(h.ctx, ports.ProviderLoginRequest{Provider: "codex"}); !errors.Is(err, errInjected) || login.ID != "" {
		t.Fatalf("login=%+v err=%v", login, err)
	}
	h.helper.startErr = nil
	login := h.start("codex", "")
	h.helper.complete(login.ID, signIn("codex", "alice@example.com"))
	for name, breakIt := range map[string]func(error){
		"status":       func(err error) { h.helper.statusErr = err },
		"result":       func(err error) { h.helper.resultErr = err },
		"route push":   func(err error) { h.helper.applyErr = err },
		"account save": func(err error) { h.store.saveErr = err },
	} {
		breakIt(errInjected)
		if got, err := h.status(login.ID); !errors.Is(err, errInjected) || got.Status != "waiting" || got.AccountID != "" {
			t.Fatalf("%s failure: login=%+v err=%v", name, got, err)
		}
		if len(h.store.get().Accounts) != 0 || len(h.helper.deleted) != 0 {
			t.Fatalf("%s failure recorded an account or deleted the sign-in", name)
		}
		breakIt(nil)
	}
	got, err := h.status(login.ID)
	if err != nil || got.Status != "complete" || len(h.store.get().Accounts) != 1 || h.store.get().Accounts[0].ID != got.AccountID {
		t.Fatalf("login=%+v err=%v accounts=%+v", got, err, h.store.get().Accounts)
	}
}

func TestAFailedSignInAtTheProviderNeverCreatesAnAccount(t *testing.T) {
	h := setup(t)
	login := h.start("claude", "")
	h.helper.status = "failed"
	if got, err := h.status(login.ID); err != nil || got.Status != "failed" || len(h.store.get().Accounts) != 0 {
		t.Fatalf("login=%+v err=%v", got, err)
	}
	h.helper.status = "waiting"
	if next := h.start("claude", ""); next.ID == login.ID {
		t.Fatal("a failed attempt was handed out again")
	}
}
