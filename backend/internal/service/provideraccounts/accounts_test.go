package provideraccounts

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestFirstSignInBecomesTheDefaultAndProvidersAreIndependent(t *testing.T) {
	h := setup(t)
	for _, agent := range []domain.AgentHarness{domain.HarnessCodex, domain.HarnessClaudeCode} {
		if id, managed, err := h.svc.ResolveAccount(h.ctx, agent, ""); id != "" || !managed || !errors.Is(err, ports.ErrProviderLoginRequired) {
			t.Fatalf("before any sign-in: id=%q managed=%t err=%v", id, managed, err)
		}
	}
	codex := h.signIn("codex", "alice@example.com")
	claude := h.signIn("claude", "bob@example.com")
	h.signIn("codex", "carol@example.com")
	h.signIn("claude", "dana@example.com")
	for agent, want := range map[domain.AgentHarness]string{domain.HarnessCodex: codex, domain.HarnessClaudeCode: claude} {
		if id, managed, err := h.svc.ResolveAccount(h.ctx, agent, ""); id != want || !managed || err != nil {
			t.Fatalf("%s: id=%q managed=%t err=%v, want %q", agent, id, managed, err, want)
		}
	}
	if id, managed, err := h.svc.ResolveAccount(h.ctx, domain.AgentHarness("opencode"), ""); id != "" || managed || err != nil {
		t.Fatalf("an agent AO does not manage: id=%q managed=%t err=%v", id, managed, err)
	}
	if !h.view(codex).Primary || h.view(codex).Kind != "oauth" || !h.view(codex).SignedIn {
		t.Fatalf("view=%+v", h.view(codex))
	}
}

func TestOneIdentityIsOneEntryAndSigningInAgainKeepsItsSessions(t *testing.T) {
	h := setup(t)
	alice := h.signIn("claude", "alice@example.com")
	h.assign("s1", domain.HarnessClaudeCode, alice)
	other := signIn("claude", "ALICE@example.com")
	other.CredentialRef, other.AuthID = "second.json", "second-auth"
	if _, err := h.svc.record(h.ctx, "claude", other, ""); !errors.Is(err, ports.ErrProviderAccountConflict) {
		t.Fatalf("a second entry for one identity: %v", err)
	}
	if id, err := h.svc.record(h.ctx, "claude", signIn("claude", "alice@example.com"), ""); err != nil || id != alice {
		t.Fatalf("recording the same sign-in again: id=%q err=%v", id, err)
	}
	if err := h.act(alice, "sign-out"); err != nil {
		t.Fatal(err)
	}
	if id, err := h.svc.record(h.ctx, "claude", other, alice); err != nil || id != alice {
		t.Fatalf("signing in to the entry again: id=%q err=%v", id, err)
	}
	h.route("s1", alice)
	state := h.store.get()
	if len(state.Accounts) != 1 || state.Accounts[0].CredentialRef != "second.json" || state.Accounts[0].AuthID != "second-auth" {
		t.Fatalf("accounts=%+v", state.Accounts)
	}
	for name, attempt := range map[string]struct {
		login   ports.VerifiedProviderLogin
		relogin string
		want    error
	}{
		"another person":   {signIn("claude", "eve@example.com"), alice, ports.ErrProviderAccountIncompatible},
		"another provider": {signIn("codex", "alice@example.com"), alice, ports.ErrProviderAccountIncompatible},
		"a removed entry":  {signIn("claude", "alice@example.com"), "gone", ports.ErrProviderAccountUnknown},
		"no identity":      {ports.VerifiedProviderLogin{Provider: "claude", CredentialRef: "x.json", AuthID: "x"}, "", ports.ErrProviderAccountIncompatible},
	} {
		before := h.store.get()
		if _, err := h.svc.record(h.ctx, "claude", attempt.login, attempt.relogin); !errors.Is(err, attempt.want) {
			t.Fatalf("%s: err=%v", name, err)
		}
		if !reflect.DeepEqual(before, h.store.get()) {
			t.Fatalf("%s changed the accounts", name)
		}
	}
}

