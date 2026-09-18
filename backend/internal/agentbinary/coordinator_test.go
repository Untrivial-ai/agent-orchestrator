package agentbinary

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type probeFunc func(context.Context, []string) (ports.AgentShellSnapshot, error)

func (f probeFunc) ProbeAgentShell(ctx context.Context, names []string) (ports.AgentShellSnapshot, error) {
	return f(ctx, names)
}
func executable(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		name += ".exe"
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}
func missing(context.Context) (string, error) { return "", ports.ErrAgentBinaryNotFound }
func specs() map[domain.AgentHarness]ports.AgentBinarySpec {
	return map[domain.AgentHarness]ports.AgentBinarySpec{"codex": {Names: []string{"codex"}, Lookup: missing, Presence: missing}, "claude": {Names: []string{"claude"}, Lookup: missing, Presence: missing}}
}
func launch(t *testing.T, c *Coordinator, h domain.AgentHarness) (ports.AgentBinaryResolution, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return c.Resolve(ctx, h, ports.BinaryResolveLaunch)
}
func TestLocalSuccessSkipsShellAndPresenceSkipsIdentity(t *testing.T) {
	p := executable(t, t.TempDir(), "codex")
	var probes, identity atomic.Int32
	s := specs()
	s["codex"] = ports.AgentBinarySpec{Names: []string{"codex"}, Presence: func(context.Context) (string, error) { return p, nil }, Lookup: func(context.Context) (string, error) { identity.Add(1); return p, nil }, Normalize: func(_ context.Context, p string, purpose ports.BinaryResolvePurpose) (string, error) {
		if purpose == ports.BinaryResolveLaunch {
			identity.Add(1)
		}
		return p, nil
	}}
	c := New(Config{Specs: s, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		probes.Add(1)
		return ports.AgentShellSnapshot{}, nil
	})})
	defer c.Close()
	for range 50 {
		r, err := c.Resolve(context.Background(), "codex", ports.BinaryResolvePresence)
		if err != nil || r.Executable != p {
			t.Fatalf("presence: %+v %v", r, err)
		}
	}
	if identity.Load() != 0 || probes.Load() != 0 {
		t.Fatalf("identity=%d probes=%d", identity.Load(), probes.Load())
	}
	if _, err := launch(t, c, "codex"); err != nil {
		t.Fatal(err)
	}
	if identity.Load() == 0 {
		t.Fatal("launch skipped identity")
	}
	t.Logf("50 local presence checks: probes=%d identity=%d", probes.Load(), identity.Load())
}
func TestConcurrentPresenceAndCanceledLaunchShareBatch(t *testing.T) {
	p := executable(t, t.TempDir(), "codex")
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	c := New(Config{Specs: specs(), Probe: probeFunc(func(ctx context.Context, names []string) (ports.AgentShellSnapshot, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		if len(names) != 2 {
			t.Errorf("names=%v", names)
		}
		select {
		case <-release:
			return ports.AgentShellSnapshot{Paths: map[string]string{"codex": p}}, nil
		case <-ctx.Done():
			return ports.AgentShellSnapshot{}, ctx.Err()
		}
	})})
	defer c.Close()
	start := time.Now()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Resolve(context.Background(), "codex", ports.BinaryResolvePresence)
			if !errors.Is(err, ports.ErrAgentBinaryChecking) {
				t.Errorf("presence %v", err)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Resolve(ctx, "codex", ports.BinaryResolveLaunch); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	r, err := launch(t, c, "codex")
	if err != nil || r.Executable != p {
		t.Fatalf("launch %+v %v", r, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("probes=%d", calls.Load())
	}
	t.Logf("50 concurrent presence checks: probes=%d latency=%s", calls.Load(), elapsed)
}
func TestSnapshotPollingInstallAndExpiry(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	c := New(Config{Specs: specs(), Now: func() time.Time { return now }, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		calls.Add(1)
		return ports.AgentShellSnapshot{Path: dir}, nil
	})})
	defer c.Close()
	if _, err := launch(t, c, "codex"); !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatal(err)
	}
	for range 3 {
		now = now.Add(time.Minute)
		if _, err := launch(t, c, "codex"); !errors.Is(err, ports.ErrAgentBinaryNotFound) {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("60-second polling probes=%d", calls.Load())
	}
	p := executable(t, dir, "codex")
	now = now.Add(time.Minute)
	if r, err := launch(t, c, "codex"); err != nil || r.Executable != p {
		t.Fatalf("install %+v %v", r, err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := launch(t, c, "codex"); !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expired snapshot probes=%d", calls.Load())
	}
}
func TestFailureBackoffAndLocalRecovery(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	probeErr := errors.New("probe timed out")
	local := ""
	s := specs()
	sp := s["codex"]
	sp.Lookup = func(context.Context) (string, error) {
		if local != "" {
			return local, nil
		}
		return missing(context.Background())
	}
	s["codex"] = sp
	c := New(Config{Specs: s, Now: func() time.Time { return now }, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		calls.Add(1)
		return ports.AgentShellSnapshot{}, probeErr
	})})
	defer c.Close()
	for _, step := range []time.Duration{0, 29 * time.Second, time.Second, 119 * time.Second, time.Second, 299 * time.Second, time.Second} {
		now = now.Add(step)
		_, err := launch(t, c, "codex")
		if !errors.Is(err, probeErr) || errors.Is(err, ports.ErrAgentBinaryNotFound) {
			t.Fatalf("failure classification %v", err)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("backoff probes=%d", calls.Load())
	}
	local = executable(t, t.TempDir(), "codex")
	if _, err := launch(t, c, "codex"); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidationWaitsForStaleBatchThenRetries(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls, active, maxActive atomic.Int32
	p := executable(t, t.TempDir(), "codex")
	c := New(Config{Specs: specs(), Probe: probeFunc(func(ctx context.Context, _ []string) (ports.AgentShellSnapshot, error) {
		n := active.Add(1)
		defer active.Add(-1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ports.AgentShellSnapshot{}, ctx.Err()
			}
			return ports.AgentShellSnapshot{}, nil
		}
		return ports.AgentShellSnapshot{Paths: map[string]string{"codex": p}}, nil
	})})
	defer c.Close()
	_, _ = c.Resolve(context.Background(), "codex", ports.BinaryResolvePresence)
	<-started
	c.Invalidate("codex")
	close(release)
	if r, err := launch(t, c, "codex"); err != nil || r.Executable != p {
		t.Fatalf("after invalidate %+v %v", r, err)
	}
	if calls.Load() != 2 || maxActive.Load() != 1 {
		t.Fatalf("calls=%d maxActive=%d", calls.Load(), maxActive.Load())
	}
}
func TestCloseCancelsProbeAndDoesNotMutatePATH(t *testing.T) {
	path := os.Getenv("PATH")
	started := make(chan struct{})
	c := New(Config{Specs: specs(), Probe: probeFunc(func(ctx context.Context, _ []string) (ports.AgentShellSnapshot, error) {
		close(started)
		<-ctx.Done()
		return ports.AgentShellSnapshot{}, ctx.Err()
	})})
	_, _ = c.Resolve(context.Background(), "codex", ports.BinaryResolvePresence)
	<-started
	start := time.Now()
	c.Close()
	if time.Since(start) > time.Second {
		t.Fatal("slow close")
	}
	if os.Getenv("PATH") != path {
		t.Fatal("daemon PATH changed")
	}
}

