package provideraccounts

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func nativeKey(provider, ref string) ports.VerifiedProviderLogin {
	return ports.VerifiedProviderLogin{Provider: provider, Email: "Global API key", Kind: "api_key", CredentialRef: ref, AuthID: ref + "-auth"}
}

func TestThisComputersLoginIsImportedOnceAndALaterRemovalStands(t *testing.T) {
	for _, action := range []string{"remove", "sign-out"} {
		t.Run(action, func(t *testing.T) {
			h := setup(t)
			imported := signIn("codex", "me@example.com")
			imported.Kind = "imported"
			h.helper.native["codex"] = nativeSource{fingerprint: "f1", login: imported}
			h.svc.importNative(h.ctx, false)
			state := h.store.get()
			if len(state.Accounts) != 1 || state.Accounts[0].Kind != "oauth" || state.Accounts[0].DisplayName == "" || state.Defaults["codex"] != state.Accounts[0].ID {
				t.Fatalf("state=%+v", state)
			}
			me := state.Accounts[0].ID
			if receipt := state.NativeImports["codex"]; receipt != (domain.NativeProviderImport{Fingerprint: "f1", AccountID: me, Email: "me@example.com"}) || !h.view(me).Global {
				t.Fatalf("receipt=%+v view=%+v", receipt, h.view(me))
			}
			// Routine reads look at the keychain once per window; a refresh looks now.
			reads := h.helper.reads
			h.svc.importNative(h.ctx, false)
			if _, err := h.svc.Accounts(h.ctx, false, false); err != nil || h.helper.reads != reads {
				t.Fatalf("err=%v extra reads=%d", err, h.helper.reads-reads)
			}
			if _, err := h.svc.Accounts(h.ctx, false, true); err != nil || h.helper.reads == reads {
				t.Fatalf("a refresh did not look: err=%v", err)
			}
			reads = h.helper.reads
			h.advance(5 * time.Minute)
			if h.svc.importNative(h.ctx, false); h.helper.reads == reads || len(h.helper.imports) != 1 {
				t.Fatalf("after the window: reads=%d imports=%v", h.helper.reads-reads, h.helper.imports)
			}
			if err := h.act(me, action); err != nil {
				t.Fatal(err)
			}
			h.helper.deleted = nil
			h.svc.importNative(h.ctx, true)
			// The agent renews its tokens: a new fingerprint, the same login.
			renewed := signIn("codex", "ME@example.com")
			renewed.CredentialRef, renewed.AuthID = "renewed.json", "renewed-auth"
			h.helper.native["codex"] = nativeSource{fingerprint: "f2", login: renewed}
			h.svc.importNative(h.ctx, true)
			h.svc.importNative(h.ctx, true)
			state = h.store.get()
			if signedIn := len(state.Accounts) == 1 && state.Accounts[0].SignedIn(); signedIn || len(state.Accounts) != map[string]int{"remove": 0, "sign-out": 1}[action] {
				t.Fatalf("the %s was undone: %+v", action, state.Accounts)
			}
			if state.NativeImports["codex"].Fingerprint != "f2" || !reflect.DeepEqual(h.helper.deleted, []string{"renewed.json"}) || len(h.helper.imports) != 2 {
				t.Fatalf("receipt=%+v deleted=%v imports=%v", state.NativeImports["codex"], h.helper.deleted, h.helper.imports)
			}
			// Another person signing in to the agent is a new login.
			h.helper.native["codex"] = nativeSource{fingerprint: "f3", login: signIn("codex", "other@example.com")}
			h.svc.importNative(h.ctx, true)
			state = h.store.get()
			other := state.Accounts[len(state.Accounts)-1]
			if other.Email != "other@example.com" || state.Defaults["codex"] != other.ID || !h.view(other.ID).Global {
				t.Fatalf("accounts=%+v defaults=%v", state.Accounts, state.Defaults)
			}
		})
	}
}

func TestALoginThatCannotBeReadLeavesTheAccountsAsTheyAre(t *testing.T) {
	h := setup(t)
	h.signIn("codex", "alice@example.com")
	before := h.store.get()
	h.helper.native["codex"] = nativeSource{fingerprint: "f1", err: errInjected}
	h.helper.native["claude"] = nativeSource{fingerprint: "f2"}
	h.svc.importNative(h.ctx, true)
	h.unchanged(before, "an unreadable login")
	h.helper.native["codex"] = nativeSource{fingerprint: "f1", login: signIn("codex", "me@example.com")}
	h.helper.applyErr = errInjected
	h.svc.importNative(h.ctx, true)
	h.unchanged(before, "an import the helper refused")
	h.helper.applyErr = nil
	h.svc.importNative(h.ctx, true)
	if state := h.store.get(); len(state.Accounts) != 2 || state.Defaults["codex"] != before.Defaults["codex"] {
		t.Fatalf("state=%+v", state)
	}
}

