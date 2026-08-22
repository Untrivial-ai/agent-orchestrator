package tmux

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestContainmentUnitNameIsDeterministic(t *testing.T) {
	if got, want := containmentUnitName("session-42", "launch-1"), "ao-session-session-42-aad36a428ce91190.scope"; got != want {
		t.Fatalf("containmentUnitName = %q, want %q", got, want)
	}
	if got := containmentUnitName("session with spaces", "launch-1"); strings.ContainsAny(got, " '") {
		t.Fatalf("containmentUnitName contains unsafe characters: %q", got)
	}
	if containmentUnitName("session-42", "launch-1") == containmentUnitName("session-42", "launch-2") {
		t.Fatal("different launch generations received the same containment unit")
	}
}

func TestSystemdWrapCommandKeepsExistingShellAsOneArgument(t *testing.T) {
	s := newSystemdContainment(&fakeRunner{}, time.Second, 5*time.Second)
	got := s.WrapCommand("/bin/sh", "cd '/tmp/ws'; exec 'codex'", "ao-session-s1.scope", 5*time.Second)
	want := `exec systemd-run --user --scope --collect --unit=ao-session-s1.scope ` +
		`--property=KillMode=control-group --property=TimeoutStopSec=5s ` +
		`--property=SendSIGKILL=yes -- '/bin/sh' -c 'cd '\''/tmp/ws'\''; exec '\''codex'\'''`
	if got != want {
		t.Fatalf("wrapped launch command = %q, want %q", got, want)
	}
}

func TestParseSystemdUnitState(t *testing.T) {
	got, err := parseSystemdUnitState("LoadState=loaded\nActiveState=inactive\nSubState=dead\n")
	if err != nil {
		t.Fatal(err)
	}
	if !got.released() || got.active() {
		t.Fatalf("state = %+v, want released and not active", got)
	}
	if _, err := parseSystemdUnitState("LoadState=loaded\nActiveState=active\n"); err == nil {
		t.Fatal("parseSystemdUnitState accepted missing SubState")
	}
	if _, err := parseSystemdUnitState("not-a-state-line"); err == nil {
		t.Fatal("parseSystemdUnitState accepted malformed line")
	}
}

func TestSystemdReleaseAcceptsInactiveAndMissing(t *testing.T) {
	tests := []struct {
		name   string
		state  string
		wantOK bool
	}{
		{name: "inactive", state: "LoadState=loaded\nActiveState=inactive\nSubState=dead\n", wantOK: true},
		{name: "not-found", state: "LoadState=not-found\nActiveState=inactive\nSubState=dead\n", wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{outputs: [][]byte{nil, []byte(tc.state)}}
			s := newSystemdContainment(fr, time.Second, time.Second)
			s.poll = time.Millisecond
			if err := s.Release(context.Background(), "ao-session-s1.scope"); (err == nil) != tc.wantOK {
				t.Fatalf("Release error = %v, wantOK=%v", err, tc.wantOK)
			}
			if len(fr.calls) != 2 || !reflect.DeepEqual(fr.calls[0].args, []string{"--user", "stop", "ao-session-s1.scope"}) {
				t.Fatalf("systemctl calls = %#v", fr.calls)
			}
		})
	}
}

func TestSystemdReleaseRejectsActiveAndMalformedState(t *testing.T) {
	for _, state := range []string{
		"LoadState=loaded\nActiveState=active\nSubState=running\n",
		"LoadState=loaded\nActiveState=deactivating\nSubState=stop-sigterm\n",
		"LoadState=loaded\nActiveState=unknown\nSubState=unknown\n",
	} {
		t.Run(strings.ReplaceAll(state, "\n", "/"), func(t *testing.T) {
			fr := &fakeRunner{outputs: [][]byte{nil, []byte(state)}}
			s := newSystemdContainment(fr, 20*time.Millisecond, 0)
			s.poll = time.Millisecond
			err := s.Release(context.Background(), "ao-session-s1.scope")
			if err == nil {
				t.Fatalf("Release(%q) = nil, want error", state)
			}
		})
	}
}