func TestASignInReplacesASavedOneOnlyWhenItIsDeadOrEnding(t *testing.T) {
	for _, reason := range []string{"healthy", "dead", "ending"} {
		t.Run(reason, func(t *testing.T) {
			h := setup(t)
			alice := h.signIn("codex", "alice@example.com")
			h.assign("s1", domain.HarnessCodex, alice)
			switch reason {
			case "dead":
				h.helper.held = []ports.ProviderCredential{{AuthID: "alice@example.com-auth", Name: "alice@example.com.json", Provider: "codex", Failed: true}}
			case "ending":
				h.helper.usage.SignInEnding = true
			}
			if _, err := h.svc.Accounts(h.ctx, true, true); err != nil {
				t.Fatal(err)
			}
			fresh := signIn("codex", "alice@example.com")
			fresh.CredentialRef, fresh.AuthID = "fresh.json", "fresh-auth"
			_, err := h.svc.StartLogin(h.ctx, ports.ProviderLoginRequest{Provider: "codex", AccountID: alice})
			id, recordErr := h.svc.record(h.ctx, "codex", fresh, alice)
			if reason == "healthy" {
				if !errors.Is(err, ports.ErrProviderAccountConflict) || !errors.Is(recordErr, ports.ErrProviderAccountConflict) || len(h.helper.started) != 0 {
					t.Fatalf("a working sign-in was replaced: start=%v record=%v", err, recordErr)
				}
				h.route("s1", alice)
				return
			}
			if err != nil || recordErr != nil || id != alice {
				t.Fatalf("start=%v record=%v id=%q", err, recordErr, id)
			}
			h.route("s1", alice)
			if got := h.store.get().Accounts[0]; got.AuthID != "fresh-auth" || !reflect.DeepEqual(h.helper.deleted, []string{"alice@example.com.json"}) {
				t.Fatalf("account=%+v deleted=%v", got, h.helper.deleted)
			}
		})
	}
}

func TestMakingAnAccountTheDefaultChangesOnlyNewSessions(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	bob := h.signIn("codex", "bob@example.com")
	h.assign("old", domain.HarnessCodex, alice)
	if err := h.act(bob, "primary"); err != nil {
		t.Fatal(err)
	}
	h.route("old", alice)
	chosen, _, err := h.svc.ResolveAccount(h.ctx, domain.HarnessCodex, "")
	if err != nil || chosen != bob {
		t.Fatalf("default=%q err=%v", chosen, err)
	}
	h.assign("new", domain.HarnessCodex, chosen)
	h.route("new", bob)
	if h.view(alice).Primary || !h.view(bob).Primary || !reflect.DeepEqual(h.view(alice).Sessions, []string{"old"}) {
		t.Fatalf("alice=%+v bob=%+v", h.view(alice), h.view(bob))
	}
}

func TestSignOutKeepsTheEntryRemoveDeletesItAndBothMoveItsSessions(t *testing.T) {
	for _, action := range []string{"sign-out", "remove"} {
		t.Run(action, func(t *testing.T) {
			h := setup(t)
			alice := h.signIn("codex", "alice@example.com")
			bob := h.signIn("codex", "bob@example.com")
			claude := h.signIn("claude", "carol@example.com")
			h.assign("alice-1", domain.HarnessCodex, alice)
			h.assign("bob-1", domain.HarnessCodex, bob)
			h.assign("bob-2", domain.HarnessCodex, bob)
			h.assign("claude-1", domain.HarnessClaudeCode, claude)
			if err := h.act(bob, action); err != nil {
				t.Fatal(err)
			}
			for _, session := range []string{"alice-1", "bob-1", "bob-2"} {
				h.route(session, alice)
			}
			h.route("claude-1", claude)
			state := h.store.get()
			if i := index(state, bob); action == "remove" && i >= 0 || action == "sign-out" && (i < 0 || state.Accounts[i].SignedIn() || state.Accounts[i].Email != "bob@example.com") {
				t.Fatalf("accounts=%+v", state.Accounts)
			}
			if !reflect.DeepEqual(h.helper.deleted, []string{"bob@example.com.json"}) || slices.Contains(h.helper.authIDs, "bob@example.com-auth") {
				t.Fatalf("deleted=%v helper accounts=%v", h.helper.deleted, h.helper.authIDs)
			}
			if h.defaultOf("codex") != alice || h.defaultOf("claude") != claude {
				t.Fatalf("defaults=%v", state.Defaults)
			}
		})
	}
}

