package fake

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifestReportsFakeHarness(t *testing.T) {
	m := New().Manifest()
	if m.ID != "fake" {
		t.Fatalf("Manifest().ID = %q, want %q", m.ID, "fake")
	}
	if domain.AgentHarness(m.ID) != domain.HarnessFake {
		t.Fatalf("manifest id %q does not match domain.HarnessFake %q", m.ID, domain.HarnessFake)
	}
	if domain.HarnessFake.IsKnown() {
		t.Fatal("domain.HarnessFake must not be registered as a user-selectable harness")
	}
	found := false
	for _, c := range m.Capabilities {
		if c == "agent" {
			found = true
		}
	}
	if !found {
		t.Fatalf("manifest missing agent capability: %#v", m.Capabilities)
	}
}

func TestGetLaunchCommandIsScriptedTimeline(t *testing.T) {
	// Fixed speedup so the emitted sleep is deterministic in the assertions.
	t.Setenv(SpeedupEnv, "4")
	cmd, err := New().GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt: "ignored by the fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	// argv[0] must be the RESOLVED shell path (what Manager.Spawn validates), not
	// a bare "sh" — see ResolveBinary / #2692 review.
	if len(cmd) != 3 || cmd[1] != "-lc" || !strings.HasSuffix(cmd[0], "sh") || !strings.Contains(cmd[0], "/") {
		t.Fatalf("launch command shape = %#v, want [<resolved sh path> -lc <script>]", cmd)
	}

	script := cmd[2]
	// Events must appear in timeline order:
	// active -> waiting_input -> active -> pr-push -> blocked -> done(exited).
	wantOrder := []string{
		"ao hooks fake session-start",
		"ao hooks fake agent-needs-input",
		"ao hooks fake user-prompt-submit",
		"ao hooks fake pr-push",
		"ao hooks fake permission-request",
		"ao hooks fake session-end",
	}
	last := 0
	for _, want := range wantOrder {
		idx := strings.Index(script[last:], want)
		if idx < 0 {
			t.Fatalf("script missing %q in order; script:\n%s", want, script)
		}
		last += idx + len(want)
	}

	// basePhaseSeconds (2) / speedup (4) = 0.5.
	if !strings.Contains(script, "sleep 0.5") {
		t.Fatalf("script does not sleep at the sped-up cadence (0.5s); script:\n%s", script)
	}
	// Hooks must read from /dev/null and never fail the timeline.
	if !strings.Contains(script, "</dev/null") || !strings.Contains(script, "|| true") {
		t.Fatalf("hook invocations are not guarded/stdin-fed; script:\n%s", script)
	}
}

func TestResolveBinaryReturnsResolvedShellPath(t *testing.T) {
	got, err := New().ResolveBinary(context.Background())
	if err != nil {
		t.Fatalf("ResolveBinary: unexpected error: %v", err)
	}
	if !strings.HasSuffix(got, "sh") || !strings.Contains(got, "/") {
		t.Fatalf("ResolveBinary = %q, want an absolute resolved sh path", got)
	}
}

// When no runnable sh is on PATH (Windows / stripped PATH), the fake must report
// NOT installed (ResolveBinary errors) so preflight fails cleanly, and
// GetLaunchCommand must surface the same error rather than emit an unlaunchable
// bare "sh" that Manager.Spawn would later reject. (#2692 review)
func TestResolveBinaryErrorsWhenNoShell(t *testing.T) {
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-no-sh-here"))
	if got, err := New().ResolveBinary(context.Background()); err == nil {
		t.Fatalf("ResolveBinary = %q, want an error when no sh is on PATH", got)
	}
	if cmd, err := New().GetLaunchCommand(context.Background(), ports.LaunchConfig{}); err == nil {
		t.Fatalf("GetLaunchCommand = %#v, want an error when no sh is on PATH", cmd)
	}
}

func TestGetLaunchCommandDefaultsToBaseCadence(t *testing.T) {
	t.Setenv(SpeedupEnv, "")
	cmd, err := New().GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd[2], "sleep 2") {
		t.Fatalf("default cadence should sleep basePhaseSeconds (2s); got:\n%s", cmd[2])
	}
}

func TestPhaseSleepIgnoresBadSpeedup(t *testing.T) {
	// phaseSleep reads AO_FAKE_SPEEDUP straight from the process environment;
	// t.Setenv restores it automatically after each test, so nothing leaks into
	// other tests under -count/-shuffle (the isolation bug the #2692 re-review
	// flagged, when a package-level getenv seam was left stubbed).
	for _, raw := range []string{"", "0", "-3", "abc"} {
		t.Setenv(SpeedupEnv, raw)
		if got := phaseSleep(); got != basePhaseSeconds {
			t.Fatalf("phaseSleep() with %q = %v, want base %v", raw, got, basePhaseSeconds)
		}
	}
	t.Setenv(SpeedupEnv, "2")
	if got := phaseSleep(); got != basePhaseSeconds/2 {
		t.Fatalf("phaseSleep() with speedup 2 = %v, want %v", got, basePhaseSeconds/2)
	}
}