func TestSystemdWaitActiveIsBoundedWhenUnitNeverAppears(t *testing.T) {
	outputs := make([][]byte, 32)
	for i := range outputs {
		outputs[i] = []byte("LoadState=not-found\nActiveState=inactive\nSubState=dead\n")
	}
	s := newSystemdContainment(&fakeRunner{outputs: outputs}, 10*time.Millisecond, 0)
	s.poll = time.Millisecond
	if err := s.WaitActive(context.Background(), "ao-session-never.scope"); err == nil || !strings.Contains(err.Error(), "did not become active") {
		t.Fatalf("WaitActive error = %v, want bounded timeout", err)
	}
}

type fakeContainment struct {
	validateErr error
	waitErr     error
	releaseErr  error
	wrap        string
	validated   int
	wrapped     []string
	waited      []string
	released    []string
	releaseCtxs []error
}

func (f *fakeContainment) Validate(context.Context) error {
	f.validated++
	return f.validateErr
}

func (f *fakeContainment) WrapCommand(_, launchCmd, unit string, _ time.Duration) string {
	f.wrapped = append(f.wrapped, unit+":"+launchCmd)
	if f.wrap != "" {
		return f.wrap
	}
	return launchCmd
}

func (f *fakeContainment) WaitActive(_ context.Context, unit string) error {
	f.waited = append(f.waited, unit)
	return f.waitErr
}

func (f *fakeContainment) Release(ctx context.Context, unit string) error {
	f.released = append(f.released, unit)
	f.releaseCtxs = append(f.releaseCtxs, ctx.Err())
	return f.releaseErr
}

func scopedConfig(launchID string) ports.RuntimeConfig {
	return ports.RuntimeConfig{
		SessionID:       "sess-1",
		WorkspacePath:   "/tmp/ws",
		Argv:            []string{"codex"},
		RuntimeLaunchID: launchID,
	}
}

func scopedHandle(launchID string) ports.RuntimeHandle {
	return ports.RuntimeHandle{ID: "sess-1", RuntimeLaunchID: launchID}
}

func TestScopedCreateAddsRemainOnExitAndVerifiesScope(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{wrap: "wrapped-launch"}
	r.containment = fc
	fr.outputs = [][]byte{nil, nil, []byte("/tmp/ws\n"), nil, nil, nil, nil}
	handle, err := r.Create(context.Background(), scopedConfig("launch-1"))
	if err != nil {
		t.Fatal(err)
	}
	if handle != scopedHandle("launch-1") {
		t.Fatalf("Create handle = %+v", handle)
	}
	wantUnit := containmentUnitName("sess-1", "launch-1")
	if fc.validated != 1 || len(fc.waited) != 1 || fc.waited[0] != wantUnit {
		t.Fatalf("containment lifecycle = validated %d, waited %#v", fc.validated, fc.waited)
	}
	if len(fr.calls) < 2 || !reflect.DeepEqual(fr.calls[1].args, setRemainOnExitArgs("sess-1")) {
		t.Fatalf("calls = %#v, want remain-on-exit after new-session", fr.calls)
	}
	if !strings.Contains(strings.Join(fr.calls[0].args, " "), "wrapped-launch") || !reflect.DeepEqual(fr.calls[0].args, scopedNewSessionArgs("sess-1", "/tmp/ws", "/bin/sh", "wrapped-launch", "launch-1")) {
		t.Fatalf("new-session did not atomically create and mark generation: %#v", fr.calls[0].args)
	}
}

