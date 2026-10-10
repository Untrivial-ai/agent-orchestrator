package local

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/processalive"
)

func setupCancellationTools(t *testing.T, root, stage string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("local desktop preparation uses POSIX process groups")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	ready, release := filepath.Join(root, "ready"), filepath.Join(root, "release")
	script := "#!" + python + "\n" + `import json, os, subprocess, sys, time
from pathlib import Path
ready = Path(os.environ["SETUP_TEST_READY"])
release = Path(os.environ["SETUP_TEST_RELEASE"])
if sys.argv[1:] == ["child"]:
    while not release.exists():
        time.sleep(0.01)
    sys.exit(0)
name = Path(sys.argv[0]).name
args = sys.argv[1:]
if name == "git" and os.environ["SETUP_TEST_STAGE"] == "build":
    os.execv(os.environ["SETUP_TEST_GIT"], ["git", *args])
if ((name == "npm" and os.environ["SETUP_TEST_STAGE"] == "prepare") or
        (name == "go" and args[0] == "build" and os.environ["SETUP_TEST_STAGE"] == "build")) and not release.exists():
    child = subprocess.Popen([sys.executable, sys.argv[0], "child"])
    ready.write_text(json.dumps([os.getpid(), child.pid]))
    child.wait()
if name == "git" and args[:3] == ["remote", "get-url", "origin"]:
    print("https://example.test/owned.git")
elif name == "go" and args == ["env", "GOVERSION"]:
    print("go1.27.1")
elif name == "go" and args[0] == "build":
    Path(args[args.index("-o") + 1]).write_text("owned daemon")
elif name == "node" and args == ["--version"]:
    print("v24.21.0")
elif name == "node" and Path(args[0]).name == "prepare.cjs":
    Path(".vite").mkdir(exist_ok=True)
    Path(".vite/testing-target.json").write_text("{}")
elif name == "npm":
    Path("node_modules").mkdir(exist_ok=True)
`
	for _, name := range []string{"git", "go", "node", "npm"} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SETUP_TEST_READY", ready)
	t.Setenv("SETUP_TEST_RELEASE", release)
	t.Setenv("SETUP_TEST_STAGE", stage)
	t.Setenv("SETUP_TEST_GIT", git)
	return ready, release
}

