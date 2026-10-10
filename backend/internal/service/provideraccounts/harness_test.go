package provideraccounts

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var errInjected = errors.New("injected failure")

func clone[T any](v T) T {
	data, _ := json.Marshal(v)
	var result T
	_ = json.Unmarshal(data, &result)
	return result
}

// memoryStore is the account document as SQLite keeps it: one JSON value.
type memoryStore struct {
	mu      sync.Mutex
	state   domain.ProviderAccountState
	loadErr error
	saveErr error
	saves   int
}

func (m *memoryStore) LoadProviderAccounts(context.Context) (domain.ProviderAccountState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(m.state), m.loadErr
}

func (m *memoryStore) SaveProviderAccounts(_ context.Context, state domain.ProviderAccountState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.state, m.saves = clone(state), m.saves+1
	return nil
}

func (m *memoryStore) get() domain.ProviderAccountState {
	state, _ := m.LoadProviderAccounts(context.Background())
	return state
}

type nativeSource struct {
	fingerprint string
	unnamed     bool // a sign-in that cannot say whose it is until it has been copied
	login       ports.VerifiedProviderLogin
	err         error
}

// fakeHelper is the account helper: it holds the route table it was last
// given and refuses one that drops an account with a request in flight.
type fakeHelper struct {
	mu        sync.Mutex
	routes    []ports.ProviderRoute
	authIDs   []string
	applies   int
	applyErr  error
	inFlight  map[string]bool
	held      []ports.ProviderCredential
	heldErr   error
	heldCalls int
	deleted   []string
	deleteErr error
	usage     domain.ProviderAccountUsage
	usageErr  error
	usageBy   map[string]domain.ProviderAccountUsage // by sign-in, in place of usage and usageErr
	usageFail map[string]bool
	usageFor  []string
	actions   []string
	outcome   string
	actionErr error
	models    []ports.AgentModelInfo
	modelsFor []string
	started   []ports.ProviderLoginRequest
	startErr  error
	status    string
	statusErr error
	results   map[string]ports.VerifiedProviderLogin
	resultErr error
	cancelled []string
	cancelErr error
	native    map[string]nativeSource
	reads     int
	imports   []string
}

func (f *fakeHelper) Endpoint() string           { return "http://127.0.0.1:1234" }
func (f *fakeHelper) TicketKey() ([]byte, error) { return []byte(strings.Repeat("k", 32)), nil }

func (f *fakeHelper) ApplyRoutes(_ context.Context, routes []ports.ProviderRoute, authIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return f.applyErr
	}
	for id := range f.inFlight {
		if !slices.Contains(authIDs, id) {
			return ports.ErrProviderAccountBusy
		}
	}
	f.routes, f.authIDs, f.applies = clone(routes), clone(authIDs), f.applies+1
	return nil
}

func (f *fakeHelper) Credentials(context.Context) ([]ports.ProviderCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heldCalls++
	return slices.Clone(f.held), f.heldErr
}

func (f *fakeHelper) DeleteCredential(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, ref)
	return nil
}

func (f *fakeHelper) AccountUsage(_ context.Context, a domain.ProviderAccount) (domain.ProviderAccountUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usageFor = append(f.usageFor, a.AuthID)
	if usage, ok := f.usageBy[a.AuthID]; ok {
		return usage, map[bool]error{true: errInjected}[f.usageFail[a.AuthID]]
	}
	return f.usage, f.usageErr
}

func (f *fakeHelper) AccountAction(_ context.Context, a domain.ProviderAccount, action, requestID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if requestID == "" {
		return "", errors.New("an action needs a request id")
	}
	f.actions = append(f.actions, action+" "+a.AuthID)
	return f.outcome, f.actionErr
}

func (f *fakeHelper) AccountModels(_ context.Context, a domain.ProviderAccount) ([]ports.AgentModelInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modelsFor = append(f.modelsFor, a.AuthID)
	return f.models, nil
}

func (f *fakeHelper) StartLogin(_ context.Context, id string, request ports.ProviderLoginRequest) (ports.ProviderLogin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return ports.ProviderLogin{}, f.startErr
	}
	f.started = append(f.started, request)
	return ports.ProviderLogin{ID: id, State: "state-" + id, URL: "https://provider.example/" + id}, nil
}

func (f *fakeHelper) LoginStatus(context.Context, ports.ProviderLogin) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.statusErr
}

func (f *fakeHelper) CancelLogin(_ context.Context, login ports.ProviderLogin) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelErr != nil {
		return f.cancelErr
	}
	f.cancelled = append(f.cancelled, login.ID)
	return nil
}

func (f *fakeHelper) LoginResult(_ context.Context, id string) (ports.VerifiedProviderLogin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.results[id]
	if f.resultErr != nil || !ok {
		return ports.VerifiedProviderLogin{}, cmp.Or(f.resultErr, errors.New("no sign-in was saved"))
	}
	return result, nil
}