func TestScopedCreateReportsCleanupFailureWhenReadinessFails(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &scriptedReleaseContainment{
		waitErr:     errors.New("scope state unavailable"),
		releaseErrs: []error{errors.New("scope still active")},
	}
	r.containment = fc
	fr.outputs = [][]byte{nil, nil, nil}

	handle, err := r.Create(context.Background(), scopedConfig("launch-1"))
	if err == nil || !strings.Contains(err.Error(), "scope state unavailable") || !strings.Contains(err.Error(), "scope still active") {
		t.Fatalf("Create error = %v, want readiness and cleanup failures", err)
	}
	if disposition, ref := ports.RuntimeCreateFailureOf(err); disposition != ports.RuntimeCreatePreserve || ref != scopedHandle("launch-1") || handle != ref {
		t.Fatalf("Create classification = %v, ref %+v, handle %+v", disposition, ref, handle)
	}
	if countCalls(fr, "if-shell") != 1 || !reflect.DeepEqual(fc.released, []string{containmentUnitName("sess-1", "launch-1")}) {
		t.Fatalf("runtime calls = %#v, releases = %#v", fr.calls, fc.released)
	}
}

func TestScopedCreateCleanupIgnoresCallerCancellation(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{}
	r.containment = fc
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fr.outputs = [][]byte{nil, nil, nil, nil}
	fr.hook = func(ctx context.Context, call int) error {
		if call == 3 {
			cancel()
			return ctx.Err()
		}
		return nil
	}

	_, err := r.Create(ctx, scopedConfig("launch-1"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want caller cancellation", err)
	}
	if disposition, _ := ports.RuntimeCreateFailureOf(err); disposition != ports.RuntimeCreateRollbackSafe {
		t.Fatalf("Create disposition = %v, want rollback safe after exact cleanup", disposition)
	}
	if countCalls(fr, "if-shell") != 1 || len(fc.releaseCtxs) != 1 || fc.releaseCtxs[0] != nil {
		t.Fatalf("runtime calls = %#v, release contexts = %#v", fr.calls, fc.releaseCtxs)
	}
}

func TestScopedCreateCleansUncertainNewSessionOutcome(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{}
	r.containment = fc
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fr.outputs = [][]byte{nil, nil}
	fr.hook = func(ctx context.Context, call int) error {
		if call == 1 {
			cancel()
			return ctx.Err()
		}
		return nil
	}

	_, err := r.Create(ctx, scopedConfig("launch-1"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want uncertain cancellation", err)
	}
	if disposition, ref := ports.RuntimeCreateFailureOf(err); disposition != ports.RuntimeCreateRollbackSafe || ref != (ports.RuntimeHandle{}) {
		t.Fatalf("Create classification = %v, ref %+v", disposition, ref)
	}
	if countCalls(fr, "if-shell") != 1 || !reflect.DeepEqual(fc.released, []string{containmentUnitName("sess-1", "launch-1")}) {
		t.Fatalf("runtime calls = %#v, releases = %#v", fr.calls, fc.released)
	}
}

func TestScopedCreateDoesNotCleanConcurrentDuplicate(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{}
	r.containment = fc
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fr.outputs = [][]byte{[]byte("duplicate session: sess-1")}
	fr.hook = func(ctx context.Context, call int) error {
		if call == 1 {
			cancel()
			return ctx.Err()
		}
		return nil
	}

	handle, err := r.Create(ctx, scopedConfig("launch-1"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want uncertain cancellation", err)
	}
	if disposition, ref := ports.RuntimeCreateFailureOf(err); disposition != ports.RuntimeCreatePreserve || ref != scopedHandle("launch-1") || handle != ref {
		t.Fatalf("Create classification = %v, ref %+v, handle %+v", disposition, ref, handle)
	}
	if countCalls(fr, "if-shell") != 0 || len(fc.released) != 0 {
		t.Fatalf("concurrent duplicate was cleaned: runtime calls = %#v, releases = %#v", fr.calls, fc.released)
	}
}

func TestScopedCreatePreservesForeignGenerationAfterUncertainOutcome(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{}
	r.containment = fc
	fr.outputs = [][]byte{nil, []byte(runtimeLaunchReportPrefix + "launch-2\n")}
	fr.hook = func(_ context.Context, call int) error {
		if call == 1 {
			return errors.New("new-session outcome unknown")
		}
		return nil
	}

	handle, err := r.Create(context.Background(), scopedConfig("launch-1"))
	if err == nil || !strings.Contains(err.Error(), "new-session outcome unknown") {
		t.Fatalf("Create error = %v", err)
	}
	if disposition, ref := ports.RuntimeCreateFailureOf(err); disposition != ports.RuntimeCreatePreserve || ref != scopedHandle("launch-1") || handle != ref {
		t.Fatalf("Create classification = %v, ref %+v, handle %+v", disposition, ref, handle)
	}
	if countCalls(fr, "if-shell") != 1 || !reflect.DeepEqual(fc.released, []string{containmentUnitName("sess-1", "launch-1")}) {
		t.Fatalf("runtime calls = %#v, releases = %#v", fr.calls, fc.released)
	}
}

func TestScopedCreatePreservesUnmarkedSessionAfterUncertainOutcome(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{}
	r.containment = fc
	fr.outputs = [][]byte{nil, []byte(runtimeLaunchReportPrefix + "\n")}
	fr.hook = func(_ context.Context, call int) error {
		if call == 1 {
			return errors.New("new-session outcome unknown")
		}
		return nil
	}

	handle, err := r.Create(context.Background(), scopedConfig("launch-1"))
	if disposition, ref := ports.RuntimeCreateFailureOf(err); disposition != ports.RuntimeCreatePreserve || ref != scopedHandle("launch-1") || handle != ref {
		t.Fatalf("Create classification = %v, ref %+v, handle %+v, err %v", disposition, ref, handle, err)
	}
	if countCalls(fr, "if-shell") != 1 || len(fc.released) != 1 {
		t.Fatalf("runtime calls = %#v, releases = %#v", fr.calls, fc.released)
	}
}

func TestScopedCreatePreservesWhenGenerationFenceFails(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{}
	r.containment = fc
	fr.outputs = [][]byte{nil, []byte("tmux server unavailable")}
	fr.hook = func(_ context.Context, call int) error {
		switch call {
		case 1:
			return errors.New("new-session outcome unknown")
		case 2:
			return errors.New("fence unavailable")
		}
		return nil
	}

	handle, err := r.Create(context.Background(), scopedConfig("launch-1"))
	if disposition, ref := ports.RuntimeCreateFailureOf(err); disposition != ports.RuntimeCreatePreserve || ref != scopedHandle("launch-1") || handle != ref {
		t.Fatalf("Create classification = %v, ref %+v, handle %+v, err %v", disposition, ref, handle, err)
	}
}

func TestScopedRestartReleasesBeforeRespawn(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{}
	r.containment = fc
	fr.outputs = [][]byte{nil, nil}
	h, err := r.Restart(context.Background(), scopedHandle("launch-1"), scopedConfig("launch-2"))
	if err != nil {
		t.Fatal(err)
	}
	if h != scopedHandle("launch-2") || !reflect.DeepEqual(fc.released, []string{containmentUnitName("sess-1", "launch-1")}) {
		t.Fatalf("restart = %+v, releases %#v", h, fc.released)
	}
	if !reflect.DeepEqual(fc.waited, []string{containmentUnitName("sess-1", "launch-2")}) {
		t.Fatalf("waited = %#v", fc.waited)
	}
	if len(fr.calls) != 2 || fr.calls[0].args[0] != "if-shell" {
		t.Fatalf("calls = %#v, want fenced respawn and liveness", fr.calls)
	}
}

func TestScopedRestartPreservesForeignGeneration(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{}
	r.containment = fc
	fr.outputs = [][]byte{[]byte(runtimeLaunchReportPrefix + "launch-foreign\n")}

	_, err := r.Restart(context.Background(), scopedHandle("launch-1"), scopedConfig("launch-2"))
	if err == nil || !strings.Contains(err.Error(), "observed \"launch-foreign\"") {
		t.Fatalf("Restart error = %v", err)
	}
	if len(fr.calls) != 1 || fr.calls[0].args[0] != "if-shell" {
		t.Fatalf("runtime calls = %#v", fr.calls)
	}
	if !reflect.DeepEqual(fc.released, []string{containmentUnitName("sess-1", "launch-1")}) || len(fc.waited) != 0 {
		t.Fatalf("containment releases = %#v, waits = %#v", fc.released, fc.waited)
	}
}

func TestScopedRestartDoesNotRespawnAfterReleaseFailure(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{releaseErr: errors.New("scope still active")}
	r.containment = fc
	_, err := r.Restart(context.Background(), scopedHandle("launch-1"), scopedConfig("launch-2"))
	if err == nil || !strings.Contains(err.Error(), "scope still active") {
		t.Fatalf("Restart error = %v, want containment failure", err)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("runtime called after release failure: %#v", fr.calls)
	}
}

func TestScopedRestartReleasesReplacementWhenReadinessFails(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{waitErr: errors.New("scope state unavailable")}
	r.containment = fc
	fr.outputs = [][]byte{nil, nil}
	_, err := r.Restart(context.Background(), scopedHandle("launch-1"), scopedConfig("launch-2"))
	if err == nil || !strings.Contains(err.Error(), "scope state unavailable") {
		t.Fatalf("Restart error = %v, want readiness failure", err)
	}
	wantReleases := []string{containmentUnitName("sess-1", "launch-1"), containmentUnitName("sess-1", "launch-2")}
	if !reflect.DeepEqual(fc.released, wantReleases) {
		t.Fatalf("releases = %#v, want old scope release plus failed replacement cleanup %#v", fc.released, wantReleases)
	}
	if len(fr.calls) != 2 || fr.calls[0].args[0] != "if-shell" || fr.calls[1].args[0] != "if-shell" {
		t.Fatalf("runtime calls = %#v, want fenced respawn and atomic marker restore", fr.calls)
	}
}

func TestScopedRestartReleasesReplacementWhenRespawnFails(t *testing.T) {
	r, _ := newTestRuntime(0)
	fr := &fakeRunnerSequence{results: []fakeRunnerResult{
		{err: errors.New("respawn outcome unknown")},
		{},
	}}
	r.runner = fr
	fc := &fakeContainment{}
	r.containment = fc
	_, err := r.Restart(context.Background(), scopedHandle("launch-1"), scopedConfig("launch-2"))
	if err == nil || !strings.Contains(err.Error(), "respawn outcome unknown") {
		t.Fatalf("Restart error = %v, want respawn failure", err)
	}
	wantReleases := []string{containmentUnitName("sess-1", "launch-1"), containmentUnitName("sess-1", "launch-2")}
	if !reflect.DeepEqual(fc.released, wantReleases) {
		t.Fatalf("releases = %#v, want old scope release plus uncertain replacement cleanup %#v", fc.released, wantReleases)
	}
}

func TestScopedRestartReleasesReplacementWhenLivenessProbeFails(t *testing.T) {
	r, _ := newTestRuntime(0)
	fr := &fakeRunnerSequence{results: []fakeRunnerResult{
		{},
		{err: errors.New("tmux probe unavailable")},
		{},
	}}
	r.runner = fr
	fc := &fakeContainment{}
	r.containment = fc
	_, err := r.Restart(context.Background(), scopedHandle("launch-1"), scopedConfig("launch-2"))
	if err == nil || !strings.Contains(err.Error(), "tmux probe unavailable") {
		t.Fatalf("Restart error = %v, want liveness failure", err)
	}
	wantReleases := []string{containmentUnitName("sess-1", "launch-1"), containmentUnitName("sess-1", "launch-2")}
	if !reflect.DeepEqual(fc.released, wantReleases) {
		t.Fatalf("releases = %#v, want old scope release plus failed replacement cleanup %#v", fc.released, wantReleases)
	}
}

func TestScopedRestartReportsReplacementCleanupFailure(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &scriptedReleaseContainment{
		waitErr:     errors.New("scope state unavailable"),
		releaseErrs: []error{nil, errors.New("scope still active")},
	}
	r.containment = fc
	fr.outputs = [][]byte{nil}
	_, err := r.Restart(context.Background(), scopedHandle("launch-1"), scopedConfig("launch-2"))
	if err == nil || !strings.Contains(err.Error(), "scope state unavailable") || !strings.Contains(err.Error(), "scope still active") {
		t.Fatalf("Restart error = %v, want readiness and cleanup failures", err)
	}
	if len(fr.calls) != 1 || len(fc.released) != 2 {
		t.Fatalf("runtime calls = %#v, releases = %#v", fr.calls, fc.released)
	}
}

func TestScopedRestartCleanupPreservesForeignGeneration(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{waitErr: errors.New("scope state unavailable")}
	r.containment = fc
	fr.outputs = [][]byte{nil, []byte(runtimeLaunchReportPrefix + "launch-foreign\n")}

	_, err := r.Restart(context.Background(), scopedHandle("launch-1"), scopedConfig("launch-2"))
	if err == nil || !strings.Contains(err.Error(), "changed to \"launch-foreign\"") {
		t.Fatalf("Restart error = %v, want foreign generation preservation", err)
	}
	if len(fr.calls) != 2 || fr.calls[1].args[0] != "if-shell" {
		t.Fatalf("runtime calls = %#v, want atomic cleanup fence", fr.calls)
	}
}

type scriptedReleaseContainment struct {
	waitErr     error
	releaseErrs []error
	released    []string
}

func (*scriptedReleaseContainment) Validate(context.Context) error { return nil }

func (*scriptedReleaseContainment) WrapCommand(_, launchCmd, _ string, _ time.Duration) string {
	return launchCmd
}

func (s *scriptedReleaseContainment) WaitActive(context.Context, string) error {
	return s.waitErr
}

func (s *scriptedReleaseContainment) Release(_ context.Context, unit string) error {
	s.released = append(s.released, unit)
	if len(s.releaseErrs) == 0 {
		return nil
	}
	err := s.releaseErrs[0]
	s.releaseErrs = s.releaseErrs[1:]
	return err
}

func TestScopedDestroyReleasesEvenWhenTmuxIsMissing(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{}
	r.containment = fc
	fr.outputs = [][]byte{[]byte("can't find session: sess-1")}
	fr.err = &exec.ExitError{}
	if err := r.Destroy(context.Background(), scopedHandle("launch-1")); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 1 || fr.calls[0].args[0] != "if-shell" {
		t.Fatalf("calls = %#v, want only exact-generation fence", fr.calls)
	}
	if !reflect.DeepEqual(fc.released, []string{containmentUnitName("sess-1", "launch-1")}) {
		t.Fatalf("released = %#v", fc.released)
	}
}

func TestScopedDestroyPreservesForeignGenerationAndBlocksWorkspaceTransition(t *testing.T) {
	r, fr := newTestRuntime(0)
	fc := &fakeContainment{}
	r.containment = fc
	fr.outputs = [][]byte{[]byte(runtimeLaunchReportPrefix + "launch-2\n")}

	err := r.Destroy(context.Background(), scopedHandle("launch-1"))
	if err == nil || !strings.Contains(err.Error(), "foreign runtime preserved") {
		t.Fatalf("Destroy error = %v", err)
	}
	if len(fr.calls) != 1 || fr.calls[0].args[0] != "if-shell" {
		t.Fatalf("runtime calls = %#v", fr.calls)
	}
	if !reflect.DeepEqual(fc.released, []string{containmentUnitName("sess-1", "launch-1")}) {
		t.Fatalf("released = %#v", fc.released)
	}
}
