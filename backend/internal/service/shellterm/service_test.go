package shellterm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const testAppRunID = "app-run-current"

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeShellRuntime records every runtime call so tests can assert on what was
// spawned and what was torn down.
type fakeShellRuntime struct {
	created   []ports.RuntimeConfig
	destroyed []string

	createErr  error
	destroyErr error
	// aliveByHandle answers IsAlive; a handle absent from the map is dead.
	aliveByHandle map[string]bool
	aliveErr      error
}

func newFakeShellRuntime() *fakeShellRuntime {
	return &fakeShellRuntime{aliveByHandle: map[string]bool{}}
}

func (f *fakeShellRuntime) Create(_ context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	if f.createErr != nil {
		return ports.RuntimeHandle{}, f.createErr
	}
	f.created = append(f.created, cfg)
	f.aliveByHandle[string(cfg.SessionID)] = true
	return ports.RuntimeHandle{ID: string(cfg.SessionID)}, nil
}

func (f *fakeShellRuntime) Destroy(_ context.Context, handle ports.RuntimeHandle) error {
	f.destroyed = append(f.destroyed, handle.ID)
	// Only a successful destroy is modeled as actually killing the runtime — a
	// failed one leaves aliveByHandle as the caller set it up, so tests can
	// distinguish "destroy errored but it was already dead" (aliveByHandle has
	// no entry) from "destroy errored and it is still alive" (pre-seeded true).
	if f.destroyErr == nil {
		delete(f.aliveByHandle, handle.ID)
	}
	return f.destroyErr
}

func (f *fakeShellRuntime) IsAlive(_ context.Context, handle ports.RuntimeHandle) (bool, error) {
	if f.aliveErr != nil {
		return false, f.aliveErr
	}
	return f.aliveByHandle[handle.ID], nil
}

// fakeShellTerminalStore is an in-memory Store keyed by handle id.
type fakeShellTerminalStore struct {
	records   []ShellTerminalRecord
	insertErr error
}

func (f *fakeShellTerminalStore) InsertShellTerminal(_ context.Context, rec ShellTerminalRecord) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.records = append(f.records, rec)
	return nil
}

func (f *fakeShellTerminalStore) UpdateShellTerminalTitle(_ context.Context, handleID, title string) (ShellTerminalRecord, bool, error) {
	for i, rec := range f.records {
		if rec.HandleID == handleID {
			f.records[i].Title = title
			return f.records[i], true, nil
		}
	}
	return ShellTerminalRecord{}, false, nil
}

func (f *fakeShellTerminalStore) SelectShellTerminalByHandleID(_ context.Context, handleID string) (ShellTerminalRecord, bool, error) {
	for _, rec := range f.records {
		if rec.HandleID == handleID {
			return rec, true, nil
		}
	}
	return ShellTerminalRecord{}, false, nil
}

