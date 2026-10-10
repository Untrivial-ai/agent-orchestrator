package provideraccounts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAHelperRefusalWritesNothingAndIsNotRetriedLater(t *testing.T) {
	for _, refusal := range []error{ports.ErrProviderAccountBusy, errInjected} {
		h := setup(t)
		alice := h.signIn("codex", "alice@example.com")
		bob := h.signIn("codex", "bob@example.com")
		h.assign("s1", domain.HarnessCodex, bob)
		before, table := h.store.get(), slices.Clone(h.helper.routes)
		h.helper.applyErr = refusal
		for name, change := range map[string]func() error{
			"remove":   func() error { return h.act(bob, "remove") },
			"sign out": func() error { return h.act(bob, "sign-out") },
			"default":  func() error { return h.act(bob, "primary") },
			"move":     func() error { return h.act(alice, "assign-session", "s1") },
			"rename":   func() error { return h.act(bob, "rename", "Other") },
			"assign":   func() error { return h.svc.AssignAccount(h.ctx, "s2", domain.HarnessCodex, alice) },
			"forget":   func() error { return h.svc.ForgetAccount(h.ctx, "s1") },
			"sign in": func() error {
				_, err := h.svc.record(h.ctx, "codex", signIn("codex", "carol@example.com"), "")
				return err
			},
		} {
			if err := change(); !errors.Is(err, refusal) {
				t.Fatalf("%s: err=%v, want %v", name, err, refusal)
			}
			h.unchanged(before, name)
		}
		h.helper.applyErr = nil
		if err := h.svc.Sync(h.ctx); err != nil {
			t.Fatal(err)
		}
		h.unchanged(before, "the next sync")
		if !reflect.DeepEqual(h.helper.routes, table) {
			t.Fatal("a refused change reached the helper later")
		}
		h.route("s1", bob)
	}
}

func TestAnAccountWithARequestInFlightCannotBeSignedOutOrRemoved(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	bob := h.signIn("codex", "bob@example.com")
	h.assign("s1", domain.HarnessCodex, bob)
	h.assign("s2", domain.HarnessCodex, alice)
	h.helper.inFlight["bob@example.com-auth"] = true
	before := h.store.get()
	for _, action := range []string{"sign-out", "remove"} {
		if err := h.act(bob, action); !errors.Is(err, ports.ErrProviderAccountBusy) {
			t.Fatalf("%s: err=%v", action, err)
		}
		h.unchanged(before, action)
	}
	// Everything that keeps the busy account is still allowed.
	if err := h.act(alice, "assign-session", "s1"); err != nil {
		t.Fatalf("moving the busy session: %v", err)
	}
	if err := h.act(bob, "primary"); err != nil {
		t.Fatalf("changing the default: %v", err)
	}
	if err := h.act(alice, "sign-out", bob); err != nil {
		t.Fatalf("signing another account out: %v", err)
	}
	h.route("s1", bob)
	h.route("s2", bob)
	delete(h.helper.inFlight, "bob@example.com-auth")
	if err := h.act(bob, "remove"); err != nil {
		t.Fatalf("once its request has finished: %v", err)
	}
	h.route("s1", "")
}

func TestSyncRefillsAHelperThatLostItsTable(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	claude := h.signIn("claude", "carol@example.com")
	h.assign("s1", domain.HarnessCodex, alice)
	h.assign("s2", domain.HarnessClaudeCode, claude)
	if err := h.act(claude, "sign-out"); err != nil {
		t.Fatal(err)
	}
	table, accounts := slices.Clone(h.helper.routes), slices.Clone(h.helper.authIDs)
	h.helper.routes, h.helper.authIDs = nil, nil
	restarted := New(h.store, h.helper, func() string { return "unused" })
	if err := restarted.Sync(h.ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.helper.routes, table) || !reflect.DeepEqual(h.helper.authIDs, accounts) {
		t.Fatalf("routes=%+v accounts=%v", h.helper.routes, h.helper.authIDs)
	}
	h.route("s1", alice)
	h.route("s2", "")
}

