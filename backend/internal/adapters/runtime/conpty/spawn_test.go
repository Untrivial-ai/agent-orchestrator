package conpty

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty/ptyregistry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestStripEnvAssignments(t *testing.T) {
	tests := []struct {
		name            string
		argv            []string
		wantAssignments []string
		wantRest        []string
	}{
		{
			name:            "no env prefix returns argv unchanged",
			argv:            []string{"opencode", "--agent", "ao-x"},
			wantAssignments: nil,
			wantRest:        []string{"opencode", "--agent", "ao-x"},
		},
		{
			name:            "env prefix is split from the real command",
			argv:            []string{"env", "OPENCODE_CONFIG=C:/cfg.json", "opencode", "--agent", "ao-x"},
			wantAssignments: []string{"OPENCODE_CONFIG=C:/cfg.json"},
			wantRest:        []string{"opencode", "--agent", "ao-x"},
		},
		{
			name:            "env with no command left is untouched",
			argv:            []string{"env", "A=1", "B=2"},
			wantAssignments: nil,
			wantRest:        []string{"env", "A=1", "B=2"},
		},
		{
			name:            "a binary merely starting with env is not treated as a prefix",
			argv:            []string{"envoy", "--config", "x"},
			wantAssignments: nil,
			wantRest:        []string{"envoy", "--config", "x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAssignments, gotRest := stripEnvAssignments(tt.argv)
			if !reflect.DeepEqual(gotAssignments, tt.wantAssignments) {
				t.Errorf("assignments = %#v, want %#v", gotAssignments, tt.wantAssignments)
			}
			if !reflect.DeepEqual(gotRest, tt.wantRest) {
				t.Errorf("rest = %#v, want %#v", gotRest, tt.wantRest)
			}
		})
	}
}

func TestStartedHostKillFailureRetainsPartialCreateEvidence(t *testing.T) {
	isolateRegistry(t)
	startupErr := errors.New("pty-host READY response unavailable")
	killErr := errors.New("kill access denied")
	pid, spawnErr := cleanupStartedHostFailure(livePID(), startupErr, func() error { return killErr })
	if pid != livePID() || !errors.Is(spawnErr, startupErr) || !errors.Is(spawnErr, killErr) {
		t.Fatalf("started-host cleanup = (%d, %v), want retained pid and joined startup/kill errors", pid, spawnErr)
	}

	runtime := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		return "", pid, spawnErr
	}})
	_, err := runtime.Create(context.Background(), ports.RuntimeConfig{
		SessionID: "sess-kill-failed", WorkspacePath: t.TempDir(), Argv: []string{"codex"},
		Env: map[string]string{runtimeLaunchIDEnv: "launch-kill-failed"},
	})
	var effect ports.RuntimeEffectError
	if !errors.As(err, &effect) {
		t.Fatalf("Create error %T does not expose RuntimeEffectError: %v", err, err)
	}
	if effect.PossibleHandle().ID != "sess-kill-failed" || effect.EffectOutcome() != ports.RuntimeEffectPossible || effect.CleanupOutcome() != ports.RuntimeCleanupFailed {
		t.Fatalf("Create effect evidence = handle %+v effect %q cleanup %q", effect.PossibleHandle(), effect.EffectOutcome(), effect.CleanupOutcome())
	}
	if !strings.Contains(err.Error(), "kill access denied") {
		t.Fatalf("Create error lost cleanup outcome: %v", err)
	}
	runtime.pidIsAlive = func(int) bool { return true }
	runtime.destroyWait = 0
	currentKillCalls := 0
	runtime.processFinder = func(int) (processKiller, error) {
		return processKillerFunc(func() error {
			currentKillCalls++
			return errors.New("current-owner force kill denied")
		}), nil
	}
	if destroyErr := runtime.Destroy(context.Background(), effect.PossibleHandle()); destroyErr == nil || !strings.Contains(destroyErr.Error(), "current-owner force kill denied") {
		t.Fatalf("Destroy current-owned partial create = %v, want controlled cleanup failure", destroyErr)
	}
	if currentKillCalls != 1 {
		t.Fatalf("current-owned partial create force-kill calls = %d, want 1", currentKillCalls)
	}

	// The possible handle must remain fenceable even after a daemon restart.
	// A live PID without a READY address is unknown, never exact absence.
	recovered := New(Options{})
	recovered.pidIsAlive = func(int) bool { return true }
	ref := ports.FencedRuntimeRef{
		Handle: effect.PossibleHandle(), SessionID: "sess-kill-failed", Generation: "launch-kill-failed",
	}
	probe := recovered.ProbeFencedRuntime(context.Background(), ref)
	if probe.Liveness != ports.FencedUnknown || probe.Reason != ports.FencedReasonProbeFailed {
		t.Fatalf("ProbeFencedRuntime partial create = %+v, want unknown/probe_failed", probe)
	}

	recoveredKillCalls := 0
	recovered.killHost = func(string) error {
		recoveredKillCalls++
		return nil
	}
	recovered.processFinder = func(int) (processKiller, error) {
		recoveredKillCalls++
		return processKillerFunc(func() error {
			recoveredKillCalls++
			return nil
		}), nil
	}
	if destroyErr := recovered.Destroy(context.Background(), effect.PossibleHandle()); destroyErr == nil || !errors.Is(destroyErr, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Destroy recovered partial create = %v, want retained inconclusive error", destroyErr)
	}
	if recoveredKillCalls != 0 {
		t.Fatalf("Destroy recovered partial create made %d unverified kill calls, want 0", recoveredKillCalls)
	}
	probe = recovered.ProbeFencedRuntime(context.Background(), ref)
	if probe.Liveness != ports.FencedUnknown {
		t.Fatalf("ProbeFencedRuntime after failed cleanup = %+v, want unknown", probe)
	}
	entries, complete, scanErr := ptyregistry.Scan(context.Background())
	if scanErr != nil || !complete || len(entries) != 1 || entries[0].SessionID != "sess-kill-failed" {
		t.Fatalf("registry after recovered cleanup = entries %+v complete=%v err=%v, want retained evidence", entries, complete, scanErr)
	}
}

