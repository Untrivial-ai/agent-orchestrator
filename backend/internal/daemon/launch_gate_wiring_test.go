package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/launchgate/claudetrust"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Construction regressions for the Claude launch gate.
//
// The gate only closes the two-roots defect if the daemon actually builds one
// and gives it to both paths that create a Claude child. A gate that exists in
// the tree but is never constructed leaves installed behaviour exactly as it
// was, which is the state the previous commit shipped and this one ends.

func TestClaudeLaunchGateRootLivesUnderTheDaemonDataDir(t *testing.T) {
	dataDir := t.TempDir()

	gate := claudeLaunchGate(dataDir)
	if gate == nil {
		t.Fatal("a configured data directory must produce a gate")
	}
	typed, ok := gate.(claudetrust.Gate)
	if !ok {
		t.Fatalf("gate = %T, want claudetrust.Gate", gate)
	}
	want := filepath.Join(dataDir, ClaudeSessionConfigDirName)
	if typed.Base != want {
		t.Fatalf("gate base = %q, want %q", typed.Base, want)
	}
	// The root must be derived from what the daemon owns. A home directory or a
	// path taken from the environment is how the writer and the child end up in
	// different roots in the first place.
	if !strings.HasPrefix(typed.Base, dataDir) {
		t.Fatalf("gate base %q escapes the daemon data directory %q", typed.Base, dataDir)
	}
}

func TestClaudeLaunchGateIsAbsentWithoutADataDir(t *testing.T) {
	for _, dataDir := range []string{"", "   "} {
		if gate := claudeLaunchGate(dataDir); gate != nil {
			t.Fatalf("data dir %q produced a gate; embedders without one must keep prior behaviour", dataDir)
		}
	}
}

// The integration property, stated as one fact: a session's own agent and its
// reviewer receive the same effective config root from the same gate. Two roots
// for one session is the defect wearing a different hat.
func TestOneGateGivesWorkerAndReviewerTheSameRoot(t *testing.T) {
	dataDir := t.TempDir()
	worktree := t.TempDir()
	gate := claudeLaunchGate(dataDir)

	roots := map[ports.LaunchRole]string{}
	for _, role := range []ports.LaunchRole{ports.LaunchRoleWorker, ports.LaunchRoleReviewer} {
		decision, err := gate.PreLaunch(context.Background(), ports.PreLaunchRequest{
			SessionID:     "mer-1",
			WorkspacePath: worktree,
			Argv:          []string{"/usr/local/bin/claude"},
			Role:          role,
			// Each child inherits a different wrong root, which is what the
			// live incident looked like.
			Env: map[string]string{claudetrust.EnvConfigDir: "/inherited/" + string(role)},
		})
		if err != nil {
			t.Fatalf("%s PreLaunch: %v", role, err)
		}
		if !decision.Allow {
			t.Fatalf("%s refused: %s", role, decision.Reason)
		}
		root := decision.EnvOverride[claudetrust.EnvConfigDir]
		if root == "" {
			t.Fatalf("%s was not redirected off its inherited root", role)
		}
		roots[role] = root
	}
	if roots[ports.LaunchRoleWorker] != roots[ports.LaunchRoleReviewer] {
		t.Fatalf("worker root %q != reviewer root %q", roots[ports.LaunchRoleWorker], roots[ports.LaunchRoleReviewer])
	}
	if !strings.HasPrefix(roots[ports.LaunchRoleWorker], filepath.Join(dataDir, ClaudeSessionConfigDirName)) {
		t.Fatalf("shared root %q is not under the daemon's own config base", roots[ports.LaunchRoleWorker])
	}
}

// Different sessions stay out of each other's state even though they share the
// base directory.
func TestSessionsGetSeparateRootsUnderOneBase(t *testing.T) {
	dataDir := t.TempDir()
	worktree := t.TempDir()
	gate := claudeLaunchGate(dataDir)

	seen := map[string]string{}
	for _, sessionID := range []string{"mer-1", "mer-2"} {
		decision, err := gate.PreLaunch(context.Background(), ports.PreLaunchRequest{
			SessionID:     sessionID,
			WorkspacePath: worktree,
			Argv:          []string{"claude"},
			Role:          ports.LaunchRoleWorker,
		})
		if err != nil {
			t.Fatalf("%s PreLaunch: %v", sessionID, err)
		}
		seen[sessionID] = decision.EnvOverride[claudetrust.EnvConfigDir]
	}
	if seen["mer-1"] == seen["mer-2"] {
		t.Fatalf("both sessions resolved the same root %q", seen["mer-1"])
	}
}
