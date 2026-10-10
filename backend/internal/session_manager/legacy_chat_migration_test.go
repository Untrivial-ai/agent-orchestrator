package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func legacyChat(id domain.SessionID) domain.SessionRecord {
	return domain.SessionRecord{
		ID: id, ProjectID: chatTestProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Activity: domain.Activity{State: domain.ActivityExited},
		Metadata: domain.SessionMetadata{
			Branch: "ao/" + string(id) + "/root", WorkspacePath: "/ws/" + string(id),
			ProviderConversationID: "conversation-" + string(id),
		},
	}
}

type switchingStore struct{ *switchTestStore }

func (switchingStore) GetActiveAgentSwitch(context.Context, domain.SessionID) (domain.AgentSwitch, bool, error) {
	return domain.AgentSwitch{}, true, nil
}

func TestALegacyChatGetsItsAccountAsItsProviderIsLaunched(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, _ := newChatManager(launcher)
	accounts := &accountRoutingFake{env: managedLaunch}
	m.accounts = accounts
	rec := legacyChat("mer-1")
	// A second launch finds the account and changes nothing.
	for range 2 {
		st.sessions[rec.ID] = rec
		if _, err := m.ResumeAgentWithMode(context.Background(), rec.ID); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(accounts.adopted, []domain.SessionID{rec.ID}) || len(launcher.started) != 2 || launcher.started[0].Env["AO_PROXY_TICKET"] != "session-ticket" {
		t.Fatalf("adopted=%v started=%#v", accounts.adopted, launcher.started)
	}
}

func TestALegacyChatKeepsThisComputersSignInWhenItCannotBeAdopted(t *testing.T) {
	for name, prepare := range map[string]func(*Manager, *accountRoutingFake, *domain.SessionRecord){
		"another agent": func(_ *Manager, _ *accountRoutingFake, rec *domain.SessionRecord) { rec.Harness = domain.HarnessCursor },
		"changing agent": func(m *Manager, _ *accountRoutingFake, _ *domain.SessionRecord) {
			m.store = switchingStore{newSwitchTestStore()}
		},
		"helper refuses": func(_ *Manager, f *accountRoutingFake, _ *domain.SessionRecord) { f.err = errors.New("helper refused") },
	} {
		m, _, _ := newChatManager(&recordingLauncher{})
		accounts := &accountRoutingFake{env: managedLaunch}
		m.accounts = accounts
		rec := legacyChat("mer-1")
		prepare(m, accounts, &rec)
		m.adoptLegacyChat(context.Background(), rec)
		if len(accounts.adopted) != 0 {
			t.Errorf("%s: adopted=%v", name, accounts.adopted)
		}
	}
}

// restartingLauncher is a Chat service whose providers can be stopped and
// started again; starting one is what gives the chat its account.
type restartingLauncher struct {
	*recordingLauncher
	store    *fakeStore
	accounts *accountRoutingFake
	running  map[domain.SessionID]bool
	busy     map[domain.SessionID]bool
	restarts []domain.SessionID
	wakeErr  error
	// wakeStops is a start that got as far as taking the chat out of its sleep.
	wakeStops bool
}

func (l *restartingLauncher) HasLiveChatController(id domain.SessionID) bool { return l.running[id] }

func (l *restartingLauncher) HibernateChatForRestart(_ context.Context, id domain.SessionID) (bool, error) {
	if l.busy[id] {
		return false, nil
	}
	rec := l.store.sessions[id]
	rec.HibernatedAt = &rec.CreatedAt
	l.store.sessions[id], l.running[id] = rec, false
	return true, nil
}

func (l *restartingLauncher) WakeChat(ctx context.Context, id domain.SessionID) error {
	rec := l.store.sessions[id]
	if l.wakeErr != nil {
		if l.wakeStops {
			rec.HibernatedAt = nil
			l.store.sessions[id] = rec
		}
		return l.wakeErr
	}
	rec.HibernatedAt = nil
	l.store.sessions[id], l.running[id] = rec, true
	l.restarts = append(l.restarts, id)
	_, err := l.accounts.AdoptSession(ctx, id, rec.Harness)
	return err
}

func TestMigrateLegacySessionsRestartsOnlyQuietChatsWithoutAnAccount(t *testing.T) {
	accounts := &accountRoutingFake{has: map[domain.SessionID]bool{"managed": true}}
	launcher := &restartingLauncher{
		recordingLauncher: &recordingLauncher{}, accounts: accounts, busy: map[domain.SessionID]bool{"busy": true},
		running: map[domain.SessionID]bool{"quiet-1": true, "quiet-2": true, "busy": true, "managed": true, "other-agent": true, "ended": true},
	}
	m, st, _ := newChatManager(launcher)
	launcher.store, m.accounts = st, accounts
	m.legacySlots = semaphore.NewWeighted(1) // these fakes are not safe to restart side by side
	for id, change := range map[domain.SessionID]func(*domain.SessionRecord){
		"quiet-1": nil, "busy": nil, "managed": nil,
		"quiet-2": func(rec *domain.SessionRecord) {
			rec.Harness, rec.Kind = domain.HarnessClaudeCode, domain.KindOrchestrator
		},
		"asleep":      func(rec *domain.SessionRecord) { rec.HibernatedAt = &rec.CreatedAt },
		"stopped":     func(rec *domain.SessionRecord) { rec.Activity.State = domain.ActivityExited },
		"other-agent": func(rec *domain.SessionRecord) { rec.Harness = domain.HarnessCursor },
		"ended":       func(rec *domain.SessionRecord) { rec.IsTerminated = true },
	} {
		rec := legacyChat(id)
		rec.Activity.State = domain.ActivityIdle
		if change != nil {
			change(&rec)
		}
		st.sessions[id] = rec
	}
	// Only the busy chat keeps running without an account: the sleeping and the
	// stopped one have no process and are adopted when they next start.
	remaining, err := m.MigrateLegacySessions(context.Background())
	if err != nil || remaining != 1 || len(launcher.restarts) != 2 || !accounts.has["quiet-1"] || !accounts.has["quiet-2"] {
		t.Fatalf("remaining=%d err=%v restarts=%v", remaining, err, launcher.restarts)
	}
	// The busy chat finishes its turn; a later pass has nothing left to do.
	launcher.busy["busy"] = false
	for range 2 {
		if remaining, err = m.MigrateLegacySessions(context.Background()); err != nil || remaining != 0 || len(launcher.restarts) != 3 {
			t.Fatalf("later pass: remaining=%d err=%v restarts=%v", remaining, err, launcher.restarts)
		}
	}
}

func TestMigrateLegacySessionsLeavesAChatAsleepWhenItCannotBeStartedAgain(t *testing.T) {
	failure := errors.New("provider did not start")
	accounts := &accountRoutingFake{}
	launcher := &restartingLauncher{recordingLauncher: &recordingLauncher{}, accounts: accounts, running: map[domain.SessionID]bool{"quiet": true}, wakeErr: failure}
	m, st, _ := newChatManager(launcher)
	launcher.store, m.accounts = st, accounts
	rec := legacyChat("quiet")
	rec.Activity.State = domain.ActivityIdle
	st.sessions[rec.ID] = rec
	if remaining, err := m.MigrateLegacySessions(context.Background()); !errors.Is(err, failure) || remaining != 1 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
	// It is asleep now, with nothing left to stop. It is adopted when it wakes.
	if remaining, err := m.MigrateLegacySessions(context.Background()); err != nil || remaining != 0 {
		t.Fatalf("next pass: remaining=%d err=%v", remaining, err)
	}
}

func TestALegacyChatWhoseStartFailedIsStartedAgainAtTheNextCheck(t *testing.T) {
	failure := errors.New("provider did not start in time")
	accounts := &accountRoutingFake{env: managedLaunch}
	launcher := &restartingLauncher{recordingLauncher: &recordingLauncher{}, accounts: accounts, running: map[domain.SessionID]bool{"quiet": true}, wakeErr: failure, wakeStops: true}
	m, st, _ := newChatManager(launcher)
	launcher.store, m.accounts = st, accounts
	rec := legacyChat("quiet")
	rec.Activity.State = domain.ActivityIdle
	st.sessions[rec.ID] = rec
	if remaining, err := m.MigrateLegacySessions(context.Background()); !errors.Is(err, failure) || remaining != 1 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
	// It is neither asleep nor running: nothing but this move would start it.
	if remaining, err := m.MigrateLegacySessions(context.Background()); err != nil || remaining != 0 {
		t.Fatalf("next check: remaining=%d err=%v", remaining, err)
	}
	if len(launcher.started) != 1 || launcher.started[0].SessionID != "quiet" {
		t.Fatalf("started=%+v, want the chat started once more", launcher.started)
	}
	// Once it is up it is left alone.
	launcher.running["quiet"], accounts.has = true, map[domain.SessionID]bool{"quiet": true}
	if remaining, err := m.MigrateLegacySessions(context.Background()); err != nil || remaining != 0 || len(launcher.started) != 1 {
		t.Fatalf("later check: remaining=%d err=%v started=%d", remaining, err, len(launcher.started))
	}
}

// screenGate is the terminal mux: which terminals a client shows, and the
// order in which keystrokes were closed and reopened.
type screenGate struct {
	onScreen map[string]bool
	events   *[]string
	onDrain  func()
}

func (g *screenGate) TerminalOnScreen(id string) bool { return g.onScreen[id] }

func (g *screenGate) BeginInputDrain(string) (time.Time, func()) {
	*g.events = append(*g.events, "keystrokes closed")
	if g.onDrain != nil {
		g.onDrain()
	}
	return time.Time{}, func() { *g.events = append(*g.events, "keystrokes open") }
}

// newLegacyTerminalManager has one running Codex terminal, "mer-1", that
// predates Account Manager, and no client showing it.
func newLegacyTerminalManager(t *testing.T) (*Manager, *fakeStore, *fakeRuntime, *accountRoutingFake, *screenGate) {
	t.Helper()
	rt := &fakeRuntime{aliveByHandle: map[string]bool{"tmux-mer-1": true}}
	m, st, _ := newExitedResumeManager(t, rt, supervisedLaunchAgent{launchArgvAgent{argv: []string{"codex", "resume", "agent-x"}}})
	rec := st.sessions["mer-1"]
	rec.Activity.State = domain.ActivityIdle
	st.sessions["mer-1"] = rec
	accounts := &accountRoutingFake{env: managedLaunch}
	gate := &screenGate{onScreen: map[string]bool{}, events: &[]string{}}
	rt.onDestroy = func(int, ports.RuntimeHandle) { *gate.events = append(*gate.events, "agent exited") }
	m.accounts, m.interfaceTransition.idleSettle = accounts, time.Millisecond
	m.SetTerminalInputGate(gate)
	return m, st, rt, accounts, gate
}

// turnEnded reports the end of a turn and waits for what that set off.
func turnEnded(t *testing.T, m *Manager, rec domain.SessionRecord) {
	t.Helper()
	m.SessionTurnEnded(rec)
	if err := m.WaitAgentSwitchWorkers(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAnIdleLegacyTerminalOffScreenIsAdoptedExitedAndResumed(t *testing.T) {
	m, st, rt, accounts, gate := newLegacyTerminalManager(t)
	rt.onDestroy = func(int, ports.RuntimeHandle) {
		if _, admitted := m.AcquireSessionInput("mer-1"); admitted || !accounts.has["mer-1"] {
			t.Errorf("agent exited with input admitted=%v adopted=%v", admitted, accounts.has["mer-1"])
		}
		*gate.events = append(*gate.events, "agent exited")
	}
	remaining, err := m.MigrateLegacySessions(context.Background())
	got := st.sessions["mer-1"]
	if err != nil || remaining != 0 || got.Activity.State != domain.ActivityIdle || got.Metadata.RuntimeLaunchID != "launch-new" || got.IsTerminated {
		t.Fatalf("remaining=%d err=%v session=%+v", remaining, err, got)
	}
	// The same conversation is resumed, on the account, with keystrokes closed throughout.
	if argv := rt.lastCfg.Argv; rt.created != 1 || !slices.Contains(argv, "agent-x") || !slices.Contains(argv, `model_provider="ao-managed"`) || rt.lastCfg.Env["AO_PROXY_TICKET"] != "session-ticket" {
		t.Fatalf("created=%d cfg=%+v", rt.created, rt.lastCfg)
	}
	if want := []string{"keystrokes closed", "agent exited", "keystrokes open"}; !reflect.DeepEqual(*gate.events, want) {
		t.Fatalf("events=%v want %v", *gate.events, want)
	}
	// It has its account now: later checks and turns leave it running.
	turnEnded(t, m, got)
	if remaining, err = m.MigrateLegacySessions(context.Background()); err != nil || remaining != 0 || rt.destroyed != 1 {
		t.Fatalf("later pass: remaining=%d err=%v destroyed=%d", remaining, err, rt.destroyed)
	}
}

func TestALegacyTerminalIsLeftRunningUntilItCanBeMoved(t *testing.T) {
	for name, hold := range map[string]func(*fakeStore, *screenGate, bool){
		"working": func(st *fakeStore, _ *screenGate, held bool) {
			rec := st.sessions["mer-1"]
			rec.Activity.State = map[bool]domain.ActivityState{true: domain.ActivityActive, false: domain.ActivityIdle}[held]
			st.sessions["mer-1"] = rec
		},
		"on screen": func(_ *fakeStore, gate *screenGate, held bool) { gate.onScreen["tmux-mer-1"] = held },
		// A prompt accepted just before input closed starts its turn before the second reading.
		"turn starting": func(st *fakeStore, gate *screenGate, held bool) {
			gate.onDrain = nil
			if held {
				gate.onDrain = func() {
					rec := st.sessions["mer-1"]
					rec.Activity.State = domain.ActivityActive
					st.sessions["mer-1"] = rec
				}
			}
		},
	} {
		m, st, rt, accounts, gate := newLegacyTerminalManager(t)
		hold(st, gate, true)
		turnEnded(t, m, st.sessions["mer-1"])
		remaining, err := m.MigrateLegacySessions(context.Background())
		if err != nil || remaining != 1 || rt.destroyed != 0 || len(accounts.adopted) != 0 {
			t.Fatalf("%s: remaining=%d err=%v destroyed=%d adopted=%v", name, remaining, err, rt.destroyed, accounts.adopted)
		}
		hold(st, gate, false)
		rec := st.sessions["mer-1"]
		rec.Activity.State = domain.ActivityIdle
		st.sessions["mer-1"] = rec
		if remaining, err = m.MigrateLegacySessions(context.Background()); err != nil || remaining != 0 || rt.created != 1 {
			t.Fatalf("%s, once free: remaining=%d err=%v created=%d", name, remaining, err, rt.created)
		}
	}
}

func TestATurnEndingMovesALegacySessionWithoutWaitingForTheCheck(t *testing.T) {
	m, st, rt, accounts, _ := newLegacyTerminalManager(t)
	other := st.sessions["mer-1"]
	other.ID, other.Harness = "mer-2", domain.HarnessCursor
	st.sessions["mer-2"] = other
	m.SessionTurnEnded(other)
	turnEnded(t, m, st.sessions["mer-1"])
	if _, moved := accounts.has["mer-2"]; moved || rt.destroyed != 1 {
		t.Fatalf("another agent was moved: destroyed=%d adopted=%v", rt.destroyed, accounts.adopted)
	}
	if got := st.sessions["mer-1"]; got.Metadata.RuntimeLaunchID != "launch-new" || rt.created != 1 || !accounts.has["mer-1"] {
		t.Fatalf("session=%+v created=%d adopted=%v", got, rt.created, accounts.adopted)
	}

	// A chat is restarted the same way.
	chatAccounts := &accountRoutingFake{}
	launcher := &restartingLauncher{recordingLauncher: &recordingLauncher{}, accounts: chatAccounts, running: map[domain.SessionID]bool{"quiet": true}}
	cm, cst, _ := newChatManager(launcher)
	launcher.store, cm.accounts = cst, chatAccounts
	rec := legacyChat("quiet")
	rec.Activity.State = domain.ActivityIdle
	cst.sessions[rec.ID] = rec
	turnEnded(t, cm, rec)
	if !reflect.DeepEqual(launcher.restarts, []domain.SessionID{"quiet"}) || !chatAccounts.has["quiet"] {
		t.Fatalf("restarts=%v adopted=%v", launcher.restarts, chatAccounts.adopted)
	}
}

func TestALegacyTerminalThatCannotBeMovedIsReportedAndTriedAgain(t *testing.T) {
	refused, noRuntime := errors.New("helper refused"), errors.New("no runtime")
	t.Run("adoption refused", func(t *testing.T) {
		m, st, rt, accounts, _ := newLegacyTerminalManager(t)
		accounts.err = refused
		if remaining, err := m.MigrateLegacySessions(context.Background()); !errors.Is(err, refused) || remaining != 1 || rt.destroyed != 0 {
			t.Fatalf("remaining=%d err=%v destroyed=%d", remaining, err, rt.destroyed)
		}
		accounts.err = nil
		if remaining, err := m.MigrateLegacySessions(context.Background()); err != nil || remaining != 0 || st.sessions["mer-1"].Metadata.RuntimeLaunchID != "launch-new" {
			t.Fatalf("next pass: remaining=%d err=%v", remaining, err)
		}
	})
	t.Run("agent cannot be exited", func(t *testing.T) {
		m, st, rt, accounts, _ := newLegacyTerminalManager(t)
		rt.destroyErr, rt.fencedResult = noRuntime, ports.FencedProbeResult{Liveness: ports.FencedAlive}
		if remaining, err := m.MigrateLegacySessions(context.Background()); !errors.Is(err, noRuntime) || remaining != 1 {
			t.Fatalf("remaining=%d err=%v", remaining, err)
		}
		// It runs on as it did, on its own sign-in, to be adopted afresh next time.
		if got := st.sessions["mer-1"]; got.Activity.State != domain.ActivityIdle || !reflect.DeepEqual(accounts.forgotten, []domain.SessionID{"mer-1"}) {
			t.Fatalf("session=%+v forgotten=%v", got, accounts.forgotten)
		}
	})
	t.Run("relaunch fails", func(t *testing.T) {
		m, st, rt, accounts, _ := newLegacyTerminalManager(t)
		rt.createErr = noRuntime
		remaining, err := m.MigrateLegacySessions(context.Background())
		if got := st.sessions["mer-1"]; !errors.Is(err, noRuntime) || remaining != 1 || got.Activity.State != domain.ActivityExited || !accounts.has["mer-1"] {
			t.Fatalf("remaining=%d err=%v session=%+v", remaining, err, got)
		}
		rt.createErr = nil
		if remaining, err = m.MigrateLegacySessions(context.Background()); err != nil || remaining != 0 || st.sessions["mer-1"].Activity.State != domain.ActivityIdle {
			t.Fatalf("next pass: remaining=%d err=%v session=%+v", remaining, err, st.sessions["mer-1"])
		}
		// An agent the user exited afterwards stays exited.
		if _, err := m.ExitAgent(context.Background(), "mer-1"); err != nil {
			t.Fatal(err)
		}
		if remaining, err = m.MigrateLegacySessions(context.Background()); err != nil || remaining != 0 || rt.created != 1 {
			t.Fatalf("after the user's exit: remaining=%d err=%v created=%d", remaining, err, rt.created)
		}
	})
}

// slowRestarter is a Chat service whose restarts overlap; it counts how many do.
type slowRestarter struct {
	*recordingLauncher
	*accountRoutingFake
	mu                      sync.Mutex
	moving, most, restarted int
	managed                 map[domain.SessionID]bool
	release                 chan struct{}
}

func (l *slowRestarter) HasLiveChatController(domain.SessionID) bool { return true }

func (l *slowRestarter) HibernateChatForRestart(context.Context, domain.SessionID) (bool, error) {
	l.mu.Lock()
	l.moving++
	l.most = max(l.most, l.moving)
	l.mu.Unlock()
	<-l.release
	return true, nil
}

func (l *slowRestarter) WakeChat(_ context.Context, id domain.SessionID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.moving, l.restarted, l.managed[id] = l.moving-1, l.restarted+1, true
	return nil
}

func (l *slowRestarter) SessionAccount(_ context.Context, id domain.SessionID) (domain.ProviderSessionRoute, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return domain.ProviderSessionRoute{}, l.managed[id], nil
}

func TestNoMoreThanFourLegacySessionsAreMovedAtOnce(t *testing.T) {
	launcher := &slowRestarter{recordingLauncher: &recordingLauncher{}, accountRoutingFake: &accountRoutingFake{}, managed: map[domain.SessionID]bool{}, release: make(chan struct{})}
	m, st, _ := newChatManager(launcher)
	m.accounts = launcher
	for i := range 9 {
		rec := legacyChat(domain.SessionID(fmt.Sprintf("quiet-%d", i)))
		rec.Activity.State = domain.ActivityIdle
		st.sessions[rec.ID] = rec
	}
	done := make(chan int, 1)
	go func() {
		remaining, _ := m.MigrateLegacySessions(context.Background())
		done <- remaining
	}()
	// Four restarts begin and the rest wait for one of them to finish.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		launcher.mu.Lock()
		moving := launcher.moving
		launcher.mu.Unlock()
		if moving == 4 || time.Now().After(deadline) {
			break
		}
	}
	time.Sleep(50 * time.Millisecond)
	close(launcher.release)
	if remaining := <-done; remaining != 0 || launcher.most != 4 || launcher.restarted != 9 {
		t.Fatalf("remaining=%d most=%d restarted=%d", remaining, launcher.most, launcher.restarted)
	}
}