func TestSyncDoesNotStartTheHelperOnADeviceWithoutAccounts(t *testing.T) {
	h := setup(t)
	if err := h.svc.Sync(h.ctx); err != nil || h.helper.applies != 0 || h.helper.heldCalls != 0 {
		t.Fatalf("err=%v pushes=%d listings=%d", err, h.helper.applies, h.helper.heldCalls)
	}
	if views, err := h.svc.Accounts(h.ctx, true, true); err != nil || len(views) != 0 || h.helper.heldCalls != 0 || len(h.helper.usageFor) != 0 {
		t.Fatalf("listing an empty device asked the helper: views=%v err=%v", views, err)
	}
	if _, handled := h.svc.AuthenticationReadiness(h.ctx, domain.HarnessCodex, ""); !handled || h.helper.heldCalls != 0 {
		t.Fatal("a readiness check asked the helper")
	}
}

func TestAFailedSyncChangesNothingStored(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	h.assign("s1", domain.HarnessCodex, alice)
	before, saves := h.store.get(), h.store.saves
	h.helper.applyErr = errInjected
	if err := h.svc.Sync(h.ctx); !errors.Is(err, errInjected) {
		t.Fatalf("err=%v", err)
	}
	h.unchanged(before, "a failed sync")
	h.helper.applyErr = nil
	if err := h.svc.Sync(h.ctx); err != nil || h.store.saves != saves {
		t.Fatalf("err=%v, a sync wrote the store %d times", err, h.store.saves-saves)
	}
	h.store.loadErr = errInjected
	if err := h.svc.Sync(h.ctx); !errors.Is(err, errInjected) {
		t.Fatalf("unreadable store: err=%v", err)
	}
}

func TestAFailedSaveAfterTheHelperAcceptedIsHealedByTheNextSync(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	bob := h.signIn("codex", "bob@example.com")
	h.assign("s1", domain.HarnessCodex, bob)
	before := h.store.get()
	h.store.saveErr = errInjected
	if err := h.act(bob, "remove"); !errors.Is(err, errInjected) {
		t.Fatalf("err=%v", err)
	}
	h.unchanged(before, "a change that could not be saved")
	if auth, _ := h.helperAuth("s1"); auth != "alice@example.com-auth" {
		t.Fatalf("the helper did not take the change first: %q", auth)
	}
	h.store.saveErr = nil
	if err := h.svc.Sync(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.route("s1", bob)
	if !slices.Contains(h.helper.authIDs, "bob@example.com-auth") {
		t.Fatalf("helper accounts=%v", h.helper.authIDs)
	}
	if err := h.act(bob, "remove"); err != nil {
		t.Fatal(err)
	}
	h.route("s1", alice)
}

func TestAChangeThatCannotDeleteTheCredentialIsStillSaved(t *testing.T) {
	h := setup(t)
	alice := h.signIn("codex", "alice@example.com")
	h.helper.deleteErr = errInjected
	if err := h.act(alice, "sign-out"); !errors.Is(err, errInjected) {
		t.Fatalf("err=%v", err)
	}
	if h.store.get().Accounts[0].SignedIn() || len(h.helper.authIDs) != 0 {
		t.Fatal("the sign-out was not saved")
	}
}

func TestSyncSweepsSignInsNoAccountNames(t *testing.T) {
	h := setup(t)
	h.signIn("codex", "alice@example.com")
	old, young := h.svc.now().Add(-time.Hour), h.svc.now().Add(-time.Minute)
	h.helper.held = []ports.ProviderCredential{
		{Name: "alice@example.com.json", Provider: "codex", ModifiedAt: old},
		{Name: "leftover.json", Provider: "claude", ModifiedAt: old},
		{Name: "attempt-in-progress.json", Provider: "codex", ModifiedAt: young},
		{Name: "age-unknown.json", Provider: "codex"},
		{Name: "not-ours.json", Provider: "gemini", ModifiedAt: old},
	}
	h.helper.heldErr = errInjected
	if err := h.svc.Sync(h.ctx); err != nil || len(h.helper.deleted) != 0 {
		t.Fatalf("an unreadable list: err=%v deleted=%v", err, h.helper.deleted)
	}
	h.helper.heldErr = nil
	if err := h.svc.Sync(h.ctx); err != nil || len(h.helper.deleted) != 0 {
		t.Fatalf("the sweep did not pace itself: err=%v deleted=%v", err, h.helper.deleted)
	}
	h.advance(10 * time.Minute)
	listings := h.helper.heldCalls
	for range 3 {
		if err := h.svc.Sync(h.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(h.helper.deleted, []string{"leftover.json"}) || h.helper.heldCalls != listings+1 {
		t.Fatalf("deleted=%v listings=%d", h.helper.deleted, h.helper.heldCalls-listings)
	}
}

func TestRunSyncsReadsAndMovesOldChatsUntilNoneAreLeft(t *testing.T) {
	h := setup(t)
	h.svc.tick = time.Millisecond
	h.helper.native["codex"] = nativeSource{fingerprint: "f1", login: signIn("codex", "native@example.com")}
	ctx, stop := context.WithCancel(h.ctx)
	// One failed pass, then two chats left, one, none.
	answers, calls := []int{0, 2, 1, 0}, make(chan int, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pass := 0
		h.svc.Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) (int, error) {
			pass++
			calls <- pass
			if pass == 1 {
				return 0, errInjected
			}
			return answers[min(pass-1, len(answers)-1)], nil
		})
	}()
	for range len(answers) {
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatal("the migration stopped being called before it reported none left")
		}
	}
	time.Sleep(30 * time.Millisecond)
	stop()
	<-done
	if len(calls) != 0 {
		t.Fatalf("the migration was called %d more times after it reported none left", len(calls))
	}
	state := h.store.get()
	if len(state.Accounts) != 1 || h.helper.applies < 2 || len(h.helper.usageFor) == 0 || h.helper.heldCalls == 0 {
		t.Fatalf("accounts=%d pushes=%d usage reads=%d listings=%d", len(state.Accounts), h.helper.applies, len(h.helper.usageFor), h.helper.heldCalls)
	}
}

