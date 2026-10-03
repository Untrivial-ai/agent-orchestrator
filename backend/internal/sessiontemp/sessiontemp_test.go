package sessiontemp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPrepareCreatesProjectOwnedTempWithOwner(t *testing.T) {
	dataDir := t.TempDir()
	tmpDir, err := Prepare(dataDir, "proj-1", "ses-1")
	if err != nil {
		t.Fatal(err)
	}
	want := TempDirFor(dataDir, "proj-1", "ses-1")
	if tmpDir != want {
		t.Fatalf("tmpDir = %q, want %q", tmpDir, want)
	}
	for _, key := range []string{"TMPDIR", "TEMP", "TMP"} {
		_ = key
	}
	env := map[string]string{"TMPDIR": "/shared/tmp", "TEMP": "/shared/tmp", "TMP": "/shared/tmp", "FOO": "bar"}
	ApplyToEnv(env, tmpDir, false)
	if env["TMPDIR"] != tmpDir || env["TEMP"] != tmpDir || env["TMP"] != tmpDir {
		t.Fatalf("temp env not pinned: %v", env)
	}
	if env["FOO"] != "bar" {
		t.Fatalf("unrelated env clobbered: %v", env)
	}
	raw, err := os.ReadFile(filepath.Join(RunDir(dataDir, "proj-1", "ses-1"), ownerFile))
	if err != nil || !strings.Contains(string(raw), `"ses-1"`) {
		t.Fatalf("owner record missing session: %s err=%v", raw, err)
	}
}

func TestPrepareIsIdempotentForSameSession(t *testing.T) {
	dataDir := t.TempDir()
	first, err := Prepare(dataDir, "proj-1", "ses-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(dataDir, "proj-1", "ses-1")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("reuse = %q, want %q", second, first)
	}
}

func TestPrepareConcurrentFirstLaunchKeepsSingleOwner(t *testing.T) {
	dataDir := t.TempDir()
	const racers = 8
	results := make([]string, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tmp, err := Prepare(dataDir, "proj-1", "ses-race")
			results[i], errs[i] = tmp, err
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent prepare: %v", err)
		}
	}
	for _, got := range results[1:] {
		if got != results[0] {
			t.Fatalf("concurrent tmp dirs diverged: %q vs %q", got, results[0])
		}
	}
}

func TestPrepareProjectlessSessionUsesStandaloneBucket(t *testing.T) {
	dataDir := t.TempDir()
	tmpDir, err := Prepare(dataDir, "", "ses-standalone")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(Root(dataDir), "standalone", "ses-standalone", "tmp")
	if tmpDir != want {
		t.Fatalf("tmpDir = %q, want %q", tmpDir, want)
	}
	env := map[string]string{}
	ApplyToEnv(env, tmpDir, false)
	if env["TMPDIR"] != want {
		t.Fatalf("TMPDIR = %q, want %q", env["TMPDIR"], want)
	}
}

func TestPrepareRefusesUnusableRoot(t *testing.T) {
	if _, err := Prepare("", "proj-1", "ses-1"); !errors.Is(err, ErrTempRootUnusable) {
		t.Fatalf("empty data dir err = %v, want ErrTempRootUnusable", err)
	}
	if _, err := Prepare("relative/path", "proj-1", "ses-1"); !errors.Is(err, ErrTempRootUnusable) {
		t.Fatalf("relative data dir err = %v, want ErrTempRootUnusable", err)
	}
	if _, err := Prepare(t.TempDir(), "bad/id", "ses-1"); !errors.Is(err, ErrTempRootUnusable) {
		t.Fatalf("bad project id err = %v, want ErrTempRootUnusable", err)
	}
	if _, err := Prepare(t.TempDir(), "proj-1", "../escape"); !errors.Is(err, ErrTempRootUnusable) {
		t.Fatalf("bad session id err = %v, want ErrTempRootUnusable", err)
	}
	// A regular file where the project root should be refuses clearly.
	dataDir := t.TempDir()
	blocker := filepath.Join(Root(dataDir), "proj-blocked")
	if err := os.MkdirAll(Root(dataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(dataDir, "proj-blocked", "ses-1"); !errors.Is(err, ErrTempRootUnusable) {
		t.Fatalf("blocked root err = %v, want ErrTempRootUnusable", err)
	}
}

func TestPrepareRefusesMismatchedOwner(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := Prepare(dataDir, "proj-1", "ses-1"); err != nil {
		t.Fatal(err)
	}
	// Corrupt the record by swapping the session identity; the next Prepare
	// for a colliding run dir must refuse instead of taking ownership.
	ownerPath := filepath.Join(RunDir(dataDir, "proj-1", "ses-1"), ownerFile)
	raw, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := strings.Replace(string(raw), `"ses-1"`, `"ses-other"`, 1)
	if err := os.WriteFile(ownerPath, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(dataDir, "proj-1", "ses-1"); !errors.Is(err, ErrTempRootUnusable) {
		t.Fatalf("mismatched owner err = %v, want ErrTempRootUnusable", err)
	}
}

func TestApplyToEnvWindowsIsCaseInsensitive(t *testing.T) {
	env := map[string]string{"tmpdir": "/shared", "Temp": "/shared", "PATH": "/bin"}
	ApplyToEnv(env, `/C/ao/session-temp/p/s/tmp`, true)
	if _, ok := env["tmpdir"]; ok {
		t.Fatalf("lowercase variant survived: %v", env)
	}
	if env["TMPDIR"] == "" || env["TEMP"] == "" || env["TMP"] == "" {
		t.Fatalf("canonical temp vars missing: %v", env)
	}
}

func TestDiscoverIsReportOnly(t *testing.T) {
	dataDir := t.TempDir()
	liveTmp, err := Prepare(dataDir, "proj-1", "ses-live")
	if err != nil {
		t.Fatal(err)
	}
	staleTmp, err := Prepare(dataDir, "proj-1", "ses-stale")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := Discover(dataDir, map[string]bool{"ses-live": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].SessionID != "ses-stale" {
		t.Fatalf("discover = %+v, want exactly ses-stale", stale)
	}
	if stale[0].TempDir != staleTmp {
		t.Fatalf("stale temp = %q, want %q", stale[0].TempDir, staleTmp)
	}
	// Report-only: both folders still exist.
	for _, dir := range []string{liveTmp, staleTmp} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("discovery deleted %q: %v", dir, err)
		}
	}
	if _, err := Discover(filepath.Join(dataDir, "missing"), nil); err != nil {
		t.Fatalf("discover on missing root: %v", err)
	}
}
