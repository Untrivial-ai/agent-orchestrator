package claudetrust

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func request(sessionID, worktree string, role ports.LaunchRole, env map[string]string) ports.PreLaunchRequest {
	return ports.PreLaunchRequest{
		SessionID:     sessionID,
		WorkspacePath: worktree,
		Argv:          []string{"/usr/local/bin/claude", "--permission-mode", "acceptEdits"},
		Env:           env,
		Role:          role,
	}
}

func trustedPaths(t *testing.T, root string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, stateFile))
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	var state struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode %s: %v", root, err)
	}
	out := map[string]bool{}
	for path, project := range state.Projects {
		out[path] = project.HasTrustDialogAccepted
	}
	return out
}

// The measured incident, as a regression.
//
// Sessions 61, 62, 67 and 68 each had hasTrustDialogAccepted true in the
// operator's default Claude root for their exact worktree paths, while the live
// child ran with CLAUDE_CONFIG_DIR pointing at another root whose state file had
// none of those entries. Trust in the wrong root is trust the child never sees.
func TestWrongRootTrustIsNotEnoughAndTheChildIsRedirected(t *testing.T) {
	base := t.TempDir()
	defaultRoot := t.TempDir()   // stands in for the operator's own root
	inheritedRoot := t.TempDir() // stands in for the root the child inherited

	worktrees := map[string]string{
		"setup-agent-orchestrator-61": filepath.Join(t.TempDir(), "61"),
		"setup-agent-orchestrator-62": filepath.Join(t.TempDir(), "62"),
		"setup-agent-orchestrator-67": filepath.Join(t.TempDir(), "67"),
		"setup-agent-orchestrator-68": filepath.Join(t.TempDir(), "68"),
	}
	// Trust exists, in the wrong place, for every one of them.
	for _, worktree := range worktrees {
		if err := writeTrust(defaultRoot, worktree); err != nil {
			t.Fatalf("seed default root: %v", err)
		}
	}
	// And the inherited root, which the child would actually read, has nothing.
	if err := os.WriteFile(filepath.Join(inheritedRoot, stateFile), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("seed inherited root: %v", err)
	}

	gate := Gate{Base: base}
	for sessionID, worktree := range worktrees {
		t.Run(sessionID, func(t *testing.T) {
			// The precondition that made the incident invisible.
			if !trustedPaths(t, defaultRoot)[worktree] {
				t.Fatal("fixture no longer reproduces: default root should trust this worktree")
			}
			if trustedPaths(t, inheritedRoot)[worktree] {
				t.Fatal("fixture no longer reproduces: inherited root should not trust it")
			}

			decision, err := gate.PreLaunch(context.Background(),
				request(sessionID, worktree, ports.LaunchRoleWorker,
					map[string]string{EnvConfigDir: inheritedRoot}))
			if err != nil {
				t.Fatalf("PreLaunch: %v", err)
			}
			if !decision.Allow {
				t.Fatalf("launch refused: %s", decision.Reason)
			}

			root := decision.EnvOverride[EnvConfigDir]
			if root == "" {
				t.Fatal("gate must redirect the child away from the inherited root")
			}
			if root == inheritedRoot {
				t.Fatal("gate left the child on the root that lacks the trust entry")
			}
			if !strings.HasPrefix(root, base) {
				t.Fatalf("config root %q is not under the AO-owned base %q", root, base)
			}
			if !trustedPaths(t, root)[worktree] {
				t.Fatalf("root %q does not trust the exact worktree %q", root, worktree)
			}
		})
	}
}

// Parity: the reviewer resolves the same root as its worker, so one write serves
// both children. The incident was observed on a reviewer.
func TestWorkerAndReviewerResolveOneRoot(t *testing.T) {
	base := t.TempDir()
	worktree := t.TempDir()
	gate := Gate{Base: base}

	worker, err := gate.PreLaunch(context.Background(),
		request("mer-1", worktree, ports.LaunchRoleWorker, nil))
	if err != nil {
		t.Fatalf("worker PreLaunch: %v", err)
	}
	reviewer, err := gate.PreLaunch(context.Background(),
		request("mer-1", worktree, ports.LaunchRoleReviewer,
			map[string]string{EnvConfigDir: "/somewhere/else"}))
	if err != nil {
		t.Fatalf("reviewer PreLaunch: %v", err)
	}
	if worker.EnvOverride[EnvConfigDir] != reviewer.EnvOverride[EnvConfigDir] {
		t.Fatalf("worker root %q != reviewer root %q; one session must have one root",
			worker.EnvOverride[EnvConfigDir], reviewer.EnvOverride[EnvConfigDir])
	}
	if !trustedPaths(t, reviewer.EnvOverride[EnvConfigDir])[worktree] {
		t.Fatal("the shared root must trust the exact worktree")
	}
}

// Trust is per exact path. A trusted parent must not confer trust on a child
// directory nobody trusted, or the gate would report a trust that Claude does
// not apply.
func TestTrustIsExactPathNotPrefix(t *testing.T) {
	root := t.TempDir()
	parent := t.TempDir()
	child := filepath.Join(parent, "nested")
	if err := writeTrust(root, parent); err != nil {
		t.Fatalf("seed: %v", err)
	}
	trusted, err := hasTrust(root, child)
	if err != nil {
		t.Fatalf("hasTrust: %v", err)
	}
	if trusted {
		t.Fatal("a trusted parent must not make a nested worktree trusted")
	}
}