func TestRemovingTheDefaultNeedsASignedInReplacementOfTheSameProvider(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	bob := h.signIn("codex", "bob@example.com")
	out := h.signIn("codex", "carol@example.com")
	claude := h.signIn("claude", "dana@example.com")
	h.assign("s1", domain.HarnessCodex, alice)
	if err := h.act(out, "sign-out"); err != nil {
		t.Fatal(err)
	}
	h.helper.deleted = nil
	for replacement, want := range map[string]error{"": ports.ErrProviderPrimaryRequired, alice: ports.ErrProviderPrimaryRequired, "missing": ports.ErrProviderAccountUnknown,
		claude: ports.ErrProviderAccountIncompatible, out: ports.ErrProviderLoginRequired} {
		before := h.store.get()
		if err := h.act(alice, "remove", replacement); !errors.Is(err, want) {
			t.Fatalf("replacement %q: err=%v, want %v", replacement, err, want)
		}
		h.unchanged(before, "a refused removal")
	}
	if err := h.act(alice, "remove", bob); err != nil {
		t.Fatal(err)
	}
	h.route("s1", bob)
	if h.defaultOf("codex") != bob || h.defaultOf("claude") != claude {
		t.Fatalf("defaults=%v", h.store.get().Defaults)
	}
}

func TestLastSignOutLeavesSessionsWaitingAndTheNextSignInPicksThemUp(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	claude := h.signIn("claude", "carol@example.com")
	h.assign("c1", domain.HarnessCodex, alice)
	h.assign("c2", domain.HarnessCodex, alice)
	h.assign("a1", domain.HarnessClaudeCode, claude)
	before, err := h.svc.LaunchAccountEnv(h.ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.act(alice, "sign-out"); err != nil {
		t.Fatal(err)
	}
	h.route("c1", "")
	h.route("c2", "")
	h.route("a1", claude)
	if _, managed, err := h.svc.ResolveAccount(h.ctx, domain.HarnessCodex, ""); !managed || !errors.Is(err, ports.ErrProviderLoginRequired) {
		t.Fatalf("managed=%t err=%v", managed, err)
	}
	bob := h.signIn("codex", "bob@example.com")
	h.route("c1", bob)
	h.route("c2", bob)
	h.route("a1", claude)
	after, err := h.svc.LaunchAccountEnv(h.ctx, "c1")
	if err != nil || !reflect.DeepEqual(before, after) || h.defaultOf("codex") != bob {
		t.Fatalf("env changed or default=%q err=%v", h.defaultOf("codex"), err)
	}
}

func TestAccountNamesAreGeneratedUniqueAndEditable(t *testing.T) {
	h := setup(t)
	h.svc.newID = func() string { return "same" }
	first := h.signIn("codex", "alice@example.com")
	h.signIn("codex", "bob@example.com")
	state := h.store.get()
	base := domain.GeneratedProviderAccountName("codex", "same")
	if state.Accounts[0].DisplayName != base || state.Accounts[1].DisplayName != base+" 2" {
		t.Fatalf("names=%q, %q", state.Accounts[0].DisplayName, state.Accounts[1].DisplayName)
	}
	if err := h.act(first, "rename", "  Work   Codex "); err != nil || h.store.get().Accounts[0].DisplayName != "Work Codex" {
		t.Fatalf("name=%q err=%v", h.store.get().Accounts[0].DisplayName, err)
	}
	for _, name := range []string{"   ", strings.Repeat("n", 81)} {
		if err := h.act(first, "rename", name); !errors.Is(err, ports.ErrProviderAccountNameInvalid) {
			t.Fatalf("name of %d characters: %v", len(name), err)
		}
	}
	if err := h.act(first, "rename", strings.Repeat("é", 80)); err != nil {
		t.Fatalf("80 characters: %v", err)
	}
	if err := h.act("gone", "rename", "x"); !errors.Is(err, ports.ErrProviderAccountUnknown) {
		t.Fatalf("unknown account: %v", err)
	}
	if err := h.act(first, "explode"); !errors.Is(err, ports.ErrProviderAccountActionUnavailable) {
		t.Fatalf("unknown action: %v", err)
	}
}

func TestAnExplicitAccountBeatsTheDefaultAndUnusableChoicesAreRefused(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	bob := h.signIn("codex", "bob@example.com")
	claude := h.signIn("claude", "carol@example.com")
	if id, managed, err := h.svc.ResolveAccount(h.ctx, domain.HarnessCodex, bob); id != bob || !managed || err != nil {
		t.Fatalf("id=%q managed=%t err=%v", id, managed, err)
	}
	if id, _, _ := h.svc.ResolveAccount(h.ctx, domain.HarnessCodex, ""); id != alice {
		t.Fatalf("default=%q", id)
	}
	if err := h.act(claude, "sign-out"); err != nil {
		t.Fatal(err)
	}
	for name, attempt := range map[string]struct {
		agent    domain.AgentHarness
		explicit string
		want     error
	}{
		"wrong provider":   {domain.HarnessCodex, claude, ports.ErrProviderAccountIncompatible},
		"unknown":          {domain.HarnessCodex, "missing", ports.ErrProviderAccountUnknown},
		"signed out":       {domain.HarnessClaudeCode, claude, ports.ErrProviderLoginRequired},
		"unmanaged agent":  {domain.AgentHarness("opencode"), alice, ports.ErrProviderAccountIncompatible},
		"signed out as is": {domain.HarnessClaudeCode, "", ports.ErrProviderLoginRequired},
	} {
		if id, _, err := h.svc.ResolveAccount(h.ctx, attempt.agent, attempt.explicit); id != "" || !errors.Is(err, attempt.want) {
			t.Fatalf("%s: id=%q err=%v", name, id, err)
		}
	}
	if err := h.svc.AssignAccount(h.ctx, "s1", domain.HarnessCodex, claude); !errors.Is(err, ports.ErrProviderAccountIncompatible) {
		t.Fatalf("assigning a Claude account to a Codex session: %v", err)
	}
	if err := h.act(claude, "primary"); !errors.Is(err, ports.ErrProviderLoginRequired) {
		t.Fatalf("a signed-out default: %v", err)
	}
	if _, managed, err := h.svc.SessionAccount(h.ctx, "s1"); managed || err != nil {
		t.Fatalf("a refused assignment left a route: managed=%t err=%v", managed, err)
	}
}

func TestSessionsKeepOneRouteTicketAndEnvironmentThroughEveryAccountChange(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	bob := h.signIn("codex", "bob@example.com")
	claude := h.signIn("claude", "carol@example.com")
	h.assign("c1", domain.HarnessCodex, alice)
	h.assign("c2", domain.HarnessCodex, alice)
	h.assign("a1", domain.HarnessClaudeCode, claude)
	if err := h.svc.AssignAccount(h.ctx, "c1", domain.HarnessCodex, bob); !errors.Is(err, ports.ErrProviderAccountConflict) {
		t.Fatalf("a second route for one session: %v", err)
	}
	envs := map[string]map[string]string{}
	for _, session := range []string{"c1", "c2", "a1"} {
		env, err := h.svc.LaunchAccountEnv(h.ctx, domain.SessionID(session))
		if err != nil {
			t.Fatal(err)
		}
		envs[session] = env
	}
	c1, a1 := envs["c1"], envs["a1"]
	if len(c1) != 2 || c1["AO_PROXY_ENDPOINT"] != "http://127.0.0.1:1234" || len(c1["AO_PROXY_TICKET"]) != 64 || c1["AO_PROXY_TICKET"] == envs["c2"]["AO_PROXY_TICKET"] {
		t.Fatalf("codex env=%v", c1)
	}
	if a1["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:1234" || len(a1["ANTHROPIC_AUTH_TOKEN"]) != 64 {
		t.Fatalf("claude env=%v", a1)
	}
	for _, name := range []string{"AO_PROXY_ENDPOINT", "AO_PROXY_TICKET", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if value, set := a1[name]; !set || value != "" {
			t.Fatalf("claude env leaves %s=%q set=%t", name, value, set)
		}
	}
	if stored := encode(h.store.get()); strings.Contains(stored, c1["AO_PROXY_TICKET"]) || strings.Contains(stored, a1["ANTHROPIC_AUTH_TOKEN"]) {
		t.Fatal("a ticket was stored")
	}
	if env, err := h.svc.LaunchAccountEnv(h.ctx, "unrouted"); env != nil || err != nil {
		t.Fatalf("an unrouted session got env=%v err=%v", env, err)
	}
	for _, step := range []func() error{
		func() error { return h.act(bob, "primary") },
		func() error { return h.act(bob, "assign-session", "c1") },
		func() error { return h.act(alice, "remove") },
		func() error { return h.act(bob, "sign-out") },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
		for session, want := range envs {
			if env, err := h.svc.LaunchAccountEnv(h.ctx, domain.SessionID(session)); err != nil || !reflect.DeepEqual(env, want) {
				t.Fatalf("session %s env changed: %v err=%v", session, env, err)
			}
		}
	}
	h.route("c1", "")
	h.route("a1", claude)
}

func TestAssignSessionMovesOneSessionAndLeavesTheDefaultAlone(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	bob := h.signIn("codex", "bob@example.com")
	claude := h.signIn("claude", "carol@example.com")
	h.assign("s1", domain.HarnessCodex, alice)
	h.assign("s2", domain.HarnessCodex, alice)
	if err := h.act(bob, "assign-session", "s1"); err != nil {
		t.Fatal(err)
	}
	h.route("s1", bob)
	h.route("s2", alice)
	if h.defaultOf("codex") != alice {
		t.Fatalf("default=%q", h.defaultOf("codex"))
	}
	if err := h.act(claude, "assign-session", "s1"); !errors.Is(err, ports.ErrProviderAccountIncompatible) {
		t.Fatalf("another provider's account: %v", err)
	}
	if err := h.act(bob, "assign-session", "unrouted"); !errors.Is(err, ports.ErrProviderAccountIncompatible) {
		t.Fatalf("a session without a route: %v", err)
	}
	h.route("s1", bob)
}

func TestForgetDropsOnlyThatSessionsRoute(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	h.assign("s1", domain.HarnessCodex, alice)
	h.assign("s2", domain.HarnessCodex, alice)
	if err := h.svc.ForgetAccount(h.ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, managed, _ := h.svc.SessionAccount(h.ctx, "s1"); managed {
		t.Fatal("the route was kept")
	}
	if _, found := h.helperAuth("s1"); found {
		t.Fatal("the helper still accepts the forgotten session's ticket")
	}
	h.route("s2", alice)
	applies := h.helper.applies
	if err := h.svc.ForgetAccount(h.ctx, "never-routed"); err != nil || h.helper.applies != applies {
		t.Fatalf("forgetting an unrouted session: err=%v pushes=%d", err, h.helper.applies-applies)
	}
}

func TestAdoptSession(t *testing.T) {
	t.Run("takes the provider's default account", func(t *testing.T) {
		h := setup(t)
		alice := h.signIn("codex", "alice@example.com")
		h.signIn("codex", "bob@example.com")
		if adopted, err := h.svc.AdoptSession(h.ctx, "old", domain.HarnessCodex); !adopted || err != nil {
			t.Fatalf("adopted=%t err=%v", adopted, err)
		}
		h.route("old", alice)
		if adopted, err := h.svc.AdoptSession(h.ctx, "other", domain.AgentHarness("opencode")); adopted || err != nil {
			t.Fatalf("an agent AO does not manage: adopted=%t err=%v", adopted, err)
		}
	})
	t.Run("waits when the provider has no account", func(t *testing.T) {
		h := setup(t)
		if adopted, err := h.svc.AdoptSession(h.ctx, "old", domain.HarnessClaudeCode); !adopted || err != nil {
			t.Fatalf("adopted=%t err=%v", adopted, err)
		}
		h.route("old", "")
		h.route("old", h.signIn("claude", "alice@example.com"))
	})
	t.Run("leaves a routed session as it is", func(t *testing.T) {
		h := setup(t)
		alice := h.signIn("codex", "alice@example.com")
		bob := h.signIn("codex", "bob@example.com")
		h.assign("s1", domain.HarnessCodex, bob)
		if adopted, err := h.svc.AdoptSession(h.ctx, "s1", domain.HarnessCodex); adopted || err != nil {
			t.Fatalf("adopted=%t err=%v", adopted, err)
		}
		h.route("s1", bob)
		_ = alice
	})
	t.Run("imports this computer's login first", func(t *testing.T) {
		h := setup(t)
		h.helper.native["codex"] = nativeSource{fingerprint: "f1", login: signIn("codex", "native@example.com")}
		if adopted, err := h.svc.AdoptSession(h.ctx, "old", domain.HarnessCodex); !adopted || err != nil {
			t.Fatalf("adopted=%t err=%v", adopted, err)
		}
		h.route("old", h.defaultOf("codex"))
		if h.defaultOf("codex") == "" {
			t.Fatal("the session waits although this computer is signed in")
		}
	})
	t.Run("changes nothing when the helper refuses", func(t *testing.T) {
		h := setup(t)
		h.signIn("codex", "alice@example.com")
		before := h.store.get()
		h.helper.applyErr = errInjected
		if adopted, err := h.svc.AdoptSession(h.ctx, "old", domain.HarnessCodex); adopted || !errors.Is(err, errInjected) {
			t.Fatalf("adopted=%t err=%v", adopted, err)
		}
		h.unchanged(before, "a refused adoption")
	})
}

func TestConcurrentChangesAllLand(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	claude := h.signIn("claude", "carol@example.com")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			agent, account := domain.HarnessCodex, alice
			if i%2 == 1 {
				agent, account = domain.HarnessClaudeCode, claude
			}
			h.assign("s"+string(rune('a'+i)), agent, account)
		})
	}
	wg.Wait()
	if state := h.store.get(); len(state.Routes) != 20 || len(h.helper.routes) != 20 {
		t.Fatalf("stored routes=%d helper routes=%d", len(state.Routes), len(h.helper.routes))
	}
	h.route("sa", alice)
	h.route("sb", claude)
}