func TestTheGlobalMarkFollowsThisComputersLoginHoweverTheAccountWasAdded(t *testing.T) {
	h := setup(t)
	mine := h.signIn("claude", "Me@Example.com")
	other := h.signIn("claude", "other@example.com")
	if h.view(mine).Global || h.view(other).Global {
		t.Fatal("an account is marked before this computer's login is known")
	}
	native := signIn("claude", "me@example.com")
	native.CredentialRef, native.AuthID = "native.json", "native-auth"
	h.helper.native["claude"] = nativeSource{fingerprint: "f1", login: native}
	h.svc.importNative(h.ctx, true)
	state := h.store.get()
	// The account keeps the sign-in it has: it may hold a newer refresh token.
	if len(state.Accounts) != 2 || state.Accounts[0].AuthID != "Me@Example.com-auth" || !reflect.DeepEqual(h.helper.deleted, []string{"native.json"}) {
		t.Fatalf("accounts=%+v deleted=%v", state.Accounts, h.helper.deleted)
	}
	if !h.view(mine).Global || h.view(other).Global || state.Defaults["claude"] != mine {
		t.Fatalf("mine=%+v other=%+v", h.view(mine), h.view(other))
	}
	if err := h.act(mine, "remove", other); err != nil {
		t.Fatal(err)
	}
	if again := h.signIn("claude", "me@example.com"); !h.view(again).Global || h.view(other).Global {
		t.Fatal("the mark did not follow the account that was added again")
	}
}

func TestTheGlobalMarkAlsoFollowsTheAccountTheReceiptNames(t *testing.T) {
	h := setup(t)
	mine := h.signIn("codex", "me@example.com")
	other := h.signIn("codex", "other@example.com")
	key, err := h.svc.record(h.ctx, "codex", nativeKey("codex", "config-index:codex:0"), "")
	if err != nil {
		t.Fatal(err)
	}
	// A receipt written before receipts kept the email names only the account.
	for receipt, want := range map[domain.NativeProviderImport]string{{Fingerprint: "f1", AccountID: other}: other, {Fingerprint: "f1", Email: "ME@example.com"}: mine, {Fingerprint: "f1", AccountID: key}: ""} {
		state := h.store.get()
		state.NativeImports = map[string]domain.NativeProviderImport{"codex": receipt}
		if err = h.store.SaveProviderAccounts(h.ctx, state); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{mine, other, key} {
			if h.view(id).Global != (id == want) {
				t.Fatalf("receipt %+v: account %s global=%t", receipt, id, h.view(id).Global)
			}
		}
	}
}

func TestThisComputersAPIKeyBecomesTheDefaultAlsoBesideALogin(t *testing.T) {
	h := setup(t)
	h.helper.native["codex"] = nativeSource{fingerprint: "f1", login: signIn("codex", "me@example.com")}
	h.helper.native["codex key"] = nativeSource{fingerprint: "k1", login: nativeKey("codex", "config-index:codex:0")}
	h.svc.importNative(h.ctx, true)
	state := h.store.get()
	if len(state.Accounts) != 2 {
		t.Fatalf("accounts=%+v", state.Accounts)
	}
	login, key := state.Accounts[0], state.Accounts[1]
	if !key.APIKey() || key.Email != "Global API key" || state.Defaults["codex"] != key.ID || !h.view(key.ID).Global || !h.view(login.ID).Global {
		t.Fatalf("key=%+v defaults=%v", key, state.Defaults)
	}
	if receipt := state.NativeKeyImports["codex"]; receipt.Fingerprint != "k1" || receipt.AccountID != key.ID {
		t.Fatalf("receipt=%+v", receipt)
	}
	// Imported once: another default, and a later sign-out, stand.
	h.assign("s1", domain.HarnessCodex, key.ID)
	if err := h.act(login.ID, "primary"); err != nil {
		t.Fatal(err)
	}
	h.svc.importNative(h.ctx, true)
	if h.defaultOf("codex") != login.ID || len(h.helper.imports) != 2 {
		t.Fatalf("default=%q imports=%v", h.defaultOf("codex"), h.helper.imports)
	}
	// A changed key takes the place of the old one in the same account.
	h.helper.native["codex key"] = nativeSource{fingerprint: "k2", login: nativeKey("codex", "config-index:codex:1")}
	h.svc.importNative(h.ctx, true)
	state = h.store.get()
	if len(state.Accounts) != 2 || state.Accounts[1].ID != key.ID || state.Accounts[1].AuthID != "config-index:codex:1-auth" || state.Defaults["codex"] != login.ID {
		t.Fatalf("accounts=%+v defaults=%v", state.Accounts, state.Defaults)
	}
	if !reflect.DeepEqual(h.helper.deleted, []string{"config-index:codex:0"}) || !h.view(key.ID).Global {
		t.Fatalf("deleted=%v", h.helper.deleted)
	}
	h.route("s1", key.ID)
	if err := h.act(key.ID, "remove"); err != nil {
		t.Fatal(err)
	}
	h.svc.importNative(h.ctx, true)
	if len(h.store.get().Accounts) != 1 {
		t.Fatal("a removed key came back")
	}
}

