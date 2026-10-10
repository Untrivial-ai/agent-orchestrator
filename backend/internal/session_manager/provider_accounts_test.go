package sessionmanager

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// accountRoutingFake is an Account Manager: every session is managed when
// managed is set, and otherwise only the ones it has adopted.
type accountRoutingFake struct {
	route                                  domain.ProviderSessionRoute
	env                                    map[string]string
	managed                                bool
	id                                     string
	err                                    error
	has                                    map[domain.SessionID]bool
	assigned, forgotten, launches, adopted []domain.SessionID
	resolved                               []string
}

func (f *accountRoutingFake) ResolveAccount(_ context.Context, h domain.AgentHarness, id string) (string, bool, error) {
	f.resolved = append(f.resolved, string(h)+":"+id)
	return f.id, f.managed, f.err
}

func (f *accountRoutingFake) AssignAccount(_ context.Context, id domain.SessionID, _ domain.AgentHarness, _ string) error {
	f.assigned = append(f.assigned, id)
	return f.err
}

func (f *accountRoutingFake) SessionAccount(_ context.Context, id domain.SessionID) (domain.ProviderSessionRoute, bool, error) {
	return f.route, f.managed || f.has[id], f.err
}

func (f *accountRoutingFake) LaunchAccountEnv(_ context.Context, id domain.SessionID) (map[string]string, error) {
	if f.err != nil || (!f.managed && !f.has[id]) {
		return nil, f.err
	}
	f.launches = append(f.launches, id)
	return f.env, nil
}

func (f *accountRoutingFake) ForgetAccount(_ context.Context, id domain.SessionID) error {
	f.forgotten = append(f.forgotten, id)
	return nil
}

func (f *accountRoutingFake) AdoptSession(_ context.Context, id domain.SessionID, _ domain.AgentHarness) (bool, error) {
	if f.err != nil || f.managed || f.has[id] {
		return false, f.err
	}
	if f.has == nil {
		f.has = map[domain.SessionID]bool{}
	}
	f.has[id] = true
	f.adopted = append(f.adopted, id)
	return true, nil
}

var managedLaunch = map[string]string{"AO_PROXY_ENDPOINT": "http://127.0.0.1:4567", "AO_PROXY_TICKET": "session-ticket"}

func TestAccountEnvIsAddedOnlyToManagedLaunches(t *testing.T) {
	failure := errors.New("routing database unavailable")
	ticket := map[string]string{"AO_PROXY_TICKET": "private", "ANTHROPIC_API_KEY": ""}
	for name, tc := range map[string]struct {
		accounts ports.ProviderAccountRouting
		key      string
		err      error
	}{
		"managed":            {&accountRoutingFake{managed: true, env: ticket}, "", nil},
		"native":             {&accountRoutingFake{env: ticket}, "ambient", nil},
		"failure":            {&accountRoutingFake{managed: true, env: ticket, err: failure}, "ambient", failure},
		"no Account Manager": {nil, "ambient", nil},
	} {
		m, _, _, _ := newManager()
		m.accounts = tc.accounts
		env := map[string]string{"PATH": "/agent-bin", "ANTHROPIC_API_KEY": "ambient"}
		err := m.applyAccountEnv(context.Background(), "s", env)
		if !errors.Is(err, tc.err) || env["PATH"] != "/agent-bin" || env["ANTHROPIC_API_KEY"] != tc.key || (tc.key == "") != (env["AO_PROXY_TICKET"] == "private") {
			t.Errorf("%s: env=%v err=%v", name, env, err)
		}
		if _, managed, err := m.sessionAccount(context.Background(), "s"); managed != (name == "managed") && err == nil {
			t.Errorf("%s: managed=%v", name, managed)
		}
	}
}