func TestPresenceNormalizesNativeWrapperWithoutLaunch(t *testing.T) {
	dir := t.TempDir()
	shim := executable(t, dir, "shim")
	native := executable(t, dir, "native")
	var launches atomic.Int32
	s := specs()
	s["codex"] = ports.AgentBinarySpec{Names: []string{"codex"}, Presence: func(context.Context) (string, error) { return shim, nil }, Lookup: func(context.Context) (string, error) { launches.Add(1); return native, nil }, Normalize: func(_ context.Context, _ string, purpose ports.BinaryResolvePurpose) (string, error) {
		if purpose == ports.BinaryResolveLaunch {
			launches.Add(1)
		}
		return native, nil
	}}
	c := New(Config{Specs: s})
	defer c.Close()
	r, err := c.Resolve(context.Background(), "codex", ports.BinaryResolvePresence)
	if err != nil || r.Executable != native {
		t.Fatalf("native presence %+v %v", r, err)
	}
	if launches.Load() != 0 {
		t.Fatal("presence ran launch")
	}
}
func TestUnknownNormalizationDoesNotBecomeNotFound(t *testing.T) {
	p := executable(t, t.TempDir(), "codex")
	s := specs()
	sp := s["codex"]
	sp.Normalize = func(context.Context, string, ports.BinaryResolvePurpose) (string, error) {
		return "", ports.ErrAgentBinaryIdentityUnknown
	}
	s["codex"] = sp
	c := New(Config{Specs: s, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		return ports.AgentShellSnapshot{Paths: map[string]string{"codex": p}}, nil
	})})
	defer c.Close()
	_, err := launch(t, c, "codex")
	if !errors.Is(err, ports.ErrAgentBinaryIdentityUnknown) || errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("identity classified as absence: %v", err)
	}
}
func TestCachedSymlinkAndExecutableIdentityAreRevalidated(t *testing.T) {
	dir := t.TempDir()
	a := executable(t, dir, "a")
	b := executable(t, dir, "b")
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	link := filepath.Join(dir, name)
	if err := os.Symlink(a, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink privileges unavailable")
		}
		t.Fatal(err)
	}
	var identities atomic.Int32
	s := specs()
	sp := s["codex"]
	sp.Normalize = func(_ context.Context, p string, _ ports.BinaryResolvePurpose) (string, error) {
		identities.Add(1)
		return p, nil
	}
	s["codex"] = sp
	c := New(Config{Specs: s, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		return ports.AgentShellSnapshot{Paths: map[string]string{"codex": link}}, nil
	})})
	defer c.Close()
	for range 3 {
		if _, err := launch(t, c, "codex"); err != nil {
			t.Fatal(err)
		}
	}
	if identities.Load() != 1 {
		t.Fatalf("warm identity count %d", identities.Load())
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	if _, err := launch(t, c, "codex"); err != nil {
		t.Fatal(err)
	}
	if identities.Load() != 2 {
		t.Fatal("symlink target not revalidated")
	}
	if err := os.WriteFile(b, []byte("#!/bin/sh\n# changed\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := launch(t, c, "codex"); err != nil {
		t.Fatal(err)
	}
	if identities.Load() != 3 {
		t.Fatal("file size not revalidated")
	}
}
func TestLocalPermissionFailureRemainsUnknownAfterSuccessfulEmptyProbe(t *testing.T) {
	s := specs()
	sp := s["codex"]
	sp.Lookup = func(context.Context) (string, error) { return "", os.ErrPermission }
	s["codex"] = sp
	c := New(Config{Specs: s, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		return ports.AgentShellSnapshot{}, nil
	})})
	defer c.Close()
	_, err := launch(t, c, "codex")
	if !errors.Is(err, os.ErrPermission) || errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("permission classified as absence: %v", err)
	}
}

