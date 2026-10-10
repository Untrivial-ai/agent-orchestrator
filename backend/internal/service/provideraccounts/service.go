// Package provideraccounts owns the managed accounts and the sessions routed to them.
package provideraccounts

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Service is Account Manager. The stored document is the truth; the helper holds a copy.
type Service struct {
	store    ports.ProviderAccountStore
	helper   ports.AccountHelper
	newID    func() string
	now      func() time.Time
	tick     time.Duration
	changed  func()
	mu       sync.Mutex // one account change at a time
	starting sync.Mutex // one sign-in start or import at a time
	memo     sync.Mutex // guards the fields below
	usage    map[string]domain.ProviderAccountUsage
	moved    map[string]*domain.ProviderAccountMove // by the account the sessions left
	failed   map[string]bool
	stamps   map[string]time.Time
	logins   map[string]*attempt
}

type attempt struct {
	mu       sync.Mutex
	login    ports.ProviderLogin
	deadline time.Time
}

// New creates the account service.
func New(store ports.ProviderAccountStore, helper ports.AccountHelper, newID func() string) *Service {
	return &Service{store: store, helper: helper, newID: newID, now: time.Now, tick: 10 * time.Second, changed: func() {},
		usage: map[string]domain.ProviderAccountUsage{}, moved: map[string]*domain.ProviderAccountMove{}, stamps: map[string]time.Time{}, logins: map[string]*attempt{}}
}

// OnChange sets what is called when the accounts a provider can use change.
func (s *Service) OnChange(fn func()) { s.changed = fn }

