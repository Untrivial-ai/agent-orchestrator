// Package claudetrust is a launch gate that makes one config root the single
// place Claude's workspace trust is written and read.
//
// The defect it closes was measured on a running install. Trust was recorded
// true in the operator's home Claude state for the exact session worktree
// paths, and the live child ran with CLAUDE_CONFIG_DIR pointing at a different
// root whose state file had none of those entries. Every surface reported that
// the trust existed; the child still stopped at the prompt that trust was meant
// to answer, before its agent loop, and AO reported it as ordinary work.
//
// Two roots is the whole bug. This gate resolves one AO-owned root per session,
// writes the exact worktree's trust there, verifies it by reading it back, and
// points the child at that same root -- for the session's own agent and for its
// reviewer alike, because the incident was observed on a reviewer child.
package claudetrust

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// EnvConfigDir is the variable Claude reads to choose its configuration root.
const EnvConfigDir = "CLAUDE_CONFIG_DIR"

const stateFile = ".claude.json"

// Gate implements ports.LaunchGate for Claude children.
type Gate struct {
	// Base is the AO-owned directory under which per-session roots are created.
	// One root per session keeps a worker and its reviewer in the same state
	// while keeping different sessions out of each other's.
	Base string
	// AppliesTo reports whether a launch is a Claude launch. Nil means the gate
	// inspects argv, which is what AO resolves and what the child will run.
	AppliesTo func(req ports.PreLaunchRequest) bool
}

// PreLaunch resolves the root, writes and verifies trust, and redirects the
// child to that root. A launch it cannot make trustworthy is refused, because
// the alternative is the state this gate exists to prevent: a child that starts,
// stops at a prompt, and is reported as running.
func (g Gate) PreLaunch(_ context.Context, req ports.PreLaunchRequest) (ports.PreLaunchDecision, error) {
	if !g.applies(req) {
		return ports.PreLaunchDecision{Allow: true}, nil
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return ports.PreLaunchDecision{Reason: "launch has no session id to key a config root on"}, nil
	}
	if strings.TrimSpace(req.WorkspacePath) == "" {
		return ports.PreLaunchDecision{Reason: "launch has no workspace path to trust"}, nil
	}
	root, err := g.root(req.SessionID)
	if err != nil {
		return ports.PreLaunchDecision{}, err
	}
	if err := writeTrust(root, req.WorkspacePath); err != nil {
		return ports.PreLaunchDecision{}, err
	}
	// Read it back from the same file the child will read. A write that
	// succeeded and a state the child can see are not the same claim.
	trusted, err := hasTrust(root, req.WorkspacePath)
	if err != nil {
		return ports.PreLaunchDecision{}, err
	}
	if !trusted {
		return ports.PreLaunchDecision{
			Reason:     fmt.Sprintf("workspace trust for %s is absent from %s after writing it", req.WorkspacePath, root),
			PromptKind: "workspace_trust",
		}, nil
	}
	// Override, not contribute: an inherited CLAUDE_CONFIG_DIR is exactly the
	// case that produced the incident, and leaving it in place would send the
	// child back to the root that lacks this entry.
	return ports.PreLaunchDecision{
		Allow:       true,
		EnvOverride: map[string]string{EnvConfigDir: root},
	}, nil
}

func (g Gate) applies(req ports.PreLaunchRequest) bool {
	if g.AppliesTo != nil {
		return g.AppliesTo(req)
	}
	for _, argument := range req.Argv {
		if base := filepath.Base(argument); base == "claude" {
			return true
		}
	}
	return false
}