// TestRandomAccountJourneysKeepTheHelperAndTheStoreInStep drives random
// account changes, some of them refused, and checks after each one that the
// helper's table is exactly what the store says.
func TestRandomAccountJourneysKeepTheHelperAndTheStoreInStep(t *testing.T) {
	for seed := range int64(12) {
		h := setup(t)
		rng := rand.New(rand.NewSource(seed))
		agents := map[string]domain.AgentHarness{"codex": domain.HarnessCodex, "claude": domain.HarnessClaudeCode}
		tickets := map[string]string{}
		pick := func(values []string) string {
			if len(values) == 0 {
				return "none"
			}
			return values[rng.Intn(len(values))]
		}
		for step := range 80 {
			state := h.store.get()
			var accounts, sessions []string
			for _, a := range state.Accounts {
				accounts = append(accounts, a.ID)
			}
			for _, r := range state.Routes {
				sessions = append(sessions, string(r.SessionID))
			}
			provider := pick([]string{"codex", "claude"})
			h.helper.applyErr = nil
			if rng.Intn(6) == 0 {
				h.helper.applyErr = ports.ErrProviderAccountBusy
			}
			var err error
			switch rng.Intn(8) {
			case 0:
				_, err = h.svc.record(h.ctx, provider, signIn(provider, fmt.Sprintf("user%d@example.com", rng.Intn(4))), "")
			case 1:
				_, err = h.svc.record(h.ctx, provider, signIn(provider, fmt.Sprintf("again%d@example.com", step)), pick(accounts))
			case 2:
				id, _, _ := h.svc.ResolveAccount(h.ctx, agents[provider], "")
				err = h.svc.AssignAccount(h.ctx, domain.SessionID(fmt.Sprintf("s%d", step)), agents[provider], id)
			case 3:
				err = h.act(pick(accounts), "primary")
			case 4:
				err = h.act(pick(accounts), "assign-session", pick(sessions))
			case 5:
				err = h.act(pick(accounts), pick([]string{"sign-out", "remove"}), pick(accounts))
			case 6:
				err = h.svc.ForgetAccount(h.ctx, domain.SessionID(pick(sessions)))
			case 7:
				_, err = h.svc.AdoptSession(h.ctx, domain.SessionID(fmt.Sprintf("old%d", step)), agents[provider])
			}
			after := h.store.get()
			if err != nil && encode(after) != encode(state) {
				t.Fatalf("seed %d step %d: a refused change (%v) was stored", seed, step, err)
			}
			h.helper.applyErr = nil
			if err := h.svc.Sync(h.ctx); err != nil {
				t.Fatal(err)
			}
			if len(h.helper.routes) != len(after.Routes) {
				t.Fatalf("seed %d step %d: helper has %d routes, the store %d", seed, step, len(h.helper.routes), len(after.Routes))
			}
			seen := map[string]bool{}
			for _, a := range after.Accounts {
				if seen[a.Provider+a.Email] || a.DisplayName == "" || a.SignedIn() != slices.Contains(h.helper.authIDs, a.AuthID) && a.AuthID != "" {
					t.Fatalf("seed %d step %d: account %+v", seed, step, a)
				}
				seen[a.Provider+a.Email] = true
			}
			for _, r := range after.Routes {
				i := index(after, r.AccountID)
				if r.AccountID != "" && (i < 0 || after.Accounts[i].Provider != r.Provider || !after.Accounts[i].SignedIn()) {
					t.Fatalf("seed %d step %d: route %+v names no signed-in account of its provider", seed, step, r)
				}
				if r.AccountID == "" && after.Defaults[r.Provider] != "" {
					t.Fatalf("seed %d step %d: session %s waits although %s has a default", seed, step, r.SessionID, r.Provider)
				}
				h.route(string(r.SessionID), r.AccountID)
				env, _ := h.svc.LaunchAccountEnv(h.ctx, r.SessionID)
				ticket := env["AO_PROXY_TICKET"] + env["ANTHROPIC_AUTH_TOKEN"]
				if known, ok := tickets[string(r.SessionID)]; ok && known != ticket {
					t.Fatalf("seed %d step %d: session %s changed its ticket", seed, step, r.SessionID)
				}
				tickets[string(r.SessionID)] = ticket
			}
			for provider, id := range after.Defaults {
				if i := index(after, id); id != "" && (i < 0 || after.Accounts[i].Provider != provider || !after.Accounts[i].SignedIn()) {
					t.Fatalf("seed %d step %d: default of %s is %q", seed, step, provider, id)
				}
			}
		}
	}
}