func TestCreateReservationFailureDoesNotSpawnOrClaimRuntimeEffect(t *testing.T) {
	isolateRegistry(t)
	spawnCalls := 0
	runtime := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		spawnCalls++
		return "127.0.0.1:1", livePID(), nil
	}})
	reservationErr := errors.New("reservation write denied")
	runtime.registerHost = func(context.Context, ptyregistry.Entry) error { return reservationErr }

	_, err := runtime.Create(context.Background(), ports.RuntimeConfig{
		SessionID: "sess-reservation-failed", WorkspacePath: t.TempDir(), Argv: []string{"codex"},
		Env: map[string]string{runtimeLaunchIDEnv: "launch-reservation-failed"},
	})
	var effect ports.RuntimeEffectError
	if !errors.As(err, &effect) || !errors.Is(err, reservationErr) {
		t.Fatalf("Create reservation failure = %v, want typed reservation error", err)
	}
	if effect.EffectOutcome() != ports.RuntimeEffectNone || effect.PossibleHandle().ID != "" || spawnCalls != 0 {
		t.Fatalf("reservation failure effect=%q handle=%+v spawnCalls=%d, want none/empty/0", effect.EffectOutcome(), effect.PossibleHandle(), spawnCalls)
	}
}

func TestDefinitiveSpawnFailureRetainsCleanupAuthorityUntilUnregisterSucceeds(t *testing.T) {
	isolateRegistry(t)
	spawnErr := errors.New("pty-host failed before starting")
	runtime := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		return "", 0, spawnErr
	}})
	unregisterErr := errors.New("reservation cleanup denied")
	unregisterCalls := 0
	runtime.unregisterHost = func(context.Context, string) error {
		unregisterCalls++
		return unregisterErr
	}

	_, err := runtime.Create(context.Background(), ports.RuntimeConfig{
		SessionID: "sess-cleanup-retry", WorkspacePath: t.TempDir(), Argv: []string{"codex"},
		Env: map[string]string{runtimeLaunchIDEnv: "launch-cleanup-retry"},
	})
	var effect ports.RuntimeEffectError
	if !errors.As(err, &effect) || !errors.Is(err, spawnErr) || !errors.Is(err, unregisterErr) {
		t.Fatalf("Create definitive failure = %v, want joined spawn and unregister errors", err)
	}
	if effect.EffectOutcome() != ports.RuntimeEffectPossible || effect.CleanupOutcome() != ports.RuntimeCleanupFailed || effect.PossibleHandle().ID != "sess-cleanup-retry" {
		t.Fatalf("definitive failure effect=%q cleanup=%q handle=%+v, want possible/failed/retained handle", effect.EffectOutcome(), effect.CleanupOutcome(), effect.PossibleHandle())
	}
	runtime.mu.Lock()
	retained := runtime.sessions["sess-cleanup-retry"]
	runtime.mu.Unlock()
	if retained == nil || !retained.currentOwner || retained.pid != 0 || retained.addr != unresolvedHostAddress {
		t.Fatalf("retained cleanup authority = %+v, want current-owner PID-zero reservation", retained)
	}

	runtime.unregisterHost = func(context.Context, string) error {
		unregisterCalls++
		return nil
	}
	runtime.pidIsAlive = func(int) bool {
		t.Fatal("PID-zero reservation cleanup must not probe OS process liveness")
		return false
	}
	runtime.processFinder = func(int) (processKiller, error) {
		t.Fatal("PID-zero reservation cleanup must not resolve or kill an OS process")
		return nil, errors.New("unreachable")
	}
	if err := runtime.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-cleanup-retry"}); err != nil {
		t.Fatalf("Destroy retrying reservation cleanup: %v", err)
	}
	if unregisterCalls != 2 {
		t.Fatalf("unregister calls = %d, want failed Create cleanup plus successful Destroy retry", unregisterCalls)
	}
	runtime.mu.Lock()
	_, exists := runtime.sessions["sess-cleanup-retry"]
	runtime.mu.Unlock()
	if exists {
		t.Fatal("reservation remained in memory after durable cleanup succeeded")
	}
}