func cancelSetupAtChild(t *testing.T, ready, release string, run func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	var pids []int
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		deadline := time.Now().Add(5 * time.Second)
		for _, pid := range pids {
			for processalive.Alive(pid) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if processalive.Alive(pid) {
				t.Errorf("setup child %d survived test cleanup", pid)
			}
		}
	})
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for len(pids) == 0 {
		if data, err := os.ReadFile(ready); err == nil {
			_ = json.Unmarshal(data, &pids)
		}
		if len(pids) > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("preparation exited before its child started: %v", err)
		case <-deadline.C:
			t.Fatal("setup child did not become ready")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancellation cause lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("setup command did not stop its process tree")
	}
	for _, pid := range pids {
		if processalive.Alive(pid) {
			t.Errorf("setup child %d survived cancellation", pid)
		}
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPreparationCancellationStopsChildrenAndReusesCheckout(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ready, release := setupCancellationTools(t, root, "prepare")
	t.Setenv("HOME", root)
	checkout := filepath.Join(root, ".ao", "dev", "agentic-target", "repos", "owned", "checkout")
	for _, name := range []string{"backend", "frontend/src/shared", "packages/product-ui", "frontend/node_modules"} {
		if err := os.MkdirAll(filepath.Join(checkout, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"backend/go.mod":                        "module example.test/owned\n\ngo 1.27.1\n",
		"frontend/src/main.ts":                  `import {resolveDaemonLaunch} from "./shared/daemon-launch"; resolveDaemonLaunch();`,
		"frontend/src/shared/daemon-launch.ts":  "export function resolveDaemonLaunch() {}",
		"frontend/package.json":                 "{}",
		"frontend/package-lock.json":            "{}",
		"packages/product-ui/package-lock.json": "{}",
		"frontend/node_modules/warm-cache":      "preserve me",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(checkout, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	frontend := filepath.Join(checkout, "frontend")
	cancelSetupAtChild(t, ready, release, func(ctx context.Context) error {
		return prepareTarget(ctx, frontend, strings.Repeat("a", 40), first)
	})
	if _, err := os.Lstat(filepath.Join(checkout, ".ao-testing-active")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled preparation retained its checkout reservation: %v", err)
	}
	if err := prepareTarget(t.Context(), frontend, strings.Repeat("b", 40), second); err != nil {
		t.Fatalf("next preparation could not reuse checkout: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(frontend, "node_modules/warm-cache")); err != nil || string(data) != "preserve me" {
		t.Fatalf("warm checkout cache changed: %q %v", data, err)
	}
}

func TestBuildCancellationStopsChildrenAndReusesCheckout(t *testing.T) {
	f, spec := preparedStartFixture(t, false)
	f.a.ops.prepare = func(context.Context, string, string, string) error { return nil }
	f.a.ops.build = buildOwnedDaemon
	backend := filepath.Join(spec.CheckoutPath, "backend")
	if err := os.Mkdir(backend, 0o700); err != nil {
		t.Fatal(err)
	}
	ready, release := setupCancellationTools(t, t.TempDir(), "build")
	f.a.ops.start = func(string, string, []string, *os.File) (int, error) {
		t.Fatal("cancelled build launched Electron")
		return 0, nil
	}
	cancelSetupAtChild(t, ready, release, func(ctx context.Context) error {
		_, err := f.a.Start(ctx, spec)
		return err
	})
	if _, err := os.Lstat(filepath.Join(spec.CheckoutPath, ".ao-testing-active")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled build retained its checkout reservation: %v", err)
	}
	// Resume with the fake Electron process only after the real build tree is gone.
	spec.AttemptID = "after-cancel"
	f.a.ops.start = func(_, _ string, env []string, _ *os.File) (int, error) {
		for _, entry := range env {
			if id, ok := strings.CutPrefix(entry, "AO_APP_RUN_ID=target-"); ok {
				f.s = f.a.launches[id]
			}
		}
		f.s.target.DaemonPID, f.s.target.DaemonStartedAt = 12, f.times[12]
		writeInfo(t, f.s)
		f.ready["executablePath"] = f.s.daemon
		f.ready["workingDirectory"] = f.s.target.DataDir
		f.ready["startupWorkingDirectory"] = f.s.frontend
		return 11, nil
	}
	target, err := f.a.Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("next attempt could not reuse checkout: %v", err)
	}
	if receipt, err := f.a.Stop(t.Context(), target); err != nil || receipt.State != domain.TestCleanupComplete {
		t.Fatalf("resumed attempt cleanup failed: %v %v", receipt, err)
	}
}

// An unreadable process inventory retains ownership instead of declaring cleanup.
func TestFailedPreparationObservationRetainsCleanupIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local desktop preparation uses POSIX process groups")
	}
	for _, stage := range []string{"prepare", "build"} {
		t.Run(stage, func(t *testing.T) {
			f, spec := preparedStartFixture(t, false)
			observationErr := errors.New("OS process inventory unavailable")
			leasePath := filepath.Join(spec.CheckoutPath, ".ao-testing-active")
			var privateData string
			failObservation := func(root string) error {
				privateData = filepath.Join(root, "data")
				if err := os.Mkdir(privateData, 0o700); err != nil {
					t.Fatal(err)
				}
				if stage == "prepare" {
					lease, err := os.OpenFile(leasePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := lease.WriteString(root); err != nil {
						t.Fatal(err)
					}
					if err := lease.Close(); err != nil {
						t.Fatal(err)
					}
				}
				info, err := os.Lstat(leasePath)
				if err != nil {
					t.Fatal(err)
				}
				return &setupGroupError{group: 1 << 30, cause: observationErr, leaseInfo: info}
			}
			if stage == "prepare" {
				f.a.ops.prepare = func(_ context.Context, _, _, root string) error { return failObservation(root) }
			} else {
				f.a.ops.build = func(_ context.Context, _, executable string) error {
					return failObservation(filepath.Dir(filepath.Dir(executable)))
				}
			}
			f.a.ops.start = func(string, string, []string, *os.File) (int, error) {
				t.Fatal("unverified setup launched Electron")
				return 0, nil
			}
			target, err := f.a.Start(t.Context(), spec)
			if !errors.Is(err, observationErr) || target.ID == "" || target.ElectronPID != 0 {
				t.Fatalf("failed setup lost its cleanup identity/cause: %+v %v", target, err)
			}
			logs, err := f.a.ReadLogs(t.Context(), target, domain.TestReadLogsRequest{MaxBytes: 1})
			if err != nil || logs.Text != "" || logs.NextCursor != "0" || logs.Truncated {
				t.Fatalf("failed setup could not return bounded final logs: %+v %v", logs, err)
			}
			for _, path := range []string{privateData, leasePath} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("unverified shutdown removed %s: %v", path, err)
				}
			}
			cleanupCtx, cancel := context.WithCancel(t.Context())
			cancel()
			failed, err := f.a.Stop(cleanupCtx, target)
			if failed.State != domain.TestCleanupFailed || !errors.Is(err, context.Canceled) {
				t.Fatalf("unobserved preparation falsely completed cleanup: %v %v", failed, err)
			}
			for _, path := range []string{privateData, leasePath} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("failed cleanup removed %s: %v", path, err)
				}
			}
			f.a.ops.processes = func(context.Context) ([]processInfo, error) {
				t.Fatal("unlaunched target requested an application process inventory")
				return nil, nil
			}
			receipt, err := f.a.Stop(t.Context(), target)
			if err != nil || receipt.State != domain.TestCleanupComplete {
				t.Fatalf("known-absent preparation group could not finish cleanup: %v %v", receipt, err)
			}
			for _, path := range []string{privateData, leasePath} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("setup state remained at %s: %v", path, err)
				}
			}
		})
	}
}