func TestLaunchRequestCancellationDoesNotCancelSharedProbe(t *testing.T) {
	p := executable(t, t.TempDir(), "codex")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c := New(Config{Specs: specs(), Probe: probeFunc(func(ctx context.Context, _ []string) (ports.AgentShellSnapshot, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return ports.AgentShellSnapshot{Paths: map[string]string{"codex": p}}, nil
		case <-ctx.Done():
			return ports.AgentShellSnapshot{}, ctx.Err()
		}
	})})
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := c.Resolve(ctx, "codex", ports.BinaryResolveLaunch); result <- err }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", err)
	}
	close(release)
	r, err := launch(t, c, "codex")
	if err != nil || r.Executable != p {
		t.Fatalf("shared probe canceled: %+v %v", r, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("probes=%d", calls.Load())
	}
}
func TestBackgroundBatchDoesNotRunIdentityNormalization(t *testing.T) {
	p := executable(t, t.TempDir(), "codex")
	completed := make(chan struct{}, 1)
	var identity atomic.Int32
	s := specs()
	sp := s["codex"]
	sp.Normalize = func(_ context.Context, p string, purpose ports.BinaryResolvePurpose) (string, error) {
		if purpose == ports.BinaryResolveLaunch {
			identity.Add(1)
		}
		return p, nil
	}
	s["codex"] = sp
	c := New(Config{Specs: s, OnChanged: func([]domain.AgentHarness) { completed <- struct{}{} }, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		return ports.AgentShellSnapshot{Paths: map[string]string{"codex": p}}, nil
	})})
	defer c.Close()
	_, _ = c.Resolve(context.Background(), "codex", ports.BinaryResolvePresence)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("missing completion")
	}
	if identity.Load() != 0 {
		t.Fatal("background identity probe")
	}
	r, err := c.Resolve(context.Background(), "codex", ports.BinaryResolvePresence)
	if err != nil || r.Executable != p {
		t.Fatalf("presence %+v %v", r, err)
	}
	if identity.Load() != 0 {
		t.Fatal("presence identity probe")
	}
}
func TestInvalidCandidatesAreConfirmedMissingOnlyAfterSuccessfulProbe(t *testing.T) {
	dir := t.TempDir()
	nonexec := filepath.Join(dir, "nonexec")
	if err := os.WriteFile(nonexec, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative/codex", dir, nonexec, filepath.Join(dir, "missing"), "//server/codex", "\\\\wsl$\\Ubuntu\\bin\\codex"} {
		t.Run(path, func(t *testing.T) {
			c := New(Config{Specs: specs(), Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
				return ports.AgentShellSnapshot{Paths: map[string]string{"codex": path}}, nil
			})})
			defer c.Close()
			if _, err := launch(t, c, "codex"); !errors.Is(err, ports.ErrAgentBinaryNotFound) {
				t.Fatalf("invalid path accepted: %v", err)
			}
		})
	}
}