func (f *fakeShellTerminalStore) SelectShellTerminalsByAppRunID(_ context.Context, appRunID string) ([]ShellTerminalRecord, error) {
	var out []ShellTerminalRecord
	for _, rec := range f.records {
		if rec.AppRunID == appRunID {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (f *fakeShellTerminalStore) SelectShellTerminalsBySessionID(_ context.Context, sessionID domain.SessionID) ([]ShellTerminalRecord, error) {
	var out []ShellTerminalRecord
	for _, rec := range f.records {
		if rec.SessionID == sessionID {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (f *fakeShellTerminalStore) SelectShellTerminalsFromPreviousAppRuns(_ context.Context, appRunID string) ([]ShellTerminalRecord, error) {
	var out []ShellTerminalRecord
	for _, rec := range f.records {
		if rec.AppRunID != appRunID {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (f *fakeShellTerminalStore) DeleteShellTerminalByHandleID(_ context.Context, handleID string) (bool, error) {
	for i, rec := range f.records {
		if rec.HandleID == handleID {
			f.records = append(f.records[:i], f.records[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeShellTerminalStore) DeleteShellTerminalsFromPreviousAppRuns(_ context.Context, appRunID string) (int64, error) {
	kept := make([]ShellTerminalRecord, 0, len(f.records))
	var cleared int64
	for _, rec := range f.records {
		if rec.AppRunID == appRunID {
			kept = append(kept, rec)
			continue
		}
		cleared++
	}
	f.records = kept
	return cleared, nil
}

type fakeProjectRootLocator struct {
	roots map[domain.ProjectID]string
	err   error
}

func (f *fakeProjectRootLocator) ProjectRoot(_ context.Context, id domain.ProjectID) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.roots[id], nil
}

// fakeSessionWorkspace is one entry in fakeSessionWorkspaceLocator: a session's
// workspace path (possibly empty, standing in for a session with no worktree
// of its own yet) and the project it belongs to.
type fakeSessionWorkspace struct {
	workspacePath string
	projectID     domain.ProjectID
}

type fakeSessionWorkspaceLocator struct {
	sessions map[domain.SessionID]fakeSessionWorkspace
	err      error
}

func (f *fakeSessionWorkspaceLocator) SessionWorkspace(_ context.Context, id domain.SessionID) (string, domain.ProjectID, error) {
	if f.err != nil {
		return "", "", f.err
	}
	ws, ok := f.sessions[id]
	if !ok {
		return "", "", apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	return ws.workspacePath, ws.projectID, nil
}

// newTestService wires a service with deterministic ids so assertions can name
// exact handles instead of matching on a prefix. Session resolution is not
// under test here, so it is wired with an empty locator; tests that need it
// use newTestServiceWithSessions.
func newTestService(rt *fakeShellRuntime, st *fakeShellTerminalStore, projects ProjectRootLocator) *Service {
	return newTestServiceWithSessions(rt, st, projects, &fakeSessionWorkspaceLocator{})
}

func newTestServiceWithSessions(rt *fakeShellRuntime, st *fakeShellTerminalStore, projects ProjectRootLocator, sessions SessionWorkspaceLocator) *Service {
	svc := NewService(rt, st, projects, sessions, "/data/dir", testAppRunID, testLogger())
	var n int
	svc.newHandleID = func() (string, error) {
		n++
		return "shellterm-test" + string(rune('0'+n)), nil
	}
	svc.now = func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) }
	return svc
}

func TestOpenShellTerminalStartsLoginShellInProjectRoot(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	projects := &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": "/repos/portfolio"}}
	svc := newTestService(rt, st, projects)

	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{ProjectID: "portfolio"})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}

	if len(rt.created) != 1 {
		t.Fatalf("runtime creates = %d, want 1", len(rt.created))
	}
	if got := rt.created[0].WorkspacePath; got != "/repos/portfolio" {
		t.Errorf("workspace path = %q, want the project root", got)
	}
	if len(rt.created[0].Argv) == 0 {
		t.Error("argv is empty; a shell terminal must launch a resolved shell")
	}
	if rt.created[0].RuntimeLaunchID != "" {
		t.Fatalf("shell terminal opted into worker containment generation %q", rt.created[0].RuntimeLaunchID)
	}
	if term.WorkingDir != "/repos/portfolio" {
		t.Errorf("working dir = %q, want the project root", term.WorkingDir)
	}
	if term.Title != "portfolio" {
		t.Errorf("title = %q, want the working dir's base name", term.Title)
	}
	if len(st.records) != 1 || st.records[0].AppRunID != testAppRunID {
		t.Fatalf("record not persisted against the current app run: %+v", st.records)
	}
}

func TestOpenShellTerminalFallsBackToDataDirWhenNoProjectGiven(t *testing.T) {
	rt := newFakeShellRuntime()
	svc := newTestService(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{})

	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	if term.WorkingDir != "/data/dir" {
		t.Errorf("working dir = %q, want the daemon data dir", term.WorkingDir)
	}
	if term.ProjectID != "" {
		t.Errorf("project id = %q, want empty", term.ProjectID)
	}
}

func TestOpenShellTerminalReturnsNotFoundForUnknownProject(t *testing.T) {
	rt := newFakeShellRuntime()
	svc := newTestService(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{}})

	_, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{ProjectID: "ghost"})

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindNotFound {
		t.Fatalf("error = %v, want a not-found apierr", err)
	}
	if len(rt.created) != 0 {
		t.Error("a runtime was spawned for an unknown project")
	}
}

func TestOpenShellTerminalScopesToSession(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	projects := &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": "/repos/portfolio"}}
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"portfolio-3": {workspacePath: "", projectID: "portfolio"},
	}}
	svc := newTestServiceWithSessions(rt, st, projects, sessions)

	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{ProjectID: "portfolio", SessionID: "portfolio-3"})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	if term.SessionID != "portfolio-3" {
		t.Errorf("returned session id = %q, want portfolio-3", term.SessionID)
	}
	if len(st.records) != 1 || st.records[0].SessionID != "portfolio-3" {
		t.Fatalf("session id not persisted on the record: %+v", st.records)
	}
}

// This is the regression the bug covered: opening a shell from a session view
// must land in that session's worktree, not the registered project root, even
// though both are supplied on the request.
func TestOpenShellTerminalStartsInSessionWorkspaceOverProjectRoot(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	projects := &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": "/repos/portfolio"}}
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"portfolio-3": {workspacePath: "/worktrees/portfolio-3", projectID: "portfolio"},
	}}
	svc := newTestServiceWithSessions(rt, st, projects, sessions)

	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{ProjectID: "portfolio", SessionID: "portfolio-3"})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	if term.WorkingDir != "/worktrees/portfolio-3" {
		t.Errorf("working dir = %q, want the session's worktree", term.WorkingDir)
	}
	if rt.created[0].WorkspacePath != "/worktrees/portfolio-3" {
		t.Errorf("runtime workspace = %q, want the session's worktree", rt.created[0].WorkspacePath)
	}
}

// A session that has no workspace of its own yet (or an orchestrator that
// simply runs at the project root) falls back to the project root rather than
// failing the open.
func TestOpenShellTerminalFallsBackToProjectRootWhenSessionHasNoWorkspace(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	projects := &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": "/repos/portfolio"}}
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"portfolio-orch": {workspacePath: "", projectID: "portfolio"},
	}}
	svc := newTestServiceWithSessions(rt, st, projects, sessions)

	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{SessionID: "portfolio-orch"})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	if term.WorkingDir != "/repos/portfolio" {
		t.Errorf("working dir = %q, want the project root fallback", term.WorkingDir)
	}
}