func TestDeriveActivityStateMapping(t *testing.T) {
	tests := []struct {
		event string
		want  domain.ActivityState
		ok    bool
	}{
		{"session-start", domain.ActivityActive, true},
		{"user-prompt-submit", domain.ActivityActive, true},
		{"pr-push", domain.ActivityActive, true},
		{"agent-needs-input", domain.ActivityWaitingInput, true},
		{"permission-request", domain.ActivityBlocked, true},
		{"stop", domain.ActivityIdle, true},
		{"session-end", domain.ActivityExited, true},
		{"unknown-event", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := DeriveActivityState(tt.event, nil)
		if got != tt.want || ok != tt.ok {
			t.Errorf("DeriveActivityState(%q) = (%q, %v), want (%q, %v)", tt.event, got, ok, tt.want, tt.ok)
		}
	}
}

func TestAuthStatusIsAuthorizedWhenGateSet(t *testing.T) {
	// Opt in explicitly — the fake must only report authorized when the gate
	// env is set (see #2692 review, @whoisasx).
	t.Setenv(HarnessEnv, "1")
	status, err := New().AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus with %s=1 = %q, want authorized", HarnessEnv, status)
	}
}

// TestAuthStatusIsUnauthorizedByDefault pins the production-safety default: on
// a user machine (no AO_FAKE_HARNESS) the fake must NOT be surfaceable as an
// authorized agent, so it never drifts into the default-selectable catalog.
func TestAuthStatusIsUnauthorizedByDefault(t *testing.T) {
	t.Setenv(HarnessEnv, "")
	status, err := New().AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("AuthStatus with %s unset = %q, want unauthorized", HarnessEnv, status)
	}
	for _, off := range []string{"0", "false", "no", "off", "  ", "bogus"} {
		t.Setenv(HarnessEnv, off)
		status, err := New().AuthStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status != ports.AgentAuthStatusUnauthorized {
			t.Fatalf("AuthStatus with %s=%q = %q, want unauthorized", HarnessEnv, off, status)
		}
	}
}

func TestGetPromptDeliveryStrategyIsInCommand(t *testing.T) {
	got, err := New().GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("prompt delivery strategy = %q, want in_command", got)
	}
}

// lifecycleStore is a minimal in-memory implementation of the store the
// lifecycle reducer writes. Only the session read/activity-write methods carry
// the activity path; the PR methods are inert stubs (the fake never opens a PR
// row).
type lifecycleStore struct {
	sessions map[domain.SessionID]domain.SessionRecord
}

func (s *lifecycleStore) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	r, ok := s.sessions[id]
	return r, ok, nil
}

func (s *lifecycleStore) UpdateSession(_ context.Context, rec domain.SessionRecord) error {
	s.sessions[rec.ID] = rec
	return nil
}

func (s *lifecycleStore) UpdateSessionFromActivitySignal(_ context.Context, rec domain.SessionRecord, _ time.Time) (bool, error) {
	s.sessions[rec.ID] = rec
	return true, nil
}

func (s *lifecycleStore) ListPRsBySession(_ context.Context, _ domain.SessionID) ([]domain.PullRequest, error) {
	return nil, nil
}

func (s *lifecycleStore) GetPR(_ context.Context, prURL string) (domain.PullRequest, bool, error) {
	return domain.PullRequest{URL: prURL, AutoInjectCI: true}, true, nil
}

func (s *lifecycleStore) ListPRReviews(_ context.Context, _ string) ([]domain.PullRequestReview, error) {
	return nil, nil
}

func (s *lifecycleStore) ListPRComments(_ context.Context, _ string) ([]domain.PullRequestComment, error) {
	return nil, nil
}

func (s *lifecycleStore) GetProject(_ context.Context, _ string) (domain.ProjectRecord, bool, error) {
	return domain.ProjectRecord{}, false, nil
}

