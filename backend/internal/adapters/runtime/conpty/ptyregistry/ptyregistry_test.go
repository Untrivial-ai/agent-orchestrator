package ptyregistry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// withFakePidAlive replaces the pidAlive var for the duration of the test.
func withFakePidAlive(t *testing.T, fn func(pid int) bool) {
	t.Helper()
	orig := pidAlive
	pidAlive = fn
	t.Cleanup(func() { pidAlive = orig })
}

func withRegistryRewrite(t *testing.T, fn func(context.Context, []Entry) error) {
	t.Helper()
	orig := rewriteRegistry
	rewriteRegistry = fn
	t.Cleanup(func() { rewriteRegistry = orig })
}

// setupHome points HOME at a temp dir and returns the expected registry path.
func setupHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return filepath.Join(dir, ".ao", "windows-pty-hosts.json")
}

// withRunFilePath sets the instance-scoped registry override for the
// duration of the test and restores the previous value on cleanup, mirroring
// withFakePidAlive above.
func withRunFilePath(t *testing.T, path string) {
	t.Helper()
	orig := overrideDir
	SetRunFilePath(path)
	t.Cleanup(func() { overrideDir = orig })
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func TestRegisterThenList(t *testing.T) {
	setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	e := Entry{SessionID: "s1", PtyHostPID: 1234, PipePath: `\\.\pipe\ao-s1`, RegisteredAt: nowRFC3339()}
	if err := Register(context.Background(), e); err != nil {
		t.Fatal(err)
	}

	got, err := List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "s1" {
		t.Fatalf("expected [s1], got %v", got)
	}
}

func TestRegisterReplaceSameID(t *testing.T) {
	setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	e1 := Entry{SessionID: "s1", PtyHostPID: 111, PipePath: `\\.\pipe\ao-s1-a`, RegisteredAt: nowRFC3339()}
	e2 := Entry{SessionID: "s1", PtyHostPID: 222, PipePath: `\\.\pipe\ao-s1-b`, RegisteredAt: nowRFC3339()}
	if err := Register(context.Background(), e1); err != nil {
		t.Fatal(err)
	}
	if err := Register(context.Background(), e2); err != nil {
		t.Fatal(err)
	}

	got, err := List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].PtyHostPID != 222 {
		t.Fatalf("expected PID 222, got %d", got[0].PtyHostPID)
	}
}