func TestOpenShellTerminalReturnsNotFoundForUnknownSession(t *testing.T) {
	rt := newFakeShellRuntime()
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{}}
	svc := newTestServiceWithSessions(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{}, sessions)

	_, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{SessionID: "ghost"})

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindNotFound {
		t.Fatalf("error = %v, want a not-found apierr", err)
	}
	if len(rt.created) != 0 {
		t.Error("a runtime was spawned for an unknown session")
	}
}

// TestOpenShellTerminalDoesNotAllocateGateForUnknownSession is the regression
// for the unbounded-growth bug: a stream of requests naming made-up session
// ids used to allocate a permanent gate map entry each time, before the
// session id was ever validated. The session must now be confirmed to exist
// before any gate state is touched, so an invalid id leaves the gate map
// untouched.
func TestOpenShellTerminalDoesNotAllocateGateForUnknownSession(t *testing.T) {
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{}}
	svc := newTestServiceWithSessions(newFakeShellRuntime(), &fakeShellTerminalStore{}, &fakeProjectRootLocator{}, sessions)

	for _, id := range []domain.SessionID{"ghost-1", "ghost-2", "ghost-3"} {
		if _, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{SessionID: id}); err == nil {
			t.Fatalf("OpenShellTerminal(%s): want an error for an unknown session", id)
		}
	}

	if len(svc.gates) != 0 {
		t.Errorf("gates = %+v, want no entries allocated for sessions that never validated", svc.gates)
	}
}

// BeginSessionTeardown is what Session Manager calls before releasing a
// session's worktree (Kill, Cleanup, RetireForReplacement, save-and-teardown),
// so a shell terminal scoped to that session never survives pointed at a
// directory that is about to be removed.
func TestBeginSessionTeardownDestroysRuntimeAndDeletesRows(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{records: []ShellTerminalRecord{
		{HandleID: "shellterm-1", SessionID: "portfolio-3", WorkingDir: "/ws/portfolio-3"},
		{HandleID: "shellterm-2", SessionID: "portfolio-3", WorkingDir: "/ws/portfolio-3"},
		{HandleID: "shellterm-other", SessionID: "other-session", WorkingDir: "/ws/other"},
	}}
	rt.aliveByHandle["shellterm-1"] = true
	rt.aliveByHandle["shellterm-2"] = true
	rt.aliveByHandle["shellterm-other"] = true
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	release, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err != nil {
		t.Fatalf("BeginSessionTeardown: %v", err)
	}
	release()

	if len(rt.destroyed) != 2 {
		t.Fatalf("destroyed = %v, want both of the session's shells torn down", rt.destroyed)
	}
	if len(st.records) != 1 || st.records[0].HandleID != "shellterm-other" {
		t.Fatalf("records = %+v, want only the other session's shell left", st.records)
	}
}

// A destroy error alone is not proof the shell survived — some runtimes error
// on an already-gone handle. When IsAlive confirms it is in fact dead, the row
// must still be cleaned up rather than left stranded.
func TestBeginSessionTeardownContinuesPastDestroyFailureWhenConfirmedDead(t *testing.T) {
	rt := newFakeShellRuntime()
	rt.destroyErr = errors.New("tmux: no such session")
	st := &fakeShellTerminalStore{records: []ShellTerminalRecord{
		{HandleID: "shellterm-1", SessionID: "portfolio-3"},
		{HandleID: "shellterm-2", SessionID: "portfolio-3"},
	}}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})
	// rt.aliveByHandle has no entry for either handle, so IsAlive answers false:
	// confirmed dead despite the destroy error.

	release, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err != nil {
		t.Fatalf("BeginSessionTeardown: %v", err)
	}
	release()
	if len(st.records) != 0 {
		t.Errorf("records = %+v, want both rows deleted once IsAlive confirmed them dead", st.records)
	}
}

