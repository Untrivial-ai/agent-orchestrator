package local

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestStartPreparesHeadAfterBaseCleanupAndRejectsStaleManifest(t *testing.T) {
	f, base := preparedStartFixture(t, false)
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir, command.Env = base.CheckoutPath, strippedEnv(os.Environ())
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %s %v", output, err)
		}
		return strings.TrimSpace(string(output))
	}
	git("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--quiet", "--allow-empty", "--no-verify", "-m", "head")
	head := base
	head.AttemptID, head.Generation, head.CommitSHA = "head", 2, git("rev-parse", "HEAD")
	git("checkout", "--quiet", "--detach", base.CommitSHA)
	baseTarget, err := f.a.Start(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := f.a.Stop(context.Background(), baseTarget)
	if err != nil || cleanup.State != domain.TestCleanupComplete || len(cleanup.Leftovers) != 0 {
		t.Fatal("base cleanup failed", cleanup, err)
	}
	// Without preparation, the real adapter must refuse base's manifest for head.
	f.a.ops.prepare = nil
	if target, err := f.a.Start(context.Background(), head); err == nil || target.ID != "" || !strings.Contains(err.Error(), "prepared target revision differs") {
		t.Fatal("stale preparation admitted", target, err)
	}
	frontend := filepath.Join(base.CheckoutPath, "frontend")
	manifestPath := filepath.Join(frontend, ".vite", "testing-target.json")
	f.a.ops.prepare = func(_ context.Context, gotFrontend, commit, root string) error {
		if gotFrontend != frontend || commit != head.CommitSHA || root != filepath.Join(filepath.Dir(base.CheckoutPath), "head") {
			t.Fatal("head preparation escaped its pinned checkout or attempt")
		}
		git("checkout", "--quiet", "--detach", commit)
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return err
		}
		var manifest map[string]any
		if err := json.Unmarshal(data, &manifest); err != nil {
			return err
		}
		manifest["commitSHA"] = commit
		data, err = json.Marshal(manifest)
		if err != nil {
			return err
		}
		return os.WriteFile(manifestPath, data, 0o600)
	}
	f.a.ops.build = func(_ context.Context, _ string, executable string) error {
		if err := os.Mkdir(filepath.Dir(executable), 0o700); err != nil {
			return err
		}
		return os.WriteFile(executable, []byte("owned head daemon"), 0o700)
	}
	f.a.ops.start = func(_ string, _ string, env []string, _ *os.File) (int, error) {
		for _, value := range env {
			if id, ok := strings.CutPrefix(value, "AO_APP_RUN_ID=target-"); ok {
				f.s = f.a.launches[id]
			}
		}
		for _, pid := range []int{11, 12, 13} {
			f.times[pid] = f.times[pid].Add(time.Second)
			f.pids[pid] = processInfo{PID: pid, Parent: 11}
		}
		f.pids[11] = processInfo{PID: 11, Parent: 1}
		f.s.target.DaemonPID, f.s.target.DaemonStartedAt = 12, f.times[12]
		writeInfo(t, f.s)
		f.ready["executablePath"], f.ready["workingDirectory"] = f.s.daemon, f.s.target.DataDir
		f.ready["startupWorkingDirectory"] = frontend
		return 11, nil
	}
	headTarget, err := f.a.Start(context.Background(), head)
	if err != nil || headTarget.ID == baseTarget.ID || headTarget.Generation != head.Generation {
		t.Fatal("head did not launch with a fresh identity", headTarget, err)
	}
	if err := prepared(context.Background(), frontend, head.CommitSHA); err != nil {
		t.Fatal("head was not prepared at its pinned SHA", err)
	}
	if cleanup, err := f.a.Stop(context.Background(), headTarget); err != nil || cleanup.State != domain.TestCleanupComplete {
		t.Fatal("head cleanup failed", cleanup, err)
	}
}

func TestStartRejectionCleansPrivatePreparation(t *testing.T) {
	probeErr := errors.New("port probe failed")
	for _, test := range []struct {
		name string
		port int
		err  error
	}{{"zero", 0, nil}, {"live", 3001, nil}, {"out of range", 65536, nil}, {"probe", 0, probeErr}} {
		t.Run(test.name, func(t *testing.T) {
			f, spec := preparedStartFixture(t, false)
			a := f.a
			root := filepath.Join(filepath.Dir(spec.CheckoutPath), string(spec.AttemptID))
			prepare := a.ops.prepare
			a.ops.prepare = func(ctx context.Context, frontend, commit, path string) error {
				if err := prepare(ctx, frontend, commit, path); err != nil {
					return err
				}
				for _, name := range []string{"data", "electron", "daemon", "setup"} {
					if err := os.Mkdir(filepath.Join(path, name), 0700); err != nil {
						return err
					}
				}
				return os.WriteFile(filepath.Join(path, "diagnostic.json"), []byte("checked facts"), 0600)
			}
			a.ops.freePort = func() (int, error) { return test.port, test.err }
			target, err := a.Start(context.Background(), spec)
			if err == nil || target.ID != "" || (test.err != nil && !errors.Is(err, test.err)) {
				t.Fatalf("rejected start: target=%+v err=%v", target, err)
			}
			for _, name := range []string{"data", "electron", "fixtures", "daemon", "setup"} {
				if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("private %s remains: %v", name, err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "diagnostic.json")); err != nil {
				t.Fatal("preparation diagnostic lost", err)
			}
			if _, err := os.Stat(filepath.Join(spec.CheckoutPath, "frontend", ".vite", "testing-target.json")); err != nil {
				t.Fatal("warm checkout changed", err)
			}
		})
	}
}

func TestStartRejectionRefusesChangedPrivateRoot(t *testing.T) {
	f, spec := preparedStartFixture(t, false)
	a := f.a
	root := filepath.Join(filepath.Dir(spec.CheckoutPath), string(spec.AttemptID))
	a.ops.freePort = func() (int, error) {
		if err := os.Rename(root, root+"-owned"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "fixtures"), 0700); err != nil {
			t.Fatal(err)
		}
		return 0, nil
	}
	_, err := a.Start(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "target state directory ownership changed") {
		t.Fatalf("changed root cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "fixtures")); err != nil {
		t.Fatal("changed root was modified", err)
	}
}