func TestConfirmedMissSkipsLocalScanFor60SecondsAndInvalidationBypasses(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var scans, probes atomic.Int32
	local := ""
	s := specs()
	sp := s["codex"]
	sp.Lookup = func(context.Context) (string, error) {
		scans.Add(1)
		if local != "" {
			return local, nil
		}
		return "", ports.ErrAgentBinaryNotFound
	}
	s["codex"] = sp
	c := New(Config{Specs: s, Now: func() time.Time { return now }, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		probes.Add(1)
		return ports.AgentShellSnapshot{}, nil
	})})
	defer c.Close()
	if _, err := launch(t, c, "codex"); !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatal(err)
	}
	initial := scans.Load()
	for range 10 {
		now = now.Add(5 * time.Second)
		if _, err := launch(t, c, "codex"); !errors.Is(err, ports.ErrAgentBinaryNotFound) {
			t.Fatal(err)
		}
	}
	if scans.Load() != initial {
		t.Fatalf("confirmed miss repeated local scans: %d -> %d", initial, scans.Load())
	}
	now = now.Add(10 * time.Second)
	if _, err := launch(t, c, "codex"); !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatal(err)
	}
	if scans.Load() != initial+1 {
		t.Fatal("local miss did not expire at 60 seconds")
	}
	local = executable(t, t.TempDir(), "codex")
	c.Invalidate("codex")
	if r, err := launch(t, c, "codex"); err != nil || r.Executable != local {
		t.Fatalf("invalidation did not retry locally: %+v %v", r, err)
	}
	if probes.Load() != 1 {
		t.Fatalf("local recovery shell probes=%d", probes.Load())
	}
}

func TestChangedSnapshotEvictsOldShellSelection(t *testing.T) {
	dir := t.TempDir()
	old := executable(t, dir, "old")
	replacement := executable(t, dir, "new")
	var calls atomic.Int32
	c := New(Config{Specs: specs(), Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		p := old
		if calls.Add(1) > 1 {
			p = replacement
		}
		return ports.AgentShellSnapshot{Paths: map[string]string{"codex": p}}, nil
	})})
	defer c.Close()
	if r, err := launch(t, c, "codex"); err != nil || r.Executable != old {
		t.Fatalf("initial %+v %v", r, err)
	}
	c.Invalidate("claude")
	if _, err := launch(t, c, "claude"); !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatal(err)
	}
	if r, err := launch(t, c, "codex"); err != nil || r.Executable != replacement {
		t.Fatalf("stale shell selection %+v %v", r, err)
	}
}