// Run keeps the helper and the accounts current until ctx ends; migrate reports the sessions left to move.
func (s *Service) Run(ctx context.Context, log *slog.Logger, migrate func(context.Context) (int, error)) {
	s.importNative(ctx, false)
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for n, left, failing := 0, 1, false; ; n++ {
		err := s.Sync(ctx)
		if err != nil && !failing && ctx.Err() == nil {
			log.Warn("managed account helper requires attention", "error", err)
		}
		if failing = err != nil; !failing && n == 0 {
			_, _ = s.Accounts(ctx, true, false)
		}
		if !failing && n%6 == 5 { // once a minute
			if err = s.switchOnLimit(ctx); err != nil && ctx.Err() == nil {
				log.Warn("moving sessions off an account that reached its limit", "error", err)
			}
		}
		// 20 seconds after start, then every 30, while sessions remain.
		if !failing && migrate != nil && left > 0 && n%3 == 2 {
			if left, err = migrate(ctx); err != nil && ctx.Err() == nil {
				log.Warn("moving sessions onto Account Manager", "error", err)
				left = 1
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// change applies fn to the stored accounts, telling the helper first so that
// its refusal writes nothing. fn returns a credential to delete once unused.
func (s *Service) change(ctx context.Context, fn func(*domain.ProviderAccountState) (string, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.store.LoadProviderAccounts(ctx)
	if err != nil {
		return err
	}
	st := domain.ProviderAccountState{Defaults: map[string]string{}, NativeImports: map[string]domain.NativeProviderImport{}, NativeKeyImports: map[string]domain.NativeProviderImport{}}
	was := encode(stored)
	_ = json.Unmarshal([]byte(was), &st)
	had := encode(st.Accounts)
	drop, err := fn(&st)
	if err == nil && encode(st) != was {
		if err = s.push(ctx, st); err == nil {
			err = s.store.SaveProviderAccounts(ctx, st)
		}
	}
	if err != nil {
		return err
	}
	if drop != "" && !uses(st, drop) {
		err = s.helper.DeleteCredential(ctx, drop)
	}
	if encode(st.Accounts) != had {
		s.changed()
	}
	return err
}

// Sync refills the helper's table and now and then deletes sign-ins no account names; no accounts, no helper.
func (s *Service) Sync(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.LoadProviderAccounts(ctx)
	if err != nil || len(st.Accounts)+len(st.Routes) == 0 {
		return err
	}
	if err = s.push(ctx, st); err != nil || !s.due("sweep", 10*time.Minute, false) {
		return err
	}
	held := s.credentials(ctx)
	keys := s.keysSweepable(st, held)
	for _, c := range held {
		old := !c.ModifiedAt.IsZero() && s.now().Sub(c.ModifiedAt) >= 15*time.Minute
		if (old || keys && strings.HasPrefix(c.Name, "config-index:")) && !uses(st, c.Name) && slices.Contains(domain.AccountProviders, c.Provider) {
			_ = s.helper.DeleteCredential(ctx, c.Name)
		}
	}
	return nil
}

// keysSweepable reports whether an API key no account names can only be a leftover: no sign-in
// or import has begun since the daemon started, and every key account's key is in the listing.
func (s *Service) keysSweepable(st domain.ProviderAccountState, held []ports.ProviderCredential) bool {
	if !s.starting.TryLock() {
		return false
	}
	defer s.starting.Unlock()
	s.memo.Lock()
	defer s.memo.Unlock()
	return len(s.logins) == 0 && !slices.ContainsFunc(st.Accounts, func(a domain.ProviderAccount) bool {
		return a.APIKey() && a.SignedIn() && !slices.ContainsFunc(held, func(c ports.ProviderCredential) bool { return c.Name == a.CredentialRef })
	})
}
func (s *Service) push(ctx context.Context, st domain.ProviderAccountState) error {
	auth, ids := map[string]string{}, make([]string, 0, len(st.Accounts))
	for _, a := range st.Accounts {
		if a.SignedIn() {
			auth[a.ID], ids = a.AuthID, append(ids, a.AuthID)
		}
	}
	routes := make([]ports.ProviderRoute, 0, len(st.Routes))
	for _, r := range st.Routes {
		hash, err := s.ticketHash(r.SessionID)
		if err != nil {
			return err
		}
		routes = append(routes, ports.ProviderRoute{TicketHash: hash, Provider: r.Provider, AuthID: auth[r.AccountID]})
	}
	return s.helper.ApplyRoutes(ctx, routes, ids)
}

// ticketHash is the name the helper knows a session by.
func (s *Service) ticketHash(id domain.SessionID) (string, error) {
	token, err := s.ticket(id)
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:]), err
}
func (s *Service) ticket(id domain.SessionID) (string, error) {
	key, err := s.helper.TicketKey()
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("ao-provider-session\x00" + string(id)))
	return hex.EncodeToString(mac.Sum(nil)), err
}
func encode(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
func index(st domain.ProviderAccountState, id string) int {
	return slices.IndexFunc(st.Accounts, func(a domain.ProviderAccount) bool { return a.ID == id })
}
func uses(st domain.ProviderAccountState, credentialRef string) bool {
	return slices.ContainsFunc(st.Accounts, func(a domain.ProviderAccount) bool { return a.CredentialRef == credentialRef })
}
func routed(st domain.ProviderAccountState, id domain.SessionID) int {
	return slices.IndexFunc(st.Routes, func(r domain.ProviderSessionRoute) bool { return r.SessionID == id })
}

// due reports that a paced check should run now, and stamps it.
func (s *Service) due(name string, every time.Duration, force bool) bool {
	s.memo.Lock()
	defer s.memo.Unlock()
	if !force && s.now().Sub(s.stamps[name]) < every {
		return false
	}
	s.stamps[name] = s.now()
	return true
}

// dead reports a sign-in the provider refuses or, with ending, one that has stopped renewing.
func (s *Service) dead(authID string, ending bool) bool {
	s.memo.Lock()
	defer s.memo.Unlock()
	return s.failed[authID] || ending && s.usage[authID].SignInEnding
}

// credentials re-reads which sign-ins the provider refuses; an outage keeps the last verdict.
func (s *Service) credentials(ctx context.Context) []ports.ProviderCredential {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := s.helper.Credentials(ctx)
	if err != nil {
		return nil
	}
	failed := map[string]bool{}
	for _, c := range list {
		if c.Failed {
			failed[c.AuthID] = true
		}
	}
	s.memo.Lock()
	defer s.memo.Unlock()
	if !maps.Equal(s.failed, failed) {
		go s.changed() // Not inline: a readiness check may have asked for this.
	}
	s.failed = failed
	return list
}
func (s *Service) check(ctx context.Context, st domain.ProviderAccountState, force bool) {
	if slices.ContainsFunc(st.Accounts, domain.ProviderAccount.SignedIn) && s.due("sign-in", 30*time.Second, force) {
		s.credentials(ctx)
	}
}

// usable says why an account cannot serve a provider's sessions, or nil.
func (s *Service) usable(ctx context.Context, st domain.ProviderAccountState, id, provider string) error {
	s.check(ctx, st, false)
	i := index(st, id)
	switch {
	case id != "" && i < 0:
		return ports.ErrProviderAccountUnknown
	case id != "" && st.Accounts[i].Provider != provider:
		return ports.ErrProviderAccountIncompatible
	case id == "" || !st.Accounts[i].SignedIn() || s.dead(st.Accounts[i].AuthID, false):
		return ports.ErrProviderLoginRequired
	}
	return nil
}

// pick is the chosen account, or the provider's default, when it is usable.
func (s *Service) pick(ctx context.Context, provider, chosen string) (domain.ProviderAccount, error) {
	st, err := s.store.LoadProviderAccounts(ctx)
	id := cmp.Or(chosen, st.Defaults[provider])
	if err == nil {
		err = s.usable(ctx, st, id, provider)
	}
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	return st.Accounts[index(st, id)], nil
}

// makeDefault also gives the provider's waiting sessions the account.
func makeDefault(st *domain.ProviderAccountState, provider, id string) {
	st.Defaults[provider] = id
	for i, r := range st.Routes {
		if r.Provider == provider && r.AccountID == "" {
			st.Routes[i].AccountID = id
		}
	}
}

// ResolveAccount picks the explicit account, or the provider's default, for a new session.
func (s *Service) ResolveAccount(ctx context.Context, harness domain.AgentHarness, explicit string) (string, bool, error) {
	provider := domain.AccountProvider(harness)
	if provider == "" && explicit == "" {
		return "", false, nil
	}
	a, err := s.pick(ctx, provider, explicit)
	return a.ID, provider != "", err
}

// AssignAccount routes a new session to an account.
func (s *Service) AssignAccount(ctx context.Context, id domain.SessionID, harness domain.AgentHarness, accountID string) error {
	provider := domain.AccountProvider(harness)
	return s.change(ctx, func(st *domain.ProviderAccountState) (string, error) {
		if routed(*st, id) >= 0 {
			return "", ports.ErrProviderAccountConflict
		}
		st.Routes = append(st.Routes, domain.ProviderSessionRoute{SessionID: id, Provider: provider, AccountID: accountID})
		return "", s.usable(ctx, *st, accountID, provider)
	})
}

// SessionAccount reads a session's route; false is a session AO does not route.
func (s *Service) SessionAccount(ctx context.Context, id domain.SessionID) (domain.ProviderSessionRoute, bool, error) {
	st, err := s.store.LoadProviderAccounts(ctx)
	if i := routed(st, id); err == nil && i >= 0 {
		return st.Routes[i], true, nil
	}
	return domain.ProviderSessionRoute{}, false, err
}

// LaunchAccountEnv is the helper address and ticket a routed session keeps for life.
func (s *Service) LaunchAccountEnv(ctx context.Context, id domain.SessionID) (map[string]string, error) {
	route, managed, err := s.SessionAccount(ctx, id)
	if err != nil || !managed {
		return nil, err
	}
	token, err := s.ticket(id)
	if err != nil {
		return nil, err
	}
	if route.Provider == "codex" {
		return map[string]string{"AO_PROXY_ENDPOINT": s.helper.Endpoint(), "AO_PROXY_TICKET": token}, nil
	}
	return map[string]string{"AO_PROXY_ENDPOINT": "", "AO_PROXY_TICKET": "", "ANTHROPIC_BASE_URL": s.helper.Endpoint(), "ANTHROPIC_AUTH_TOKEN": token, "ANTHROPIC_API_KEY": "", "CLAUDE_CODE_OAUTH_TOKEN": "", "CLAUDE_CODE_USE_BEDROCK": "", "CLAUDE_CODE_USE_VERTEX": "", "CLAUDE_CODE_USE_FOUNDRY": ""}, nil
}

// ForgetAccount drops a deleted session's route.
func (s *Service) ForgetAccount(ctx context.Context, id domain.SessionID) error {
	return s.change(ctx, func(st *domain.ProviderAccountState) (string, error) {
		st.Routes = slices.DeleteFunc(st.Routes, func(r domain.ProviderSessionRoute) bool { return r.SessionID == id })
		return "", nil
	})
}

// AdoptSession routes an older session to its provider's default, or leaves it waiting for a sign-in.
func (s *Service) AdoptSession(ctx context.Context, id domain.SessionID, harness domain.AgentHarness) (bool, error) {
	provider, adopted := domain.AccountProvider(harness), false
	s.importNative(ctx, false)
	err := s.change(ctx, func(st *domain.ProviderAccountState) (string, error) {
		if adopted = provider != "" && routed(*st, id) < 0; adopted {
			st.Routes = append(st.Routes, domain.ProviderSessionRoute{SessionID: id, Provider: provider, AccountID: st.Defaults[provider]})
		}
		return "", nil
	})
	return adopted && err == nil, err
}

// Act applies one account action and returns a reset's outcome.
func (s *Service) Act(ctx context.Context, id string, in ports.ProviderAccountAction) (string, error) {
	if slices.Contains([]string{ports.AccountActionReset, ports.AccountActionResume, ports.AccountActionRefresh}, in.Action) {
		return s.helperAction(ctx, id, in.Action)
	}
	return "", s.change(ctx, func(st *domain.ProviderAccountState) (string, error) {
		i := index(*st, id)
		if i < 0 {
			return "", ports.ErrProviderAccountUnknown
		}
		a, name := &st.Accounts[i], strings.Join(strings.Fields(in.DisplayName), " ")
		switch in.Action {
		case "rename":
			if n := utf8.RuneCountInString(name); n == 0 || n > 80 {
				return "", ports.ErrProviderAccountNameInvalid
			}
			a.DisplayName = name
			return "", nil
		case "primary":
			makeDefault(st, a.Provider, id)
			a.Reserved = false
			return "", s.usable(ctx, *st, id, a.Provider)
		case "settings":
			if in.Reserved != nil { // The default is what new sessions use, so it cannot be held back.
				a.Reserved = *in.Reserved && st.Defaults[a.Provider] != id
			}
			if in.OnLimit != nil {
				if j := index(*st, *in.OnLimit); *in.OnLimit != "" && (j < 0 || *in.OnLimit == id || st.Accounts[j].Provider != a.Provider) {
					return "", ports.ErrProviderAccountIncompatible
				}
				a.OnLimit = *in.OnLimit
			}
			if in.WarnAt != nil {
				a.WarnAt = min(max(*in.WarnAt, 0), 90)
			}
			return "", nil
		case "assign-session":
			j := routed(*st, domain.SessionID(in.SessionID))
			if j < 0 {
				return "", ports.ErrProviderAccountIncompatible
			}
			st.Routes[j].AccountID = id
			return "", s.usable(ctx, *st, id, st.Routes[j].Provider)
		case "sign-out", "remove":
			return s.remove(ctx, st, i, in.ReplacementPrimaryID, in.Action == "sign-out")
		}
		return "", ports.ErrProviderAccountActionUnavailable
	})
}

// remove signs an account out, or deletes it, and moves its sessions to the provider's default.
func (s *Service) remove(ctx context.Context, st *domain.ProviderAccountState, i int, replacement string, signOut bool) (string, error) {
	a, next := st.Accounts[i], st.Defaults[st.Accounts[i].Provider]
	if next == a.ID {
		next = ""
		if slices.ContainsFunc(st.Accounts, func(o domain.ProviderAccount) bool { return o.ID != a.ID && o.Provider == a.Provider && o.SignedIn() }) {
			if replacement == "" || replacement == a.ID {
				return "", ports.ErrProviderPrimaryRequired
			}
			if err := s.usable(ctx, *st, replacement, a.Provider); err != nil {
				return "", err
			}
			next = replacement
		}
		st.Defaults[a.Provider] = next
	}
	for j, r := range st.Routes {
		if r.AccountID == a.ID {
			st.Routes[j].AccountID = next
		}
	}
	if signOut {
		st.Accounts[i].CredentialRef, st.Accounts[i].AuthID = "", ""
	} else {
		st.Accounts = slices.Delete(st.Accounts, i, i+1)
		for j := range st.Accounts {
			if st.Accounts[j].OnLimit == a.ID {
				st.Accounts[j].OnLimit = ""
			}
		}
	}
	return a.CredentialRef, nil
}

// spent reports a reading whose general limit has nothing left.
func spent(u domain.ProviderAccountUsage) bool {
	return slices.ContainsFunc(u.Windows, func(w domain.ProviderAccountUsageWindow) bool { return w.Scope == "" && w.RemainingFraction <= 0 })
}

// switchOnLimit moves the sessions of each account that has run out onto the
// account it names, when that one is signed in and has something left. A default
// that ran out hands the default on as well, so new sessions follow.
func (s *Service) switchOnLimit(ctx context.Context) error {
	st, err := s.store.LoadProviderAccounts(ctx)
	if err != nil || !slices.ContainsFunc(st.Accounts, func(a domain.ProviderAccount) bool { return a.OnLimit != "" }) {
		return err
	}
	used := s.usages(ctx, st.Accounts)
	var failures []error
	for _, from := range st.Accounts {
		next, known := used[from.OnLimit]
		if from.OnLimit == "" || !spent(used[from.ID]) || !known || next.Status != "available" || spent(next) {
			continue
		}
		sessions := 0
		err := s.change(ctx, func(st *domain.ProviderAccountState) (string, error) {
			to := index(*st, from.OnLimit)
			if err := s.usable(ctx, *st, from.OnLimit, from.Provider); err != nil {
				return "", err
			}
			for i, r := range st.Routes {
				if r.AccountID == from.ID {
					st.Routes[i].AccountID, sessions = from.OnLimit, sessions+1
				}
			}
			if st.Defaults[from.Provider] == from.ID {
				st.Defaults[from.Provider], st.Accounts[to].Reserved = from.OnLimit, false
			}
			return "", nil
		})
		if failures = append(failures, err); err == nil && sessions > 0 {
			s.memo.Lock()
			s.moved[from.ID] = &domain.ProviderAccountMove{To: from.OnLimit, At: s.now().UTC().Format(time.RFC3339), Sessions: sessions}
			s.memo.Unlock()
		}
	}
	return errors.Join(failures...)
}

func (s *Service) helperAction(ctx context.Context, id, action string) (string, error) {
	st, err := s.store.LoadProviderAccounts(ctx)
	i := index(st, id)
	switch {
	case err != nil:
		return "", err
	case i < 0:
		return "", ports.ErrProviderAccountUnknown
	case !st.Accounts[i].SignedIn():
		return "", ports.ErrProviderLoginRequired
	}
	outcome, err := s.helper.AccountAction(ctx, st.Accounts[i], action, s.newID())
	s.due("usage "+st.Accounts[i].AuthID, 0, true) // The cached reading is now out of date.
	if action == ports.AccountActionRefresh {
		s.check(ctx, st, true) // A refused renewal is the verdict that the sign-in is dead.
	}
	return outcome, err
}

// record saves a verified sign-in as a new account, or onto the one signing in again.
func (s *Service) record(ctx context.Context, provider string, v ports.VerifiedProviderLogin, relogin string) (string, error) {
	id := relogin
	err := s.change(ctx, func(st *domain.ProviderAccountState) (drop string, _ error) {
		i := slices.IndexFunc(st.Accounts, func(a domain.ProviderAccount) bool {
			return a.ID == relogin || relogin == "" && a.Provider == provider && strings.EqualFold(a.Email, v.Email)
		})
		switch {
		case v.Provider != provider || strings.TrimSpace(v.Email) == "" || v.CredentialRef == "" || v.AuthID == "":
			return "", ports.ErrProviderAccountIncompatible
		case i < 0 && relogin != "":
			return "", ports.ErrProviderAccountUnknown
		case i < 0:
			id = s.add(st, v)
		case st.Accounts[i].Provider != provider || !strings.EqualFold(st.Accounts[i].Email, v.Email):
			return "", ports.ErrProviderAccountIncompatible
		case st.Accounts[i].CredentialRef == v.CredentialRef && st.Accounts[i].AuthID == v.AuthID:
			id = st.Accounts[i].ID
		case relogin == "" || st.Accounts[i].SignedIn() && !s.dead(st.Accounts[i].AuthID, true):
			return "", ports.ErrProviderAccountConflict
		default: // A dead or ending sign-in is replaced; its sessions keep the account.
			a := &st.Accounts[i]
			drop, a.Email, a.Kind, a.CredentialRef, a.AuthID = a.CredentialRef, v.Email, v.Kind, v.CredentialRef, v.AuthID
		}
		if st.Defaults[provider] == "" {
			makeDefault(st, provider, id)
		}
		return drop, nil
	})
	return id, err
}

// add appends a new account under a generated name no other account has.
func (s *Service) add(st *domain.ProviderAccountState, v ports.VerifiedProviderLogin) string {
	a := domain.ProviderAccount{ID: s.newID(), Provider: v.Provider, Email: v.Email, Kind: v.Kind, CredentialRef: v.CredentialRef, AuthID: v.AuthID}
	base := domain.GeneratedProviderAccountName(a.Provider, a.ID)
	a.DisplayName = base
	for n := 2; slices.ContainsFunc(st.Accounts, func(o domain.ProviderAccount) bool { return o.DisplayName == a.DisplayName }); n++ {
		a.DisplayName = fmt.Sprintf("%s %d", base, n)
	}
	st.Accounts = append(st.Accounts, a)
	return a.ID
}

// discard deletes a sign-in the helper saved, unless an account uses it.
func (s *Service) discard(ctx context.Context, ref string) error {
	return s.change(ctx, func(*domain.ProviderAccountState) (string, error) { return ref, nil })
}

// importNative makes this computer's own logins and API keys accounts, once each.
func (s *Service) importNative(ctx context.Context, force bool) {
	s.starting.Lock() // two imports at once would each take the other's key for one added by hand
	defer s.starting.Unlock()
	if !s.due("native", 5*time.Minute, force) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Keys last: a key beside a login becomes the default, as the agent uses it first.
	for _, key := range []bool{false, true} {
		for _, provider := range domain.AccountProviders {
			s.importOne(ctx, provider, key)
		}
	}
}
func receipts(st *domain.ProviderAccountState, key bool) map[string]domain.NativeProviderImport {
	return map[bool]map[string]domain.NativeProviderImport{false: st.NativeImports, true: st.NativeKeyImports}[key]
}
func (s *Service) importOne(ctx context.Context, provider string, key bool) {
	stored, err := s.store.LoadProviderAccounts(ctx)
	if err != nil {
		return
	}
	receipt := receipts(&stored, key)[provider]
	// A copied sign-in is renewed by the helper, which signs the computer's own agent out once.
	// So one AO has copied before, or whose account it already holds, is never copied again.
	known := func(identity string) bool {
		email, signIn := strings.CutPrefix(identity, "id:")
		if !signIn { // A key, or a sign-in that cannot say whose it is: that one is copied the first time only.
			return receipt.Fingerprint != "" && (identity == "" || identity == receipt.Fingerprint)
		}
		return strings.EqualFold(receipt.Email, email) || slices.ContainsFunc(stored.Accounts, func(a domain.ProviderAccount) bool {
			return a.Provider == provider && !a.APIKey() && strings.EqualFold(a.Email, email)
		})
	}
	v, found, err := s.helper.ImportNative(ctx, provider, key, known)
	byHand := errors.Is(err, ports.ErrProviderAccountConflict)
	if found == "" || !byHand && (err != nil || v.AuthID == "") {
		return // Nothing new, or nothing readable: the accounts stay as they are.
	}
	v.Provider, v.Kind = provider, map[bool]string{false: "oauth", true: "api_key"}[key]
	_ = s.change(ctx, func(st *domain.ProviderAccountState) (string, error) {
		receipt, id, drop := receipts(st, key)[provider], "", v.CredentialRef
		i := slices.IndexFunc(st.Accounts, func(a domain.ProviderAccount) bool {
			return a.Provider == provider && a.APIKey() == key && (key && a.ID == receipt.AccountID || !key && strings.EqualFold(a.Email, v.Email))
		})
		switch {
		case receipt.Fingerprint == found:
			return drop, nil
		case byHand: // A key added by hand stays that account; it is only not looked at again.
			receipt = domain.NativeProviderImport{}
		case !key && receipt.Email != "" && strings.EqualFold(receipt.Email, v.Email):
			// The same login with renewed tokens: a removal or sign-out stands.
		case i < 0:
			if slices.ContainsFunc(st.Accounts, func(a domain.ProviderAccount) bool {
				return a.Provider == provider && strings.EqualFold(a.Email, v.Email)
			}) {
				return "", ports.ErrProviderAccountConflict
			}
			id, drop = s.add(st, v), ""
		case key || !st.Accounts[i].SignedIn(): // A changed key takes the place of the one before it.
			a := &st.Accounts[i]
			id, drop, a.CredentialRef, a.AuthID = a.ID, a.CredentialRef, v.CredentialRef, v.AuthID
		default: // Its saved sign-in may hold a newer refresh token: keep it.
			id = st.Accounts[i].ID
		}
		if id != "" {
			receipt.AccountID, receipt.Email = id, v.Email
			if st.Defaults[provider] == "" || key && i < 0 {
				makeDefault(st, provider, id)
			}
		}
		receipt.Fingerprint = found
		receipts(st, key)[provider] = receipt
		return drop, nil
	})
}

// usages reads each signed-in account's usage; a failed reading is retried sooner.
func (s *Service) usages(ctx context.Context, accounts []domain.ProviderAccount) map[string]domain.ProviderAccountUsage {
	var wg sync.WaitGroup
	for _, a := range accounts {
		s.memo.Lock()
		fresh := s.now().Before(s.stamps["usage "+a.AuthID])
		s.memo.Unlock()
		if fresh || !a.SignedIn() {
			continue
		}
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			usage, err := s.helper.AccountUsage(ctx, a)
			ttl := 5 * time.Minute // A provider asked more often than this starts to refuse.
			if err == nil {
				if models, err := s.helper.AccountModels(ctx, a); err == nil {
					for _, m := range models {
						usage.Models = append(usage.Models, cmp.Or(m.Label, m.ID))
					}
				}
			}
			s.memo.Lock()
			defer s.memo.Unlock()
			last, good := s.usage[a.AuthID], "good "+a.AuthID
			switch {
			case err == nil:
				s.stamps[good] = s.now()
			case last.Status == "available" && s.now().Sub(s.stamps[good]) < 30*time.Minute:
				// A provider that did not answer this time: its last reading stands for half an hour, with what the helper knows now.
				last.Activity, last.Health, last.Requests = usage.Activity, usage.Health, usage.Requests
				usage, ttl = last, time.Minute
			default:
				usage.Status, ttl = "unavailable", time.Minute
			}
			s.usage[a.AuthID], s.stamps["usage "+a.AuthID] = usage, s.now().Add(ttl)
		})
	}
	wg.Wait()
	s.memo.Lock()
	defer s.memo.Unlock()
	result := map[string]domain.ProviderAccountUsage{}
	for _, a := range accounts {
		if reading, ok := s.usage[a.AuthID]; ok && a.SignedIn() {
			result[a.ID] = reading
		}
	}
	return result
}

// Accounts lists every account as the API shows it.
func (s *Service) Accounts(ctx context.Context, usage, refresh bool) ([]domain.ProviderAccountView, error) {
	s.importNative(ctx, refresh)
	st, err := s.store.LoadProviderAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var used map[string]domain.ProviderAccountUsage
	if usage {
		used = s.usages(ctx, st.Accounts)
	}
	// After usage: a refused usage request makes the helper re-check that sign-in.
	s.check(ctx, st, refresh)
	views := make([]domain.ProviderAccountView, 0, len(st.Accounts))
	for _, a := range st.Accounts {
		own := st.NativeImports[a.Provider] // This computer's own login, if it has one: the account it became, or one under the same email.
		view := domain.ProviderAccountView{ID: a.ID, Provider: a.Provider, DisplayName: a.DisplayName, Email: a.Email, Kind: a.Kind,
			Global:   st.NativeKeyImports[a.Provider].AccountID == a.ID || !a.APIKey() && (own.AccountID == a.ID || strings.EqualFold(a.Email, cmp.Or(own.Email, "\x00"))),
			SignedIn: a.SignedIn() && !s.dead(a.AuthID, false), Primary: st.Defaults[a.Provider] == a.ID, Sessions: []string{},
			Reserved: a.Reserved, OnLimit: a.OnLimit, WarnAt: a.WarnAt}
		s.memo.Lock()
		view.Moved = s.moved[a.ID]
		s.memo.Unlock()
		var byHash map[string]int64 // The helper knows a session by its ticket; the page knows it by its id.
		if reading, read := used[a.ID]; read {
			if view.Usage = &reading; reading.Activity != nil {
				activity := *reading.Activity
				byHash, activity.Sessions, reading.Activity = activity.Sessions, map[string]int64{}, &activity
			}
		}
		for _, r := range st.Routes {
			if r.AccountID != a.ID {
				continue
			}
			view.Sessions = append(view.Sessions, string(r.SessionID))
			if hash, err := s.ticketHash(r.SessionID); err == nil && byHash[hash] > 0 {
				view.Usage.Activity.Sessions[string(r.SessionID)] = byHash[hash]
			}
		}
		views = append(views, view)
	}
	return views, nil
}

// AuthenticationReadiness answers whether a managed provider has a working sign-in.
func (s *Service) AuthenticationReadiness(ctx context.Context, harness domain.AgentHarness, _ domain.AgentReadinessPurpose) (domain.AgentAuthenticationObservation, bool) {
	provider, now := domain.AccountProvider(harness), time.Now().UTC()
	if provider == "" {
		return domain.AgentAuthenticationObservation{}, false
	}
	st, err := s.store.LoadProviderAccounts(ctx)
	if err != nil {
		return domain.AgentAuthenticationObservation{State: domain.AgentAuthenticationUnknown, Freshness: domain.AgentReadinessStale, AttemptedAt: &now,
			ReasonCode: domain.AgentReadinessReasonAuthCheckFailed, Reason: "Managed account status could not be read."}, true
	}
	result := domain.AgentAuthenticationObservation{State: domain.AgentAuthenticationUnauthorized, Freshness: domain.AgentReadinessFresh, CheckedAt: &now, AttemptedAt: &now,
		ReasonCode: domain.AgentReadinessReasonUnauthorized, Reason: "Sign in through Account Manager to use this provider."}
	s.check(ctx, st, false)
	for _, a := range st.Accounts {
		switch {
		case a.Provider != provider || !a.SignedIn():
		case s.dead(a.AuthID, false):
			result.Reason = "Sign in again through Account Manager to use this provider."
		default:
			result.State, result.ReasonCode, result.Reason = domain.AgentAuthenticationAuthorized, domain.AgentReadinessReasonAuthorized, "A managed account is signed in."
			return result, true
		}
	}
	return result, true
}

// scoped is the account a model scope names; false for other harnesses and cloud credentials.
func (s *Service) scoped(ctx context.Context, harness domain.AgentHarness, scope string) (domain.ProviderAccount, bool, error) {
	provider := domain.AccountProvider(harness)
	if provider == "" || strings.HasPrefix(strings.TrimSpace(scope), "@cred:") {
		return domain.ProviderAccount{}, false, nil
	}
	chosen, ok := ports.AccountFromModelCatalogScope(scope)
	a, err := s.pick(ctx, provider, map[bool]string{true: chosen}[ok])
	return a, true, err
}

// DiscoverModels lists the models the scope's account can use.
func (s *Service) DiscoverModels(ctx context.Context, harness domain.AgentHarness, scope string) (ports.AgentModelCatalog, bool, error) {
	a, handled, err := s.scoped(ctx, harness, scope)
	if !handled || err != nil {
		return ports.AgentModelCatalog{}, handled, err
	}
	models, err := s.helper.AccountModels(ctx, a)
	return ports.AgentModelCatalog{AgentID: string(harness), SelectionMode: ports.ModelSelectionCatalog, Models: models, CustomModelEntry: ports.CustomModelEntryDirect,
		AllowCustom: true, Source: ports.ModelCatalogSourceManagedAccount, FetchedAt: time.Now().UTC()}, true, err
}

// ModelsFingerprint changes when another account or sign-in becomes the scope's.
func (s *Service) ModelsFingerprint(ctx context.Context, harness domain.AgentHarness, scope string) (string, bool, error) {
	a, handled, err := s.scoped(ctx, harness, scope)
	return strings.Trim(a.ID+":"+a.AuthID, ":"), handled, err
}

// StartLogin starts a sign-in, or returns the provider's waiting one when it is the same.
func (s *Service) StartLogin(ctx context.Context, in ports.ProviderLoginRequest) (login ports.ProviderLogin, err error) {
	in.Mode = cmp.Or(strings.TrimSpace(in.Mode), "browser")
	switch {
	case !slices.Contains(domain.AccountProviders, in.Provider):
		return login, apierr.Invalid("PROVIDER_REQUIRED", "Choose Codex or Claude", nil)
	case in.Mode == "device" && in.Provider != "codex":
		return login, apierr.Invalid("LOGIN_MODE_UNSUPPORTED", "Device login is available for Codex only", nil)
	case in.Mode == "import" && strings.TrimSpace(in.CredentialJSON) == "":
		return login, apierr.Invalid("CREDENTIAL_JSON_REQUIRED", "Paste or choose a credential JSON file", nil)
	case in.Mode == "api_key" && (strings.TrimSpace(in.APIKey) == "" || strings.TrimSpace(in.BaseURL) == ""):
		return login, apierr.Invalid("API_KEY_FIELDS_REQUIRED", "API key and base URL are required", nil)
	case !slices.Contains([]string{"browser", "device", "import", "api_key"}, in.Mode):
		return login, apierr.Invalid("LOGIN_MODE_UNSUPPORTED", "Choose browser, device, API key, or JSON import", nil)
	}
	s.starting.Lock()
	defer s.starting.Unlock()
	// "@provider" also names the provider's latest attempt.
	current, err := s.LoginStatus(ctx, "@"+in.Provider)
	switch {
	case errors.Is(err, ports.ErrProviderLoginUnknown) || err == nil && current.Status != "waiting":
	case err != nil:
		return current, err
	case current.AccountID == in.AccountID && current.Mode == in.Mode:
		return current, nil
	default:
		return login, ports.ErrProviderAccountConflict
	}
	if st, _ := s.store.LoadProviderAccounts(ctx); in.AccountID != "" {
		if i := index(st, in.AccountID); i < 0 || st.Accounts[i].Provider != in.Provider || st.Accounts[i].SignedIn() && !s.dead(st.Accounts[i].AuthID, true) {
			return login, ports.ErrProviderAccountConflict
		}
	}
	id := s.newID()
	if login, err = s.helper.StartLogin(ctx, id, in); err != nil {
		return ports.ProviderLogin{}, err
	}
	login.ID, login.Provider, login.Mode, login.Status, login.AccountID = id, in.Provider, in.Mode, "waiting", in.AccountID
	ttl := 6 * time.Minute
	if in.Mode == "device" {
		ttl = 15 * time.Minute // as long as the provider keeps the code alive
	}
	at := &attempt{login: login, deadline: s.now().Add(ttl)}
	s.memo.Lock()
	defer s.memo.Unlock()
	s.logins[id], s.logins["@"+in.Provider] = at, at
	return login, nil
}

// LoginStatus polls a sign-in and records its account once it completes.
func (s *Service) LoginStatus(ctx context.Context, id string) (ports.ProviderLogin, error) {
	s.memo.Lock()
	at := s.logins[id]
	s.memo.Unlock()
	if at == nil {
		return ports.ProviderLogin{}, ports.ErrProviderLoginUnknown
	}
	at.mu.Lock()
	defer at.mu.Unlock()
	if at.login.Status != "waiting" {
		return at.login, nil
	}
	status, err := s.helper.LoginStatus(ctx, at.login)
	var v ports.VerifiedProviderLogin
	switch {
	case !s.now().Before(at.deadline) && (err != nil || status == "waiting"):
		err = s.end(ctx, at, "failed") // Out of time, also when the helper cannot be reached.
	case err == nil && status == "complete":
		if v, err = s.helper.LoginResult(ctx, at.login.ID); err != nil {
			break
		}
		accountID, failure := s.record(ctx, at.login.Provider, v, at.login.AccountID)
		if err = failure; err == nil {
			at.login.Status, at.login.AccountID = status, accountID
		} else if errors.Is(err, ports.ErrProviderAccountIncompatible) || errors.Is(err, ports.ErrProviderAccountConflict) || errors.Is(err, ports.ErrProviderAccountUnknown) {
			at.login.Status = "failed"
			err = errors.Join(err, s.discard(ctx, v.CredentialRef))
		}
	case err == nil:
		at.login.Status = status
	}
	return at.login, err
}

// CancelLogin ends a waiting sign-in. Any other attempt is left as it is.
func (s *Service) CancelLogin(ctx context.Context, id string) error {
	s.memo.Lock()
	at := s.logins[id]
	s.memo.Unlock()
	if at == nil {
		return nil
	}
	at.mu.Lock()
	defer at.mu.Unlock()
	if at.login.Status != "waiting" {
		return nil
	}
	return s.end(ctx, at, "cancelled")
}

// end cancels a waiting attempt and deletes a sign-in granted a moment before.
func (s *Service) end(ctx context.Context, at *attempt, status string) error {
	if err := s.helper.CancelLogin(ctx, at.login); err != nil {
		return err
	}
	at.login.Status = status
	v, _ := s.helper.LoginResult(ctx, at.login.ID) // Nothing was saved, or the sweep will find it.
	return s.discard(ctx, v.CredentialRef)
}