// Existing state in the AO-owned root is preserved: a second session's entry and
// any unrelated key Claude keeps must survive the write.
func TestWriteTrustPreservesUnrelatedState(t *testing.T) {
	root := t.TempDir()
	seed := map[string]any{
		"projects":     map[string]any{"/other/worktree": map[string]any{"hasTrustDialogAccepted": true}},
		"oauthAccount": map[string]any{"emailAddress": "someone@example.invalid"},
	}
	encoded, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(filepath.Join(root, stateFile), encoded, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := writeTrust(root, "/new/worktree"); err != nil {
		t.Fatalf("writeTrust: %v", err)
	}
	paths := trustedPaths(t, root)
	if !paths["/other/worktree"] || !paths["/new/worktree"] {
		t.Fatalf("both worktrees must be trusted, got %v", paths)
	}
	raw, _ := os.ReadFile(filepath.Join(root, stateFile))
	if !strings.Contains(string(raw), "someone@example.invalid") {
		t.Fatal("unrelated Claude state must survive the trust write")
	}
}

// A non-Claude launch is left completely alone.
func TestNonClaudeLaunchIsUntouched(t *testing.T) {
	gate := Gate{Base: t.TempDir()}
	req := request("mer-1", t.TempDir(), ports.LaunchRoleWorker, nil)
	req.Argv = []string{"/usr/local/bin/codex", "--sandbox", "danger-full-access"}

	decision, err := gate.PreLaunch(context.Background(), req)
	if err != nil {
		t.Fatalf("PreLaunch: %v", err)
	}
	if !decision.Allow {
		t.Fatal("a non-Claude launch must not be refused by this gate")
	}
	if len(decision.EnvOverride) != 0 || len(decision.Env) != 0 {
		t.Fatalf("a non-Claude launch must not gain environment: %+v", decision)
	}
}

// Unusable inputs refuse rather than write somewhere unexpected.
func TestUnusableInputsRefuse(t *testing.T) {
	worktree := t.TempDir()
	for name, req := range map[string]ports.PreLaunchRequest{
		"no session id": request("", worktree, ports.LaunchRoleWorker, nil),
		"no workspace":  request("mer-1", "", ports.LaunchRoleWorker, nil),
	} {
		t.Run(name, func(t *testing.T) {
			decision, err := Gate{Base: t.TempDir()}.PreLaunch(context.Background(), req)
			if err != nil {
				t.Fatalf("PreLaunch: %v", err)
			}
			if decision.Allow {
				t.Fatal("an unusable request must not be allowed")
			}
			if strings.TrimSpace(decision.Reason) == "" {
				t.Fatal("a refusal must say why")
			}
		})
	}
	if _, err := (Gate{}).PreLaunch(context.Background(),
		request("mer-1", worktree, ports.LaunchRoleWorker, nil)); err == nil {
		t.Fatal("a gate with no AO-owned base must error rather than guess a root")
	}
	if _, err := (Gate{Base: t.TempDir()}).PreLaunch(context.Background(),
		request("../escape", worktree, ports.LaunchRoleWorker, nil)); err == nil {
		t.Fatal("a session id that is not one path segment must error")
	}
}

// P1 from review 5113322042: a same-user process that can write AO's data
// directory could pre-create a predictable session root as a symlink, and the
// gate would then write .claude.json outside the daemon's data directory.
func TestSymlinkedSessionRootIsRefused(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "mer-1")); err != nil {
		t.Fatalf("stage symlink: %v", err)
	}

	_, err := Gate{Base: base}.PreLaunch(context.Background(),
		request("mer-1", t.TempDir(), ports.LaunchRoleWorker, nil))

	if err == nil {
		t.Fatal("a symlinked session root must be refused, not written through")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want it to name the symlink", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, stateFile)); statErr == nil {
		t.Fatal("the gate wrote state outside the AO-owned base")
	}
}

func TestSymlinkedBaseIsRefused(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	base := filepath.Join(parent, "config-base")
	if err := os.Symlink(outside, base); err != nil {
		t.Fatalf("stage symlink: %v", err)
	}

	_, err := Gate{Base: base}.PreLaunch(context.Background(),
		request("mer-1", t.TempDir(), ports.LaunchRoleWorker, nil))

	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want a symlinked base refused", err)
	}
}

func TestSymlinkedStateFileIsRefused(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "mer-1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("stage root: %v", err)
	}
	elsewhere := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(elsewhere, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("stage target: %v", err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, stateFile)); err != nil {
		t.Fatalf("stage symlink: %v", err)
	}

	_, err := Gate{Base: base}.PreLaunch(context.Background(),
		request("mer-1", t.TempDir(), ports.LaunchRoleWorker, nil))

	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want a symlinked state file refused", err)
	}
	raw, readErr := os.ReadFile(elsewhere)
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if strings.Contains(string(raw), "hasTrustDialogAccepted") {
		t.Fatal("the gate wrote through the symlink")
	}
}

// Atomic replacement alone is not enough: two callers that both load, mutate
// and rename lose whichever wrote first, including unrelated entries. Every
// worktree written concurrently must survive.
func TestConcurrentTrustWritesAllSurvive(t *testing.T) {
	base := t.TempDir()
	gate := Gate{Base: base}
	worktrees := make([]string, 12)
	for i := range worktrees {
		worktrees[i] = filepath.Join(t.TempDir(), "wt")
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(worktrees))
	for _, worktree := range worktrees {
		wg.Add(1)
		go func(worktree string) {
			defer wg.Done()
			if _, err := gate.PreLaunch(context.Background(),
				request("mer-1", worktree, ports.LaunchRoleWorker, nil)); err != nil {
				errs <- err
			}
		}(worktree)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent PreLaunch: %v", err)
	}

	trusted := trustedPaths(t, filepath.Join(base, "mer-1"))
	for _, worktree := range worktrees {
		if !trusted[worktree] {
			t.Fatalf("worktree %q was lost by a concurrent write; %d of %d survived",
				worktree, len(trusted), len(worktrees))
		}
	}
}