func TestRelatedWorkUsesTheOwnersAccountOnlyWhenTheProviderMatches(t *testing.T) {
	for name, tc := range map[string]struct {
		harness  domain.AgentHarness
		provider string
		managed  bool
		account  string
		calls    int
		want     error
	}{
		"codex owner":        {domain.HarnessCodex, "codex", true, "a", 1, nil},
		"claude owner":       {domain.HarnessClaudeCode, "claude", true, "c", 1, nil},
		"different provider": {domain.HarnessClaudeCode, "codex", true, "a", 0, nil},
		"unmanaged harness":  {domain.HarnessCursor, "codex", true, "a", 0, nil},
		"native owner":       {domain.HarnessCodex, "", false, "", 0, nil},
		"waiting owner":      {domain.HarnessCodex, "codex", true, "", 0, ports.ErrProviderLoginRequired},
	} {
		m, _, _, _ := newManager()
		f := &accountRoutingFake{managed: tc.managed, route: domain.ProviderSessionRoute{Provider: tc.provider, AccountID: tc.account}, env: map[string]string{"ticket": "owner"}}
		m.accounts = f
		env := map[string]string{}
		err := m.RelatedAccountEnv(context.Background(), "worker", tc.harness, env)
		if !errors.Is(err, tc.want) || len(f.launches) != tc.calls || (tc.calls == 1) != (env["ticket"] == "owner") {
			t.Errorf("%s: env=%v launches=%v err=%v", name, env, f.launches, err)
		}
	}
}

func TestSpawnRoutesTheDefaultOrChosenAccountIntoTheTerminalLaunch(t *testing.T) {
	for _, harness := range []domain.AgentHarness{domain.HarnessCodex, domain.HarnessClaudeCode} {
		for _, choice := range []string{"", "explicit-account"} {
			m, st, rt, _ := newManager()
			f := &accountRoutingFake{managed: true, id: "resolved-account", env: managedLaunch}
			m.accounts = f
			rec, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Harness: harness, RequestedMode: domain.SessionModeTUI, AccountID: choice})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.resolved, []string{string(harness) + ":" + choice}) || !reflect.DeepEqual(f.assigned, []domain.SessionID{rec.ID}) || !reflect.DeepEqual(f.launches, []domain.SessionID{rec.ID}) {
				t.Fatalf("resolve=%v assign=%v launch=%v", f.resolved, f.assigned, f.launches)
			}
			if _, saved := st.sessions[rec.ID]; !saved || rt.created != 1 || rt.lastCfg.Env["AO_PROXY_TICKET"] != "session-ticket" {
				t.Fatalf("saved=%v created=%d env=%v", saved, rt.created, rt.lastCfg.Env)
			}
			// Codex is pointed at the helper by argument; the ticket stays in the environment.
			if !slices.Contains(rt.lastCfg.Argv, `model_provider="ao-managed"`) || slices.ContainsFunc(rt.lastCfg.Argv, func(arg string) bool { return arg == "session-ticket" }) {
				t.Fatalf("argv=%v", rt.lastCfg.Argv)
			}
		}
	}
}

func TestSpawnWithoutAUsableAccountLeavesNoSession(t *testing.T) {
	for _, assignOnly := range []bool{false, true} {
		m, st, rt, _ := newManager()
		f := &failingAssign{accountRoutingFake: accountRoutingFake{managed: true, err: ports.ErrProviderLoginRequired}, assignOnly: assignOnly}
		m.accounts = f
		_, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex, AccountID: "signed-out"})
		if !errors.Is(err, ports.ErrProviderLoginRequired) || len(st.sessions) != 0 || rt.created != 0 {
			t.Fatalf("assignOnly=%v err=%v sessions=%d runtimes=%d", assignOnly, err, len(st.sessions), rt.created)
		}
		if assignOnly != (len(f.forgotten) == 1) {
			t.Fatalf("assignOnly=%v forgotten=%v", assignOnly, f.forgotten)
		}
	}
}

// failingAssign resolves an account and then cannot give it to the session.
type failingAssign struct {
	accountRoutingFake
	assignOnly bool
}

func (f *failingAssign) ResolveAccount(ctx context.Context, h domain.AgentHarness, id string) (string, bool, error) {
	if f.assignOnly {
		return "a", true, nil
	}
	return f.accountRoutingFake.ResolveAccount(ctx, h, id)
}