func TestPostStartRegistryUpdateFailureLeavesDurableUnknownReservation(t *testing.T) {
	isolateRegistry(t)
	startupErr := errors.New("READY response lost")
	runtime := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		return "", livePID(), startupErr
	}})
	registerCalls := 0
	updateErr := errors.New("registry update denied")
	runtime.registerHost = func(ctx context.Context, entry ptyregistry.Entry) error {
		registerCalls++
		if registerCalls == 1 {
			return ptyregistry.Register(ctx, entry)
		}
		return updateErr
	}

	_, err := runtime.Create(context.Background(), ports.RuntimeConfig{
		SessionID: "sess-update-failed", WorkspacePath: t.TempDir(), Argv: []string{"codex"},
		Env: map[string]string{runtimeLaunchIDEnv: "launch-update-failed"},
	})
	var effect ports.RuntimeEffectError
	if !errors.As(err, &effect) || !errors.Is(err, startupErr) || !errors.Is(err, updateErr) || effect.EffectOutcome() != ports.RuntimeEffectPossible {
		t.Fatalf("Create update failure = %v effect=%v, want joined possible effect", err, effect.EffectOutcome())
	}
	fresh := New(Options{})
	probe := fresh.ProbeFencedRuntime(context.Background(), ports.FencedRuntimeRef{
		Handle: effect.PossibleHandle(), SessionID: "sess-update-failed", Generation: "launch-update-failed",
	})
	if probe.Liveness != ports.FencedUnknown {
		t.Fatalf("fresh ProbeFencedRuntime after update failure = %+v, want unknown", probe)
	}
}

func TestPendingPIDZeroReservationRemainsUnknownAcrossScanAndRestart(t *testing.T) {
	isolateRegistry(t)
	entry := ptyregistry.Entry{
		SessionID: "sess-pending", PtyHostPID: 0, PipePath: unresolvedHostAddress,
		LaunchID: "launch-pending", RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := ptyregistry.Register(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	entries, complete, err := ptyregistry.Scan(context.Background())
	if err != nil || !complete || len(entries) != 1 || !reflect.DeepEqual(entries[0], entry) {
		t.Fatalf("pending registry scan = entries %+v complete=%v err=%v, want retained reservation", entries, complete, err)
	}
	fresh := New(Options{})
	probe := fresh.ProbeFencedRuntime(context.Background(), ports.FencedRuntimeRef{
		Handle: ports.RuntimeHandle{ID: entry.SessionID}, SessionID: domain.SessionID(entry.SessionID), Generation: entry.LaunchID,
	})
	if probe.Liveness != ports.FencedUnknown {
		t.Fatalf("fresh ProbeFencedRuntime pending reservation = %+v, want unknown", probe)
	}
}

func TestInteractiveTerminalEnvDropsAmbientNoColorAndAdvertisesTrueColor(t *testing.T) {
	env := interactiveTerminalEnv(
		[]string{"PATH=/usr/bin", "TERM=dumb", "COLORTERM=ansi", "NO_COLOR=1"},
		map[string]string{"AO_SESSION_ID": "sess-1"},
		nil,
	)

	for _, want := range []string{
		"PATH=/usr/bin",
		"AO_SESSION_ID=sess-1",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("env missing %q: %#v", want, env)
		}
	}
	for _, got := range env {
		if got == "NO_COLOR=1" {
			t.Fatalf("ambient NO_COLOR leaked into interactive terminal env: %#v", env)
		}
	}
}

func TestInteractiveTerminalEnvPreservesExplicitNoColor(t *testing.T) {
	for _, tt := range []struct {
		name        string
		configured  map[string]string
		assignments []string
	}{
		{name: "runtime config", configured: map[string]string{"NO_COLOR": "1"}},
		{name: "argv env assignment", assignments: []string{"NO_COLOR=1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := interactiveTerminalEnv(
				[]string{"NO_COLOR=ambient"},
				tt.configured,
				tt.assignments,
			)
			if !slices.Contains(env, "NO_COLOR=1") {
				t.Fatalf("explicit NO_COLOR not preserved: %#v", env)
			}
		})
	}
}