// TestBeginSessionTeardownReturnsErrorAndKeepsRowWhenRuntimeStaysAlive is the
// regression for the bug where a Destroy error was logged and the row deleted
// anyway: a live PTY that survives Destroy must keep its row (still
// tracked/re-attachable) and must fail BeginSessionTeardown, so the caller
// knows not to remove the worktree out from under it.
func TestBeginSessionTeardownReturnsErrorAndKeepsRowWhenRuntimeStaysAlive(t *testing.T) {
	rt := newFakeShellRuntime()
	rt.destroyErr = errors.New("tmux: kill-session refused")
	st := &fakeShellTerminalStore{records: []ShellTerminalRecord{
		{HandleID: "shellterm-1", SessionID: "portfolio-3"},
	}}
	rt.aliveByHandle["shellterm-1"] = true // still alive despite the destroy error
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	release, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err == nil {
		t.Fatal("BeginSessionTeardown: want an error, the runtime is still alive")
	}
	if release != nil {
		t.Error("release should be nil on a failed Begin — the gate already released itself")
	}

	if len(st.records) != 1 || st.records[0].HandleID != "shellterm-1" {
		t.Fatalf("records = %+v, want the still-alive shell's row kept", st.records)
	}
}

func TestBeginSessionTeardownNoopWhenSessionHasNoShells(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{records: []ShellTerminalRecord{
		{HandleID: "shellterm-other", SessionID: "other-session"},
	}}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	release, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err != nil {
		t.Fatalf("BeginSessionTeardown: %v", err)
	}
	release()
	if len(rt.destroyed) != 0 {
		t.Errorf("destroyed = %v, want nothing torn down", rt.destroyed)
	}
	if len(st.records) != 1 {
		t.Errorf("records = %+v, want the unrelated shell left alone", st.records)
	}
}

// TestBeginSessionTeardownReleaseIsAcquisitionSpecific is the regression for
// the bug where releasing a session's gate was keyed by session id alone: a
// stray or duplicate release call for session X could unlock a DIFFERENT,
// still in-flight Begin for that same X. The release function Begin returns
// is now tied to that one acquisition — a stray call to an old release must
// not touch a newer acquisition's lock, even for the same session id.
func TestBeginSessionTeardownReleaseIsAcquisitionSpecific(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	release1, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err != nil {
		t.Fatalf("Begin 1: %v", err)
	}
	release1()

	release2, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err != nil {
		t.Fatalf("Begin 2: %v", err)
	}

	// A stray repeat of the FIRST acquisition's release must not touch the
	// second, still in-flight acquisition's hold.
	release1()

	gate := svc.sessionGateFor("portfolio-3")
	if gateIsFree(gate) {
		t.Fatal("a stray release from an old acquisition freed a different, still-open acquisition's gate")
	}

	release2()
	if !gateIsFree(gate) {
		t.Fatal("the correct acquisition's release did not free the gate")
	}
}

// gateIsFree reports whether the gate can be taken right now, leaving it as it
// found it.
func gateIsFree(g *sessionGate) bool {
	select {
	case g.ch <- struct{}{}:
		<-g.ch
		return true
	default:
		return false
	}
}

// shrinkSessionGateWait makes a contended acquire give up quickly so tests
// need not wait out the production budget.
func shrinkSessionGateWait(t *testing.T, d time.Duration) {
	t.Helper()
	orig := sessionGateWaitTimeout
	sessionGateWaitTimeout = d
	t.Cleanup(func() { sessionGateWaitTimeout = orig })
}

// TestOpenShellTerminalAbortsWhenContextCancelledWaitingForGate: sync.Mutex.Lock
// is uninterruptible, so a teardown whose workspace.Destroy stalls used to park
// every waiter forever — an HTTP handler blocked there could not notice its
// client disconnecting or its deadline passing. A waiter must now abort on
// context cancellation instead of hanging.
func TestOpenShellTerminalAbortsWhenContextCancelledWaitingForGate(t *testing.T) {
	shrinkSessionGateWait(t, 10*time.Second) // long: cancellation must win, not the budget
	rt := newFakeShellRuntime()
	projects := &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": "/repos/portfolio"}}
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"portfolio-3": {workspacePath: "", projectID: "portfolio"},
	}}
	svc := newTestServiceWithSessions(rt, &fakeShellTerminalStore{}, projects, sessions)

	release, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err != nil {
		t.Fatalf("BeginSessionTeardown: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	reachedGate := make(chan struct{})
	svc.onSessionGateWait = func(domain.SessionID) { close(reachedGate) }

	openDone := make(chan error, 1)
	go func() {
		_, err := svc.OpenShellTerminal(ctx, OpenShellTerminalInput{ProjectID: "portfolio", SessionID: "portfolio-3"})
		openDone <- err
	}()

	select {
	case <-reachedGate:
	case <-time.After(2 * time.Second):
		t.Fatal("OpenShellTerminal never reached the gate")
	}
	cancel()

	select {
	case err := <-openDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OpenShellTerminal did not abort after its context was cancelled")
	}
}

// A gate held by a wedged teardown must eventually surface a retryable error
// rather than blocking the request forever.
func TestOpenShellTerminalGivesUpWaitingForAWedgedGate(t *testing.T) {
	shrinkSessionGateWait(t, 30*time.Millisecond)
	rt := newFakeShellRuntime()
	projects := &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": "/repos/portfolio"}}
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"portfolio-3": {workspacePath: "", projectID: "portfolio"},
	}}
	svc := newTestServiceWithSessions(rt, &fakeShellTerminalStore{}, projects, sessions)

	release, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err != nil {
		t.Fatalf("BeginSessionTeardown: %v", err)
	}
	defer release() // never released before the open below gives up

	_, err = svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{ProjectID: "portfolio", SessionID: "portfolio-3"})

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindConflict {
		t.Fatalf("error = %v, want a conflict apierr the client can retry", err)
	}
}