func (f *fakeHelper) ImportNative(_ context.Context, provider string, apiKey bool, known func(string) bool) (ports.VerifiedProviderLogin, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := provider
	if apiKey {
		key += " key"
	}
	source, signedIn := f.native[key]
	f.reads++
	if !signedIn || source.fingerprint == "" && !source.unnamed || known(source.fingerprint) {
		return ports.VerifiedProviderLogin{}, source.fingerprint, nil
	}
	f.imports = append(f.imports, key)
	identity := source.fingerprint
	if source.unnamed {
		identity = "id:" + strings.ToLower(source.login.Email)
	}
	return source.login, identity, source.err
}

func (f *fakeHelper) complete(id string, result ports.VerifiedProviderLogin) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.results[id] = "complete", result
}

type harness struct {
	t      *testing.T
	ctx    context.Context
	svc    *Service
	store  *memoryStore
	helper *fakeHelper
	mu     sync.Mutex
	now    time.Time
	ids    int
}

func setup(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, ctx: context.Background(), store: &memoryStore{}, now: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}
	h.helper = &fakeHelper{inFlight: map[string]bool{}, status: "waiting", results: map[string]ports.VerifiedProviderLogin{}, native: map[string]nativeSource{},
		usage: domain.ProviderAccountUsage{Status: "available", Plan: "Pro"}}
	h.svc = New(h.store, h.helper, func() string {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.ids++
		return fmt.Sprintf("id-%d", h.ids)
	})
	h.svc.now = func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.now
	}
	return h
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

func signIn(provider, email string) ports.VerifiedProviderLogin {
	return ports.VerifiedProviderLogin{Provider: provider, Email: email, Kind: "oauth", CredentialRef: email + ".json", AuthID: email + "-auth"}
}

// signIn records a verified sign-in as a new account and returns the account.
func (h *harness) signIn(provider, email string) string {
	h.t.Helper()
	id, err := h.svc.record(h.ctx, provider, signIn(provider, email), "")
	if err != nil {
		h.t.Fatalf("sign in %s: %v", email, err)
	}
	return id
}

func (h *harness) assign(session string, agent domain.AgentHarness, accountID string) {
	h.t.Helper()
	if err := h.svc.AssignAccount(h.ctx, domain.SessionID(session), agent, accountID); err != nil {
		h.t.Fatalf("assign %s: %v", session, err)
	}
}

// settings applies the settings given; the others stay as they are.
func (h *harness) settings(accountID string, in ports.ProviderAccountAction) error {
	in.Action = "settings"
	_, err := h.svc.Act(h.ctx, accountID, in)
	return err
}

func (h *harness) act(accountID, action string, more ...string) error {
	in := ports.ProviderAccountAction{Action: action}
	if len(more) > 0 {
		in.ReplacementPrimaryID, in.DisplayName, in.SessionID = more[0], more[0], more[0]
	}
	_, err := h.svc.Act(h.ctx, accountID, in)
	return err
}

// route checks a session's account in the store and the sign-in the helper
// would use for its ticket.
func (h *harness) route(session, wantAccount string) {
	h.t.Helper()
	got, ok, err := h.svc.SessionAccount(h.ctx, domain.SessionID(session))
	if err != nil || !ok || got.AccountID != wantAccount {
		h.t.Fatalf("session %s: route=%+v found=%t err=%v, want account %q", session, got, ok, err, wantAccount)
	}
	wantAuth := ""
	if i := index(h.store.get(), wantAccount); i >= 0 {
		wantAuth = h.store.get().Accounts[i].AuthID
	}
	if auth, found := h.helperAuth(session); !found || auth != wantAuth {
		h.t.Fatalf("session %s: helper uses %q (found=%t), want %q", session, auth, found, wantAuth)
	}
}

func (h *harness) helperAuth(session string) (string, bool) {
	token, _ := h.svc.ticket(domain.SessionID(session))
	sum := sha256.Sum256([]byte(token))
	h.helper.mu.Lock()
	defer h.helper.mu.Unlock()
	for _, r := range h.helper.routes {
		if r.TicketHash == hex.EncodeToString(sum[:]) {
			return r.AuthID, true
		}
	}
	return "", false
}

func (h *harness) view(accountID string) domain.ProviderAccountView {
	h.t.Helper()
	views, err := h.svc.Accounts(h.ctx, false, false)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, v := range views {
		if v.ID == accountID {
			return v
		}
	}
	h.t.Fatalf("account %s is not listed: %+v", accountID, views)
	return domain.ProviderAccountView{}
}

func (h *harness) defaultOf(provider string) string { return h.store.get().Defaults[provider] }

// unchanged fails when a refused operation wrote anything or deleted a credential.
func (h *harness) unchanged(before domain.ProviderAccountState, what string) {
	h.t.Helper()
	if encode(h.store.get()) != encode(before) || len(h.helper.deleted) != 0 {
		h.t.Fatalf("%s changed stored accounts or deleted %v", what, h.helper.deleted)
	}
}