func TestSyncRemovesALeftoverAPIKeyOnlyWhenNothingCouldBeHoldingIt(t *testing.T) {
	key := func(index string) ports.VerifiedProviderLogin {
		return ports.VerifiedProviderLogin{Provider: "codex", Email: "key " + index, Kind: "api_key", CredentialRef: "config-index:codex:" + index, AuthID: index + "-auth"}
	}
	leftover, mine := ports.ProviderCredential{Name: "config-index:codex:leftover", Provider: "codex"}, ports.ProviderCredential{Name: "config-index:codex:mine", Provider: "codex"}
	for name, tc := range map[string]struct {
		held    []ports.ProviderCredential
		signIn  bool
		deleted []string
	}{
		"a key no account names goes, the account's own key stays": {held: []ports.ProviderCredential{mine, leftover}, deleted: []string{leftover.Name}},
		"nothing goes once a sign-in has begun":                    {held: []ports.ProviderCredential{mine, leftover}, signIn: true},
		"nothing goes when the account's own key is not listed":    {held: []ports.ProviderCredential{leftover}},
	} {
		t.Run(name, func(t *testing.T) {
			h := setup(t)
			if _, err := h.svc.record(h.ctx, "codex", key("mine"), ""); err != nil {
				t.Fatal(err)
			}
			if tc.signIn {
				if _, err := h.svc.StartLogin(h.ctx, ports.ProviderLoginRequest{Provider: "claude", Mode: "browser"}); err != nil {
					t.Fatal(err)
				}
			}
			h.helper.held = tc.held
			if err := h.svc.Sync(h.ctx); err != nil || !reflect.DeepEqual(h.helper.deleted, tc.deleted) {
				t.Fatalf("err=%v deleted=%v, want %v", err, h.helper.deleted, tc.deleted)
			}
		})
	}
}