func TestCloseShellTerminalAbortsWhenContextCancelledWaitingForGate(t *testing.T) {
	shrinkSessionGateWait(t, 10*time.Second)
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	release, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err != nil {
		t.Fatalf("BeginSessionTeardown: %v", err)
	}
	defer release()

	st.records = append(st.records, ShellTerminalRecord{HandleID: "shellterm-1", SessionID: "portfolio-3"})
	ctx, cancel := context.WithCancel(context.Background())
	reachedGate := make(chan struct{})
	svc.onSessionGateWait = func(domain.SessionID) { close(reachedGate) }

	closeDone := make(chan error, 1)
	go func() { closeDone <- svc.CloseShellTerminal(ctx, "shellterm-1") }()

	select {
	case <-reachedGate:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseShellTerminal never reached the gate")
	}
	cancel()

	select {
	case err := <-closeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseShellTerminal did not abort after its context was cancelled")
	}
}

// TestOpenShellTerminalPersistsSessionProjectWhenRequestOmitsIt: a
// session-scoped open that names no project still belongs to that session's
// project. Persisting the request's empty ProjectID verbatim would leave the
// row unattributable even though the working dir resolved through that very
// project.
func TestOpenShellTerminalPersistsSessionProjectWhenRequestOmitsIt(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	projects := &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": "/repos/portfolio"}}
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"portfolio-3": {workspacePath: "/worktrees/portfolio-3", projectID: "portfolio"},
	}}
	svc := newTestServiceWithSessions(rt, st, projects, sessions)

	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{SessionID: "portfolio-3"})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	if term.ProjectID != "portfolio" {
		t.Errorf("returned project id = %q, want the session's project", term.ProjectID)
	}
	if len(st.records) != 1 || st.records[0].ProjectID != "portfolio" {
		t.Fatalf("persisted project id = %+v, want the session's project", st.records)
	}
}

// TestBeginSessionTeardownBlocksConcurrentOpenUntilEnd is the deterministic
// interleaving proof for the race the bug shipped with: a snapshot SELECT
// could miss a shell OpenShellTerminal inserted a moment later, right before
// the worktree was destroyed. BeginSessionTeardown now holds the session's
// gate across the whole teardown window, so a concurrent Open cannot
// interleave — it blocks until the release function unlocks the gate.
//
// The onSessionGateWait hook is what makes this deterministic rather than
// timing-based: without it, "openDone hasn't fired after 50ms" is equally
// consistent with "correctly blocked" and "goroutine never got scheduled yet"
// — a broken gate that didn't block at all could still pass by accident on a
// slow machine. Waiting on the hook first proves the goroutine actually
// reached gate.mu.Lock() before asserting it's stuck there.
func TestBeginSessionTeardownBlocksConcurrentOpenUntilEnd(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	projects := &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": "/repos/portfolio"}}
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"portfolio-3": {workspacePath: "", projectID: "portfolio"},
	}}
	svc := newTestServiceWithSessions(rt, st, projects, sessions)

	release, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err != nil {
		t.Fatalf("BeginSessionTeardown: %v", err)
	}

	reachedGate := make(chan struct{})
	svc.onSessionGateWait = func(id domain.SessionID) {
		if id == "portfolio-3" {
			close(reachedGate)
		}
	}

	openDone := make(chan error, 1)
	go func() {
		_, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{ProjectID: "portfolio", SessionID: "portfolio-3"})
		openDone <- err
	}()

	select {
	case <-reachedGate:
		// The goroutine is now blocked on (or about to call) gate.mu.Lock().
	case <-time.After(2 * time.Second):
		t.Fatal("OpenShellTerminal never reached the gate acquisition point")
	}

	select {
	case <-openDone:
		t.Fatal("OpenShellTerminal returned while the teardown gate was still held")
	case <-time.After(50 * time.Millisecond):
		// Having proven it reached the gate, "still not done" now means
		// "genuinely blocked" rather than "not scheduled yet".
	}

	release()

	select {
	case err := <-openDone:
		if err != nil {
			t.Fatalf("OpenShellTerminal after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OpenShellTerminal did not unblock after release")
	}
}