func (s *lifecycleStore) GetPRLastNudgeSignature(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (s *lifecycleStore) UpdatePRLastNudgeSignature(_ context.Context, _ string, _ string) error {
	return nil
}

func (s *lifecycleStore) ListSessions(_ context.Context, _ domain.ProjectID) ([]domain.SessionRecord, error) {
	return nil, nil
}

// TestFullLifecycleSpawnToTermination is the end-to-end integration test the
// #2692 re-review asked for: it drives the real path — spawn (GetLaunchCommand)
// -> hooks fire (the script actually runs, calling a stub `ao` on PATH that
// records each event) -> activity derivation (DeriveActivityState) -> reduction
// (lifecycle.Manager.ApplyActivitySignal, including the sticky-state precedence
// rules) -> agent exit — and asserts the derived activity states in order.
//
// It also asserts the whole sped-up run compresses to well under a second, and
// that the terminal state is a deterministic ActivityExited without terminating
// the still-available runtime, not an incidental idle.
func TestFullLifecycleSpawnToTermination(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no POSIX shell on PATH")
	}

	// A very high speedup so the six phase sleeps are ~1ms each: the run must be
	// near-instant, never the ~12s it takes unspeeded.
	t.Setenv(SpeedupEnv, "2000")

	// Stub `ao` on PATH: the script calls `ao hooks fake <event>`; this shim
	// records <event> ($3) to a log so the test can read the ordered events the
	// hooks actually fired. Its own stdout/stderr are redirected to /dev/null by
	// the script, so it must persist through a file.
	dir := t.TempDir()
	hookLog := filepath.Join(dir, "events.log")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$3\" >> \"$AO_HOOK_LOG\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ao"), []byte(shim), 0o755); err != nil { //nolint:gosec // test shim must be executable
		t.Fatal(err)
	}

	// Spawn: build the launch command exactly as the daemon would.
	cmd, err := New().GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: "ignored"})
	if err != nil {
		t.Fatalf("GetLaunchCommand: %v", err)
	}

	// Hooks fire: run the script with the stub `ao` ahead of the real PATH.
	run := exec.Command(cmd[0], cmd[1:]...) //nolint:gosec // argv is the harness's own launch command
	run.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AO_HOOK_LOG="+hookLog,
		"AO_SESSION_ID=fake-1",
	)
	start := time.Now()
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("timeline script failed: %v\n%s", err, out)
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("sped-up run took %v, want well under a second (speedup not applied?)", elapsed)
	}

	raw, err := os.ReadFile(hookLog) //nolint:gosec // path is under the test's own TempDir
	if err != nil {
		t.Fatalf("read hook log: %v", err)
	}
	events := strings.Fields(strings.TrimSpace(string(raw)))
	wantEvents := []string{
		"session-start", "agent-needs-input", "user-prompt-submit",
		"pr-push", "permission-request", "session-end",
	}
	if strings.Join(events, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("hooks fired = %v, want %v", events, wantEvents)
	}

	// Activity derivation + reduction: seed a spawning session and fold every
	// fired event through the real deriver and lifecycle reducer, capturing the
	// persisted state after each. This exercises sticky-state precedence:
	// waiting_input and blocked must be cleared by the following turn-boundary
	// events, and session-end must mark the agent exited.
	store := &lifecycleStore{sessions: map[domain.SessionID]domain.SessionRecord{
		"fake-1": {
			ID:        "fake-1",
			ProjectID: "proj-1",
			Harness:   domain.HarnessFake,
			Activity:  domain.Activity{State: "", LastActivityAt: time.Now()},
		},
	}}
	mgr := lifecycle.New(store, nil)

	var got []domain.ActivityState
	for _, ev := range events {
		state, ok := DeriveActivityState(ev, nil)
		if !ok {
			t.Fatalf("event %q derived no activity signal", ev)
		}
		sig := ports.ActivitySignal{Valid: true, State: state, Event: ev, Timestamp: time.Now()}
		if err := mgr.ApplyActivitySignal(context.Background(), "fake-1", sig); err != nil {
			t.Fatalf("ApplyActivitySignal(%q): %v", ev, err)
		}
		got = append(got, store.sessions["fake-1"].Activity.State)
	}

	// The persisted states, in order. pr-push derives active and lands while
	// already active, so it does not add a distinct transition — the observable
	// lifecycle is spawning -> active -> waiting_input -> active -> blocked ->
	// exited.
	want := []domain.ActivityState{
		domain.ActivityActive,       // session-start
		domain.ActivityWaitingInput, // agent-needs-input
		domain.ActivityActive,       // user-prompt-submit (clears waiting_input)
		domain.ActivityActive,       // pr-push (already active)
		domain.ActivityBlocked,      // permission-request
		domain.ActivityExited,       // session-end (clears blocked)
	}
	if len(got) != len(want) {
		t.Fatalf("derived %d states %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("state[%d] after %q = %q, want %q (full: %v)", i, events[i], got[i], want[i], got)
		}
	}

	// Agent exit must be durable and distinct from runtime termination.
	final := store.sessions["fake-1"]
	if final.IsTerminated {
		t.Fatalf("session terminated after agent-only session-end; state=%q", final.Activity.State)
	}
	if final.Activity.State != domain.ActivityExited {
		t.Fatalf("terminal state = %q, want %q (not an incidental idle)", final.Activity.State, domain.ActivityExited)
	}
}