func TestConcurrentRegistersPreserveEveryHost(t *testing.T) {
	setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	const count = 16
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- Register(context.Background(), Entry{
				SessionID:    "session-" + strconv.Itoa(i),
				PtyHostPID:   1000 + i,
				PipePath:     "127.0.0.1:" + strconv.Itoa(50000+i),
				RegisteredAt: nowRFC3339(),
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != count {
		t.Fatalf("registry has %d entries, want %d: %v", len(entries), count, entries)
	}
}

func TestUnregisterRemoves(t *testing.T) {
	setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	e := Entry{SessionID: "s1", PtyHostPID: 1234, PipePath: `\\.\pipe\ao-s1`, RegisteredAt: nowRFC3339()}
	if err := Register(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := Unregister(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	got, err := List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestUnregisterNoOpWhenAbsent(t *testing.T) {
	setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	if err := Unregister(context.Background(), "nonexistent"); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterAndUnregisterHonorCanceledContext(t *testing.T) {
	regPath := setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })
	entry := Entry{SessionID: "s1", PtyHostPID: 1234, PipePath: `\\.\pipe\ao-s1`, RegisteredAt: nowRFC3339()}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Register(cancelled, entry); !errors.Is(err, context.Canceled) {
		t.Fatalf("Register with cancelled context = %v, want context cancellation", err)
	}
	if _, err := os.Stat(regPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled Register created registry file: %v", err)
	}

	if err := Register(context.Background(), entry); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	if err := Unregister(cancelled, entry.SessionID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Unregister with cancelled context = %v, want context cancellation", err)
	}
	entries, err := List(context.Background())
	if err != nil || len(entries) != 1 || entries[0].SessionID != entry.SessionID {
		t.Fatalf("cancelled Unregister changed registry: entries=%v err=%v", entries, err)
	}
}

func TestListPrunesDeadPIDs(t *testing.T) {
	regPath := setupHome(t)

	// PID 1 alive, PID 2 dead.
	alive := map[int]bool{1: true, 2: false}
	withFakePidAlive(t, func(pid int) bool { return alive[pid] })

	e1 := Entry{SessionID: "s1", PtyHostPID: 1, PipePath: `\\.\pipe\ao-s1`, RegisteredAt: nowRFC3339()}
	e2 := Entry{SessionID: "s2", PtyHostPID: 2, PipePath: `\\.\pipe\ao-s2`, RegisteredAt: nowRFC3339()}
	if err := Register(context.Background(), e1); err != nil {
		t.Fatal(err)
	}
	if err := Register(context.Background(), e2); err != nil {
		t.Fatal(err)
	}

	got, err := List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "s1" {
		t.Fatalf("expected [s1], got %v", got)
	}

	// Verify the on-disk file was rewritten with only the live entry.
	data, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	var disk []Entry
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk) != 1 || disk[0].SessionID != "s1" {
		t.Fatalf("disk should have only s1, got %v", disk)
	}
}

func TestEmptyResultDeletesFile(t *testing.T) {
	regPath := setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	e := Entry{SessionID: "s1", PtyHostPID: 1, PipePath: `\\.\pipe\ao-s1`, RegisteredAt: nowRFC3339()}
	if err := Register(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	// Unregister last entry -> file should be deleted.
	if err := Unregister(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(regPath); !os.IsNotExist(err) {
		t.Fatal("expected registry file to be deleted")
	}
}

func TestClearDeletesFile(t *testing.T) {
	regPath := setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	e := Entry{SessionID: "s1", PtyHostPID: 1, PipePath: `\\.\pipe\ao-s1`, RegisteredAt: nowRFC3339()}
	if err := Register(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(regPath); !os.IsNotExist(err) {
		t.Fatal("expected registry file to be deleted after Clear")
	}
}

func TestRegistryMalformedIsUnknown(t *testing.T) {
	setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	// Write malformed JSON directly.
	path, _ := registryFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json {{{"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, complete, err := Scan(context.Background())
	if err == nil || complete {
		t.Fatalf("Scan malformed registry = (%v, %v, %v), want incomplete error", got, complete, err)
	}
	if !errors.Is(err, ErrRegistryMalformed) {
		t.Fatalf("Scan malformed registry error = %v, want ErrRegistryMalformed", err)
	}
}

func TestRegistryMissingFileIsComplete(t *testing.T) {
	setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	got, complete, err := Scan(context.Background())
	if err != nil || !complete || len(got) != 0 {
		t.Fatalf("Scan missing registry = (%v, %v, %v), want empty complete scan", got, complete, err)
	}
}

func TestRegistryUnreadableIsUnknown(t *testing.T) {
	path := setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}

	got, complete, err := Scan(context.Background())
	if err == nil || complete {
		t.Fatalf("Scan unreadable registry = (%v, %v, %v), want incomplete error", got, complete, err)
	}
}

func TestRegistryPruneWritePermissionFailureIsUnknown(t *testing.T) {
	path := setupHome(t)
	withFakePidAlive(t, func(int) bool { return false })
	permissionErr := errors.New("permission denied")
	withRegistryRewrite(t, func(context.Context, []Entry) error { return permissionErr })
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]Entry{{
		SessionID: "dead", PtyHostPID: 99, PipePath: `\\.\pipe\dead`, RegisteredAt: nowRFC3339(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, complete, err := Scan(context.Background())
	if !errors.Is(err, permissionErr) || complete || len(got) != 0 {
		t.Fatalf("Scan prune write failure = (%v, %v, %v), want empty incomplete permission error", got, complete, err)
	}
}

func TestScanCancellationStopsBeforePruneWrite(t *testing.T) {
	path := setupHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	withFakePidAlive(t, func(int) bool {
		cancel()
		return false
	})
	rewriteCalled := false
	withRegistryRewrite(t, func(context.Context, []Entry) error {
		rewriteCalled = true
		return nil
	})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]Entry{{
		SessionID: "dead", PtyHostPID: 99, PipePath: `\\.\pipe\dead`, RegisteredAt: nowRFC3339(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, complete, err := Scan(ctx)

	if !errors.Is(err, context.Canceled) || complete || len(got) != 0 {
		t.Fatalf("Scan cancelled during PID probe = (%v, %v, %v), want empty incomplete context cancellation", got, complete, err)
	}
	if rewriteCalled {
		t.Fatal("cancelled registry scan performed a prune write")
	}
}

func TestAtomicWriteProducesValidJSON(t *testing.T) {
	regPath := setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	e := Entry{SessionID: "s1", PtyHostPID: 99, PipePath: `\\.\pipe\ao-s1`, RegisteredAt: nowRFC3339()}
	if err := Register(context.Background(), e); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("registry file is not valid JSON: %v", err)
	}
	if len(entries) != 1 || entries[0].PtyHostPID != 99 {
		t.Fatalf("unexpected entries: %v", entries)
	}
}

// TestSetRunFilePathScopesRegistryToInstanceDir verifies that pinning the
// registry to a run-file's directory writes there instead of ~/.ao, even
// though HOME still resolves to a different temp dir.
func TestSetRunFilePathScopesRegistryToInstanceDir(t *testing.T) {
	homeRegPath := setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	instanceDir := t.TempDir()
	withRunFilePath(t, filepath.Join(instanceDir, "running.json"))

	e := Entry{SessionID: "s1", PtyHostPID: 1234, PipePath: "127.0.0.1:50000", RegisteredAt: nowRFC3339()}
	if err := Register(context.Background(), e); err != nil {
		t.Fatal(err)
	}

	wantPath := filepath.Join(instanceDir, "windows-pty-hosts.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected registry at instance dir %s: %v", wantPath, err)
	}
	if _, err := os.Stat(homeRegPath); !os.IsNotExist(err) {
		t.Fatalf("expected no registry written under HOME %s", homeRegPath)
	}
}

// TestTwoInstancesWithDifferentRunFilePathsDoNotShareRegistry is the
// regression test for the cross-instance session collision: two daemon
// instances (e.g. a headless dev daemon and the desktop app) with different
// AO_RUN_FILE locations must not clobber each other's same-named session
// even though both resolve to one registry file today.
func TestTwoInstancesWithDifferentRunFilePathsDoNotShareRegistry(t *testing.T) {
	setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	instanceA := t.TempDir()
	instanceB := t.TempDir()

	withRunFilePath(t, filepath.Join(instanceA, "running.json"))
	if err := Register(context.Background(), Entry{SessionID: "demo-website-2", PtyHostPID: 100, PipePath: "127.0.0.1:50001", RegisteredAt: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}

	withRunFilePath(t, filepath.Join(instanceB, "running.json"))
	if err := Register(context.Background(), Entry{SessionID: "demo-website-2", PtyHostPID: 200, PipePath: "127.0.0.1:50002", RegisteredAt: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}

	// Instance A's own registration for the same session id must be
	// untouched by instance B registering a session of the same name.
	withRunFilePath(t, filepath.Join(instanceA, "running.json"))
	got, err := List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PtyHostPID != 100 {
		t.Fatalf("expected instance A's own entry (pid 100) untouched by instance B, got %v", got)
	}
}

// TestSetRunFilePathEmptyClearsOverride verifies the documented contract: an
// empty path clears any override and reverts to the ~/.ao default. This is
// what lets conpty.New(Options{}) -- the zero value used throughout this
// package's own tests -- always start from a clean, deterministic registry
// resolution regardless of what an earlier test configured.
func TestSetRunFilePathEmptyClearsOverride(t *testing.T) {
	regPath := setupHome(t)
	withFakePidAlive(t, func(int) bool { return true })

	withRunFilePath(t, filepath.Join(t.TempDir(), "running.json"))
	withRunFilePath(t, "")

	if err := Register(context.Background(), Entry{SessionID: "s1", PtyHostPID: 1, PipePath: "127.0.0.1:50000", RegisteredAt: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(regPath); err != nil {
		t.Fatalf("expected default HOME-based registry at %s: %v", regPath, err)
	}
}