func TestRenameShellTerminalUpdatesTitle(t *testing.T) {
	st := &fakeShellTerminalStore{records: []ShellTerminalRecord{
		{HandleID: "shellterm-1", Title: "portfolio", AppRunID: testAppRunID},
	}}
	svc := newTestService(newFakeShellRuntime(), st, &fakeProjectRootLocator{})

	term, err := svc.RenameShellTerminal(context.Background(), "shellterm-1", "  deploy logs  ")
	if err != nil {
		t.Fatalf("RenameShellTerminal: %v", err)
	}
	if term.Title != "deploy logs" {
		t.Errorf("returned title = %q, want the trimmed new title", term.Title)
	}
	if st.records[0].Title != "deploy logs" {
		t.Errorf("stored title = %q, want the trimmed new title", st.records[0].Title)
	}
}

func TestRenameShellTerminalRejectsEmptyTitle(t *testing.T) {
	st := &fakeShellTerminalStore{records: []ShellTerminalRecord{{HandleID: "shellterm-1", Title: "portfolio"}}}
	svc := newTestService(newFakeShellRuntime(), st, &fakeProjectRootLocator{})

	_, err := svc.RenameShellTerminal(context.Background(), "shellterm-1", "   ")

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindInvalid {
		t.Fatalf("error = %v, want an invalid apierr", err)
	}
	if st.records[0].Title != "portfolio" {
		t.Errorf("title changed to %q on a rejected rename", st.records[0].Title)
	}
}

func TestRenameShellTerminalReturnsNotFoundForUnknownHandle(t *testing.T) {
	svc := newTestService(newFakeShellRuntime(), &fakeShellTerminalStore{}, &fakeProjectRootLocator{})

	_, err := svc.RenameShellTerminal(context.Background(), "shellterm-ghost", "whatever")

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindNotFound {
		t.Fatalf("error = %v, want a not-found apierr", err)
	}
}

// A row that names a PTY nobody spawned would be re-attached forever after a
// restart, so a failed insert must take the runtime down with it.
func TestOpenShellTerminalDestroysRuntimeWhenPersistFails(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{insertErr: errors.New("disk full")}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	if _, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{}); err == nil {
		t.Fatal("OpenShellTerminal succeeded despite a failed insert")
	}
	if len(rt.destroyed) != 1 {
		t.Fatalf("destroyed runtimes = %v, want the spawned PTY rolled back", rt.destroyed)
	}
	if rt.destroyed[0] != string(rt.created[0].SessionID) {
		t.Errorf("destroyed %q, want the handle that was just created", rt.destroyed[0])
	}
}

func TestCloseShellTerminalDestroysRuntimeAndDeletesRecord(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	if err := svc.CloseShellTerminal(context.Background(), term.HandleID); err != nil {
		t.Fatalf("CloseShellTerminal: %v", err)
	}

	if len(st.records) != 0 {
		t.Errorf("records = %+v, want the row deleted", st.records)
	}
	if len(rt.destroyed) != 1 || rt.destroyed[0] != term.HandleID {
		t.Errorf("destroyed = %v, want %q", rt.destroyed, term.HandleID)
	}
}

func TestCloseShellTerminalReturnsNotFoundForUnknownHandle(t *testing.T) {
	svc := newTestService(newFakeShellRuntime(), &fakeShellTerminalStore{}, &fakeProjectRootLocator{})

	err := svc.CloseShellTerminal(context.Background(), "shellterm-missing")

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindNotFound {
		t.Fatalf("error = %v, want a not-found apierr", err)
	}
}

// TestCloseShellTerminalKeepsRowWhenRuntimeStaysAlive is the regression for
// the bug where CloseShellTerminal deleted the row BEFORE attempting Destroy,
// so a shell that survived (destroy failed and IsAlive confirms it) would
// vanish from tracking while still running. A later session teardown that
// scans for this session's shells would then find nothing and remove the
// worktree out from under it. The row must now be kept, and the caller told
// the close didn't actually take.
func TestCloseShellTerminalKeepsRowWhenRuntimeStaysAlive(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	rt.destroyErr = errors.New("tmux: kill-session refused")
	// aliveByHandle already has term.HandleID from the open above: still alive
	// despite the destroy error.

	err = svc.CloseShellTerminal(context.Background(), term.HandleID)

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindConflict {
		t.Fatalf("error = %v, want a conflict apierr", err)
	}
	if len(st.records) != 1 || st.records[0].HandleID != term.HandleID {
		t.Fatalf("records = %+v, want the still-alive shell's row kept", st.records)
	}
}