func TestLocalUnknownDoesNotScheduleShell(t *testing.T) {
	for _, cause := range []error{os.ErrPermission, context.DeadlineExceeded, ports.ErrAgentBinaryIdentityUnknown} {
		t.Run(cause.Error(), func(t *testing.T) {
			var calls atomic.Int32
			s := specs()
			sp := s["codex"]
			sp.Lookup = func(context.Context) (string, error) { return "", cause }
			s["codex"] = sp
			c := New(Config{Specs: s, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
				calls.Add(1)
				return ports.AgentShellSnapshot{}, nil
			})})
			defer c.Close()
			_, err := launch(t, c, "codex")
			if !errors.Is(err, cause) || errors.Is(err, ports.ErrAgentBinaryNotFound) {
				t.Fatalf("unknown lost: %v", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("unknown scheduled %d probes", calls.Load())
			}
		})
	}
}

func TestSlowPresenceCannotReplaceConfirmedLaunchIdentity(t *testing.T) {
	p := executable(t, t.TempDir(), "codex")
	started := make(chan struct{})
	release := make(chan struct{})
	var launches atomic.Int32
	s := specs()
	s["codex"] = ports.AgentBinarySpec{Names: []string{"codex"}, Lookup: func(context.Context) (string, error) { launches.Add(1); return p, nil }, Presence: func(context.Context) (string, error) {
		close(started)
		<-release
		return p, ports.ErrAgentBinaryIdentityUnknown
	}}
	c := New(Config{Specs: s})
	defer c.Close()
	done := make(chan error, 1)
	go func() { _, err := c.Resolve(context.Background(), "codex", ports.BinaryResolvePresence); done <- err }()
	<-started
	if _, err := launch(t, c, "codex"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("presence discarded confirmed identity: %v", err)
	}
	if _, err := launch(t, c, "codex"); err != nil {
		t.Fatal(err)
	}
	if launches.Load() != 1 {
		t.Fatalf("launch repeated identity %d times", launches.Load())
	}
}
func TestInvalidationDuringRawLookupRetriesBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	old := executable(t, dir, "old")
	replacement := executable(t, dir, "new")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	s := specs()
	sp := s["codex"]
	sp.Lookup = func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return old, nil
		}
		return replacement, nil
	}
	s["codex"] = sp
	c := New(Config{Specs: s})
	defer c.Close()
	done := make(chan ports.AgentBinaryResolution, 1)
	go func() { r, _ := c.Resolve(context.Background(), "codex", ports.BinaryResolveLaunch); done <- r }()
	<-started
	c.Invalidate("codex")
	close(release)
	if r := <-done; r.Executable != replacement {
		t.Fatalf("returned stale raw result: %+v", r)
	}
}
func TestInvalidationDuringShellCandidateMissRetries(t *testing.T) {
	p := executable(t, t.TempDir(), "codex")
	started := make(chan struct{})
	release := make(chan struct{})
	var normalized atomic.Int32
	s := specs()
	sp := s["codex"]
	sp.Normalize = func(_ context.Context, p string, _ ports.BinaryResolvePurpose) (string, error) {
		if normalized.Add(1) == 1 {
			close(started)
			<-release
			return "", ports.ErrAgentBinaryNotFound
		}
		return p, nil
	}
	s["codex"] = sp
	c := New(Config{Specs: s, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		return ports.AgentShellSnapshot{Paths: map[string]string{"codex": p}}, nil
	})})
	defer c.Close()
	done := make(chan error, 1)
	go func() { _, err := c.Resolve(context.Background(), "codex", ports.BinaryResolveLaunch); done <- err }()
	<-started
	c.Invalidate("codex")
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("returned stale shell miss: %v", err)
	}
}

func TestReplacementDuringIdentityProbeMustBeValidatedAgain(t *testing.T) {
	dir := t.TempDir()
	path := executable(t, dir, "codex")
	calls := 0
	s := specs()
	spec := s["codex"]
	spec.Normalize = func(_ context.Context, p string, _ ports.BinaryResolvePurpose) (string, error) {
		calls++
		if calls == 1 {
			replacement := executable(t, dir, "replacement")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
		}
		return p, nil
	}
	s["codex"] = spec
	c := New(Config{Specs: s, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		return ports.AgentShellSnapshot{Paths: map[string]string{"codex": path}}, nil
	})})
	defer c.Close()
	if _, err := launch(t, c, "codex"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("identity probes=%d, replacement reused old identity", calls)
	}
}