func TestManagedChatLaunchAndRestoreCarryTheSameTicket(t *testing.T) {
	launcher := &recordingLauncher{}
	m, sessions, runtime := newChatManager(launcher)
	f := &accountRoutingFake{managed: true, id: "a", route: domain.ProviderSessionRoute{Provider: "codex", AccountID: "a"}, env: managedLaunch}
	m.accounts = f
	rec, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: chatTestProject, Kind: domain.KindWorker, Harness: domain.HarnessCodex, RequestedMode: domain.SessionModeChat})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Kill(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	native := sessions.sessions[rec.ID].Metadata.ProviderConversationID
	if result, err := m.RestoreWithMode(context.Background(), rec.ID); err != nil || result.Mode != RestoreModeNative {
		t.Fatalf("restore=%+v err=%v", result, err)
	}
	if len(launcher.started) != 2 || runtime.created != 0 || native == "" || launcher.started[1].ProviderConversationID != native {
		t.Fatalf("started=%d terminals=%d conversation=%q", len(launcher.started), runtime.created, native)
	}
	for _, start := range launcher.started {
		if start.SessionID != rec.ID || start.Env["AO_PROXY_TICKET"] != "session-ticket" || start.Env["AO_PROXY_ENDPOINT"] != managedLaunch["AO_PROXY_ENDPOINT"] {
			t.Fatalf("launch env=%v", start.Env)
		}
	}
	if !reflect.DeepEqual(f.assigned, []domain.SessionID{rec.ID}) || len(f.resolved) != 1 {
		t.Fatalf("restore chose an account again: resolved=%v assigned=%v", f.resolved, f.assigned)
	}
}

func TestManagedSessionCannotChangeProvider(t *testing.T) {
	for _, mode := range []domain.SessionMode{domain.SessionModeTUI, domain.SessionModeChat} {
		runtime := &fakeRestartRuntime{fakeRuntime: &fakeRuntime{}}
		m, sessions, _ := newSwitchTestManager(t, runtime)
		rec := sessions.sessions["proj-1"]
		rec.Mode = mode
		sessions.sessions[rec.ID] = rec
		routes := &accountRoutingFake{managed: true, route: domain.ProviderSessionRoute{Provider: "claude", AccountID: "alice"}}
		m.accounts = routes
		_, err := m.SwitchAgent(context.Background(), rec.ID, SwitchAgentConfig{TargetHarness: domain.HarnessCodex, IdempotencyKey: "managed-harness-switch"})
		if !errors.Is(err, ports.ErrProviderAccountIncompatible) || !reflect.DeepEqual(sessions.sessions[rec.ID], rec) {
			t.Fatalf("%s: err=%v", mode, err)
		}
		if len(runtime.destroyedIDs) != 0 || runtime.created != 0 || len(routes.assigned)+len(routes.launches)+len(routes.forgotten) != 0 {
			t.Fatalf("%s: a refused change touched the session's process or account", mode)
		}
	}
}

func TestManagedSessionsSkipTheNativeSignInProbe(t *testing.T) {
	for _, managed := range []bool{false, true} {
		m, _, _, _ := newManager()
		m.agents = singleAgent{agent: &launchAuthAgent{recordingAgent: &recordingAgent{}, status: ports.AgentAuthStatusUnauthorized}}
		m.accounts = &accountRoutingFake{managed: managed, id: "a"}
		_, _, _, spawnErr := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex, RequestedMode: domain.SessionModeTUI})

		switching, store, _, _, _ := newTransitionManager(t, domain.SessionModeChat)
		switching.agents = singleAgent{agent: &transitionLaunchAuthAgent{status: ports.AgentAuthStatusUnauthorized}}
		switching.accounts = &accountRoutingFake{managed: managed}
		switchErr := switching.preflightInterfaceTarget(context.Background(), store.sessions["session-1"], domain.SessionInterfaceTransition{TargetMode: domain.SessionModeTUI, NativeConversationID: "native-1"})
		for name, err := range map[string]error{"spawn": spawnErr, "interface switch": switchErr} {
			if managed == errors.Is(err, ports.ErrAgentAuthRequired) {
				t.Errorf("%s managed=%v: err=%v", name, managed, err)
			}
		}
	}
}