// TestCloseShellTerminalBlocksUntilSessionTeardownReleases is the regression
// for the bug where CloseShellTerminal never took the session gate at all: it
// deleted a session-scoped shell's row directly, so a BeginSessionTeardown
// racing the same close could run its SELECT before the delete landed, see
// nothing to drain, and let Session Manager remove the worktree while the
// close's own Destroy call was still in flight or had failed. Close must now
// serialize against a teardown for the same session exactly like Open does.
func TestCloseShellTerminalBlocksUntilSessionTeardownReleases(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	// Nothing scoped to the session yet, so Begin's own drain finds nothing and
	// succeeds trivially — it still holds the gate via the returned release.
	release, err := svc.BeginSessionTeardown(context.Background(), "portfolio-3")
	if err != nil {
		t.Fatalf("BeginSessionTeardown: %v", err)
	}

	// The row Close will target appears only now — standing in for a shell
	// that showed up while the session's gate was already held, which is
	// exactly the interleaving the gate exists to serialize against.
	st.records = append(st.records, ShellTerminalRecord{HandleID: "shellterm-1", SessionID: "portfolio-3"})
	rt.aliveByHandle["shellterm-1"] = true

	reachedGate := make(chan struct{})
	svc.onSessionGateWait = func(id domain.SessionID) {
		if id == "portfolio-3" {
			close(reachedGate)
		}
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- svc.CloseShellTerminal(context.Background(), "shellterm-1")
	}()

	select {
	case <-reachedGate:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseShellTerminal never reached the gate acquisition point")
	}

	select {
	case <-closeDone:
		t.Fatal("CloseShellTerminal returned while the teardown gate was still held")
	case <-time.After(50 * time.Millisecond):
	}

	release()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("CloseShellTerminal after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseShellTerminal did not unblock after release")
	}
}

// A shell with no session (opened from the topbar or /terminals) has no
// worktree teardown to race, so closing it must never touch the gate at all.
func TestCloseShellTerminalDoesNotGateStandaloneShells(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	if err := svc.CloseShellTerminal(context.Background(), term.HandleID); err != nil {
		t.Fatalf("CloseShellTerminal: %v", err)
	}
	if len(svc.gates) != 0 {
		t.Errorf("gates = %+v, want no gate allocated for a session-less shell", svc.gates)
	}
}

// The daemon may restart under a live app; the shells it left behind are still
// running and must come back as attachable tabs.
func TestListShellTerminalsForCurrentAppRunReturnsSurvivingTerminals(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})
	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}

	// A fresh Service over the SAME store and runtime stands in for the daemon
	// coming back up within one app run.
	restarted := NewService(rt, st, &fakeProjectRootLocator{}, &fakeSessionWorkspaceLocator{}, "/data/dir", testAppRunID, testLogger())
	got, err := restarted.ListShellTerminalsForCurrentAppRun(context.Background())
	if err != nil {
		t.Fatalf("ListShellTerminalsForCurrentAppRun: %v", err)
	}
	if len(got) != 1 || got[0].HandleID != term.HandleID {
		t.Fatalf("terminals = %+v, want the surviving handle %q", got, term.HandleID)
	}
}

func TestListShellTerminalsForCurrentAppRunPrunesTerminalsWhoseShellExited(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})
	term, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	delete(rt.aliveByHandle, term.HandleID) // the user typed `exit`

	got, err := svc.ListShellTerminalsForCurrentAppRun(context.Background())
	if err != nil {
		t.Fatalf("ListShellTerminalsForCurrentAppRun: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("terminals = %+v, want the dead shell pruned", got)
	}
	if len(st.records) != 0 {
		t.Errorf("records = %+v, want the dead row deleted", st.records)
	}
}

// A probe ERROR is not proof of death — the same rule internal/terminal applies
// on attach. A transient runtime hiccup must not delete a working terminal.
func TestListShellTerminalsForCurrentAppRunKeepsTerminalWhenLivenessProbeErrors(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})
	if _, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{}); err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	rt.aliveErr = errors.New("tmux server unreachable")

	got, err := svc.ListShellTerminalsForCurrentAppRun(context.Background())
	if err != nil {
		t.Fatalf("ListShellTerminalsForCurrentAppRun: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("terminals = %+v, want the row kept through a failed probe", got)
	}
	if len(st.records) != 1 {
		t.Errorf("records = %+v, want the row kept through a failed probe", st.records)
	}
}