func (g Gate) root(sessionID string) (string, error) {
	base := strings.TrimSpace(g.Base)
	if base == "" {
		return "", fmt.Errorf("claudetrust: no AO-owned config base configured")
	}
	if strings.ContainsAny(sessionID, `/\`) || sessionID == "." || sessionID == ".." {
		return "", fmt.Errorf("claudetrust: session id is not a single path segment")
	}
	if err := ensureRealDir(base); err != nil {
		return "", err
	}
	root := filepath.Join(base, sessionID)
	if err := ensureRealDir(root); err != nil {
		return "", err
	}
	return root, nil
}

// ensureRealDir creates a directory and refuses a symlink at that exact path.
//
// A same-user process that can write AO's data directory could otherwise
// pre-create a predictable session root as a symlink, and the gate would then
// write a .claude.json outside the daemon's data directory entirely.
// os.MkdirAll is content with an existing symlink to a directory, so the check
// is explicit and uses Lstat, which does not follow the final component.
func ensureRealDir(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("claudetrust: %s is a symlink; refusing to write through it", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("claudetrust: %s exists and is not a directory", path)
		}
	case os.IsNotExist(err):
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("claudetrust: create config root parent: %w", err)
		}
		// Mkdir, not MkdirAll, on the final component: MkdirAll succeeds when
		// the path already resolves through a symlink, which is the case being
		// rejected. Mkdir fails with EEXIST instead, and the Lstat above then
		// names it.
		if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
			return fmt.Errorf("claudetrust: create config root: %w", err)
		}
		return ensureRealDir(path)
	default:
		return fmt.Errorf("claudetrust: inspect config root: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("claudetrust: secure config root: %w", err)
	}
	return nil
}

// withRootLock serializes read/modify/write of one session's state across
// processes and goroutines. Atomic replacement alone is not enough: two callers
// that both load, both mutate, and both rename lose whichever wrote first,
// including unrelated entries the loser had added.
func withRootLock(root string, action func() error) (err error) {
	lockPath := filepath.Join(root, ".launch-gate.lock")
	if info, statErr := os.Lstat(lockPath); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("claudetrust: lock path is a symlink; refusing to follow it")
	}
	flags := os.O_RDWR | os.O_CREATE
	if syscall.O_NOFOLLOW != 0 {
		flags |= syscall.O_NOFOLLOW
	}
	handle, openErr := os.OpenFile(lockPath, flags, 0o600)
	if openErr != nil {
		return fmt.Errorf("claudetrust: open state lock: %w", openErr)
	}
	defer func() {
		if closeErr := handle.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("claudetrust: close state lock: %w", closeErr)
		}
	}()
	if lockErr := syscall.Flock(int(handle.Fd()), syscall.LOCK_EX); lockErr != nil {
		return fmt.Errorf("claudetrust: lock state: %w", lockErr)
	}
	defer func() {
		if unlockErr := syscall.Flock(int(handle.Fd()), syscall.LOCK_UN); unlockErr != nil && err == nil {
			err = fmt.Errorf("claudetrust: unlock state: %w", unlockErr)
		}
	}()
	return action()
}

func loadState(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("claudetrust: read state: %w", err)
	}
	state := map[string]any{}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		// Refuse rather than replace: an unreadable state file may be a live
		// child's, and overwriting it would destroy conversation history.
		return nil, fmt.Errorf("claudetrust: state is not a JSON object; it was not replaced")
	}
	return state, nil
}

func writeTrust(root, worktree string) error {
	return withRootLock(root, func() error { return writeTrustLocked(root, worktree) })
}

func writeTrustLocked(root, worktree string) error {
	path := filepath.Join(root, stateFile)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("claudetrust: %s is a symlink; refusing to write through it", path)
	}
	state, err := loadState(path)
	if err != nil {
		return err
	}
	projects, _ := state["projects"].(map[string]any)
	if projects == nil {
		if _, present := state["projects"]; present {
			return fmt.Errorf("claudetrust: projects state is not an object; it was not replaced")
		}
		projects = map[string]any{}
	}
	project, _ := projects[worktree].(map[string]any)
	if project == nil {
		if _, present := projects[worktree]; present {
			return fmt.Errorf("claudetrust: project state is not an object; it was not replaced")
		}
		project = map[string]any{}
	}
	project["hasTrustDialogAccepted"] = true
	projects[worktree] = project
	state["projects"] = projects

	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("claudetrust: encode state: %w", err)
	}
	temporary, err := os.CreateTemp(root, ".claude-state-")
	if err != nil {
		return fmt.Errorf("claudetrust: stage state: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("claudetrust: write state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("claudetrust: sync state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("claudetrust: close state: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("claudetrust: secure state: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("claudetrust: persist state: %w", err)
	}
	return nil
}

// hasTrust reports whether the exact worktree path is trusted in this root. The
// path is compared exactly: a parent entry must not confer trust on a child
// directory that nobody trusted.
func hasTrust(root, worktree string) (bool, error) {
	path := filepath.Join(root, stateFile)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("claudetrust: %s is a symlink; refusing to read through it", path)
	}
	state, err := loadState(path)
	if err != nil {
		return false, err
	}
	projects, _ := state["projects"].(map[string]any)
	if projects == nil {
		return false, nil
	}
	project, _ := projects[worktree].(map[string]any)
	if project == nil {
		return false, nil
	}
	accepted, _ := project["hasTrustDialogAccepted"].(bool)
	return accepted, nil
}