func TestAnAPIKeyAlreadyAddedByHandIsLeftAsItIs(t *testing.T) {
	h := setup(t)
	byHand, err := h.svc.record(h.ctx, "claude", nativeKey("claude", "config-index:claude:0"), "")
	if err != nil {
		t.Fatal(err)
	}
	second := h.signIn("claude", "alice@example.com")
	if err = h.act(second, "primary"); err != nil {
		t.Fatal(err)
	}
	before := h.store.get()
	h.helper.native["claude key"] = nativeSource{fingerprint: "k1", err: ports.ErrProviderAccountConflict}
	h.svc.importNative(h.ctx, true)
	h.svc.importNative(h.ctx, true)
	state := h.store.get()
	if !reflect.DeepEqual(state.Accounts, before.Accounts) || state.Defaults["claude"] != second || h.view(byHand).Global {
		t.Fatalf("state=%+v", state)
	}
	if state.NativeKeyImports["claude"] != (domain.NativeProviderImport{Fingerprint: "k1"}) || len(h.helper.imports) != 1 || len(h.helper.deleted) != 0 {
		t.Fatalf("receipt=%+v imports=%v deleted=%v", state.NativeKeyImports["claude"], h.helper.imports, h.helper.deleted)
	}
}

// The helper renews a copied sign-in, which signs the computer's own agent out
// once. So a login is copied only while AO does not know whose it is.
func TestALoginAOAlreadyKnowsIsNeverCopiedAgain(t *testing.T) {
	owned := func(email string) nativeSource {
		return nativeSource{fingerprint: "id:" + strings.ToLower(email), login: signIn("claude", email)}
	}
	t.Run("an account added by hand already has that owner", func(t *testing.T) {
		h := setup(t)
		h.signIn("claude", "alice@example.com")
		h.helper.native["claude"] = owned("Alice@Example.com")
		before := h.store.get()
		h.svc.importNative(h.ctx, true)
		if h.unchanged(before, "a login AO already holds"); len(h.helper.imports) != 0 {
			t.Fatalf("copied: %v", h.helper.imports)
		}
	})
	t.Run("copied once, then left alone through renewals, sign-out and removal", func(t *testing.T) {
		h := setup(t)
		h.helper.native["claude"] = owned("bob@example.com")
		h.svc.importNative(h.ctx, true)
		state := h.store.get()
		if len(h.helper.imports) != 1 || len(state.Accounts) != 1 || state.NativeImports["claude"].Fingerprint != "id:bob@example.com" {
			t.Fatalf("imports=%v state=%+v", h.helper.imports, state)
		}
		for _, action := range []string{"", "sign-out", "remove"} {
			if action != "" {
				if err := h.act(state.Accounts[0].ID, action); err != nil {
					t.Fatal(err)
				}
			}
			if h.svc.importNative(h.ctx, true); len(h.helper.imports) != 1 {
				t.Fatalf("after %q the login was copied again: %v", action, h.helper.imports)
			}
		}
		// Somebody else signing in to the agent is a login AO does not have.
		h.helper.native["claude"] = owned("carol@example.com")
		if h.svc.importNative(h.ctx, true); len(h.helper.imports) != 2 || len(h.store.get().Accounts) != 1 {
			t.Fatalf("imports=%v accounts=%+v", h.helper.imports, h.store.get().Accounts)
		}
	})
	t.Run("a login that cannot say whose it is is copied the first time only", func(t *testing.T) {
		h := setup(t)
		h.helper.native["claude"] = nativeSource{unnamed: true, login: signIn("claude", "dave@example.com")}
		h.svc.importNative(h.ctx, true)
		h.svc.importNative(h.ctx, true)
		if receipt := h.store.get().NativeImports["claude"]; len(h.helper.imports) != 1 || receipt.Fingerprint != "id:dave@example.com" {
			t.Fatalf("imports=%v receipt=%+v", h.helper.imports, receipt)
		}
	})
}