// The app was force-killed, so nothing closed its shells. The next boot must
// sweep them rather than leak PTYs, while leaving the new run's shells alone.
func TestReapShellTerminalsFromPreviousAppRunsDestroysOrphansOnly(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{records: []ShellTerminalRecord{
		{HandleID: "shellterm-orphan1", AppRunID: "app-run-crashed", WorkingDir: "/a"},
		{HandleID: "shellterm-orphan2", AppRunID: "app-run-crashed", WorkingDir: "/b"},
		{HandleID: "shellterm-current", AppRunID: testAppRunID, WorkingDir: "/c"},
	}}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	cleared, err := svc.ReapShellTerminalsFromPreviousAppRuns(context.Background())
	if err != nil {
		t.Fatalf("ReapShellTerminalsFromPreviousAppRuns: %v", err)
	}
	if cleared != 2 {
		t.Errorf("cleared = %d, want 2", cleared)
	}
	if len(rt.destroyed) != 2 {
		t.Errorf("destroyed = %v, want both orphaned PTYs torn down", rt.destroyed)
	}
	if len(st.records) != 1 || st.records[0].HandleID != "shellterm-current" {
		t.Errorf("records = %+v, want only the current run's shell kept", st.records)
	}
}

// One un-destroyable PTY must not wedge the sweep: the rows are cleared anyway,
// or every future boot would retry the same failure forever.
func TestReapShellTerminalsFromPreviousAppRunsClearsRowsWhenDestroyFails(t *testing.T) {
	rt := newFakeShellRuntime()
	rt.destroyErr = errors.New("tmux: no such session")
	st := &fakeShellTerminalStore{records: []ShellTerminalRecord{
		{HandleID: "shellterm-orphan", AppRunID: "app-run-crashed", WorkingDir: "/a"},
	}}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	cleared, err := svc.ReapShellTerminalsFromPreviousAppRuns(context.Background())
	if err != nil {
		t.Fatalf("ReapShellTerminalsFromPreviousAppRuns: %v", err)
	}
	if cleared != 1 {
		t.Errorf("cleared = %d, want the row cleared despite the destroy failure", cleared)
	}
	if len(st.records) != 0 {
		t.Errorf("records = %+v, want cleared", st.records)
	}
}

// TestReapShellTerminalsFromPreviousAppRunsKeepsRowForConfirmedLiveOrphan is
// the boot-order regression: the old Reap bulk-deleted every orphan row after
// best-effort destroys, regardless of whether each one actually died. A shell
// that survived a crash independently of the daemon (its OS-level tmux/conpty
// session outlives the process) would then have its row wiped anyway —
// invisible to a later BeginSessionTeardown for the session it's scoped to,
// which would find nothing to drain and let that session's worktree be
// removed while the orphaned shell is still attached to it. Reap must keep
// that row instead, so the later teardown still sees it.
func TestReapShellTerminalsFromPreviousAppRunsKeepsRowForConfirmedLiveOrphan(t *testing.T) {
	rt := newFakeShellRuntime()
	rt.destroyErr = errors.New("tmux: kill-session refused")
	rt.aliveByHandle["shellterm-orphan-alive"] = true // survives the crash, still alive
	st := &fakeShellTerminalStore{records: []ShellTerminalRecord{
		{HandleID: "shellterm-orphan-alive", SessionID: "mer-1", AppRunID: "app-run-crashed", WorkingDir: "/a"},
		{HandleID: "shellterm-orphan-dead", AppRunID: "app-run-crashed", WorkingDir: "/b"},
	}}
	svc := newTestService(rt, st, &fakeProjectRootLocator{})

	cleared, err := svc.ReapShellTerminalsFromPreviousAppRuns(context.Background())
	if err != nil {
		t.Fatalf("ReapShellTerminalsFromPreviousAppRuns: %v", err)
	}
	if cleared != 1 {
		t.Errorf("cleared = %d, want only the confirmed-dead orphan counted", cleared)
	}
	if len(st.records) != 1 || st.records[0].HandleID != "shellterm-orphan-alive" {
		t.Fatalf("records = %+v, want the still-alive orphan's row kept", st.records)
	}

	// The row must still be there for a later teardown scan of its session to
	// find — proving Reap didn't just keep it by accident but that it stays
	// visible to exactly the query that matters.
	remaining, err := st.SelectShellTerminalsBySessionID(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("SelectShellTerminalsBySessionID: %v", err)
	}
	if len(remaining) != 1 || remaining[0].HandleID != "shellterm-orphan-alive" {
		t.Fatalf("session-scoped lookup after reap = %+v, want the surviving orphan visible", remaining)
	}
}

func TestShellTerminalTitleFallsBackForRootlessPaths(t *testing.T) {
	if got := shellTerminalTitle(""); got != "Shell" {
		t.Errorf("title for empty path = %q, want %q", got, "Shell")
	}
	if got := shellTerminalTitle("/repos/portfolio"); got != "portfolio" {
		t.Errorf("title = %q, want %q", got, "portfolio")
	}
}