func TestConcurrentSignInsOfOneIdentityLeaveOneEntry(t *testing.T) {
	h := setup(t)
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Go(func() { ids[i], _ = h.svc.record(h.ctx, "codex", signIn("codex", "alice@example.com"), "") })
	}
	wg.Wait()
	state := h.store.get()
	if len(state.Accounts) != 1 || state.Defaults["codex"] != state.Accounts[0].ID {
		t.Fatalf("accounts=%+v defaults=%v", state.Accounts, state.Defaults)
	}
	for _, id := range ids {
		if id != state.Accounts[0].ID {
			t.Fatalf("a sign-in was recorded as %q, the entry is %q", id, state.Accounts[0].ID)
		}
	}
}

func TestSettingsReserveAnAccountNameWhereItsSessionsGoAndSayWhenToWarn(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	bob := h.signIn("codex", "bob@example.com")
	out := h.signIn("codex", "carol@example.com")
	claude := h.signIn("claude", "dana@example.com")
	if err := h.act(out, "sign-out"); err != nil {
		t.Fatal(err)
	}
	h.helper.deleted = nil
	// The default is what new sessions use, so it cannot be held back.
	for id, want := range map[string]bool{alice: false, bob: true} {
		if err := h.settings(id, ports.ProviderAccountAction{Reserved: new(true)}); err != nil || h.view(id).Reserved != want {
			t.Fatalf("reserving %s: reserved=%t err=%v", id, h.view(id).Reserved, err)
		}
	}
	if err := h.act(bob, "primary"); err != nil || h.view(bob).Reserved || !h.view(bob).Primary {
		t.Fatalf("the new default is still reserved: view=%+v err=%v", h.view(bob), err)
	}
	if err := h.settings(alice, ports.ProviderAccountAction{Reserved: new(true)}); err != nil || !h.view(alice).Reserved {
		t.Fatalf("the account that was the default: view=%+v err=%v", h.view(alice), err)
	}
	if err := h.settings(alice, ports.ProviderAccountAction{Reserved: new(false)}); err != nil || h.view(alice).Reserved {
		t.Fatalf("lifting the reserve: view=%+v err=%v", h.view(alice), err)
	}
	// Sessions move to another account of the same provider, signed in or not.
	for target, want := range map[string]error{bob: nil, out: nil, "": nil, alice: ports.ErrProviderAccountIncompatible, claude: ports.ErrProviderAccountIncompatible, "gone": ports.ErrProviderAccountIncompatible} {
		before := h.store.get()
		if err := h.settings(alice, ports.ProviderAccountAction{OnLimit: new(target)}); !errors.Is(err, want) {
			t.Fatalf("on limit %q: err=%v, want %v", target, err, want)
		}
		if want != nil {
			h.unchanged(before, "a refused setting")
		} else if got := h.view(alice).OnLimit; got != target {
			t.Fatalf("on limit=%q, want %q", got, target)
		}
	}
	for given, want := range map[int]int{-5: 0, 0: 0, 25: 25, 90: 90, 95: 90} {
		if err := h.settings(alice, ports.ProviderAccountAction{WarnAt: new(given)}); err != nil || h.view(alice).WarnAt != want {
			t.Fatalf("warn at %d: stored %d err=%v", given, h.view(alice).WarnAt, err)
		}
	}
	// Each setting given replaces what is stored; the others stay.
	if err := h.settings(alice, ports.ProviderAccountAction{OnLimit: new(bob), WarnAt: new(20)}); err != nil {
		t.Fatal(err)
	}
	if err := h.settings(alice, ports.ProviderAccountAction{Reserved: new(true)}); err != nil {
		t.Fatal(err)
	}
	if view := h.view(alice); !view.Reserved || view.OnLimit != bob || view.WarnAt != 20 {
		t.Fatalf("view=%+v", view)
	}
	if err := h.settings("gone", ports.ProviderAccountAction{WarnAt: new(20)}); !errors.Is(err, ports.ErrProviderAccountUnknown) {
		t.Fatalf("an unknown account: %v", err)
	}
	// A signed-out account is still there to name; a removed one is not.
	if err := h.settings(out, ports.ProviderAccountAction{OnLimit: new(bob)}); err != nil {
		t.Fatal(err)
	}
	if err := h.act(bob, "sign-out", alice); err != nil || h.view(out).OnLimit != bob {
		t.Fatalf("a sign-out cleared the setting: on limit=%q err=%v", h.view(out).OnLimit, err)
	}
	if err := h.act(bob, "remove"); err != nil || h.view(alice).OnLimit != "" || h.view(out).OnLimit != "" {
		t.Fatalf("a removed account is still named: alice=%q carol=%q err=%v", h.view(alice).OnLimit, h.view(out).OnLimit, err)
	}
}
