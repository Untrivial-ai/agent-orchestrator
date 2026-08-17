// Package vibe contains AO's experimental host-trusted Mistral Vibe reviewer.
package vibe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	agentvibe "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/vibe"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/internal/reviewgateway"
)

const (
	pinnedVersion = "2.23.2"
	reviewerAgent = "plan"
)

// HarnessID identifies the Vibe reviewer adapter.
const HarnessID domain.ReviewerHarness = "vibe"

// HostTrustWarning documents the authority retained by Vibe's interactive TUI.
const HostTrustWarning = "experimental host-trusted reviewer: Vibe exposes a shell, external editor, and approval-mode changes without OS isolation"

var (
	requiredFlags               = []string{"--agent", "--workdir", "--trust"}
	safeID                      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	uncontainedInteractiveRisks = []string{
		"terminal shell escape",
		"external editor",
		"approval-mode toggle",
		"layered config discovery",
	}
)

// stagedCommandSpec records requirements the current runtime contract cannot
// yet express. It must not cross the production Reviewer boundary until the
// launcher can enforce every field rather than treating Env as an overlay.
type stagedCommandSpec struct {
	Command                ports.ReviewCommandSpec
	EnvironmentReplacement bool
	BlockedInput           []string
	UncontainedRisks       []string
}

// Reviewer describes Vibe's host-trusted interactive lifecycle. Function
// fields keep compatibility tests independent of an installed or authenticated
// Vibe binary; dataDir remains for the future contained-launch contract.
type Reviewer struct {
	dataDir       string
	resolveBinary func(context.Context) (string, error)
	run           func(context.Context, map[string]string, string, ...string) ([]byte, error)
}

// New returns the experimental host-trusted adapter.
func New() *Reviewer {
	return &Reviewer{
		resolveBinary: agentvibe.ResolveVibeBinary,
		run: func(ctx context.Context, env map[string]string, binary string, args ...string) ([]byte, error) {
			cmd := aoprocess.CommandContext(ctx, binary, args...)
			cmd.Env = appendEnvironment(os.Environ(), env)
			return cmd.CombinedOutput()
		},
	}
}

// Harness identifies the Vibe reviewer.
func (*Reviewer) Harness() domain.ReviewerHarness { return HarnessID }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerReusePolicy = (*Reviewer)(nil)

// ReviewCommand launches Vibe's persistent plan-agent TUI over the worker
// checkout. The profile and terminal escape surfaces remain host-trusted.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	if strings.TrimSpace(inv.TaskPromptRoot) == "" {
		return ports.ReviewCommandSpec{Argv: []string{binary}}, nil
	}
	if !filepath.IsAbs(binary) {
		return ports.ReviewCommandSpec{}, errors.New("vibe reviewer: resolved binary must be absolute")
	}
	env, err := reviewgateway.PrepareHostTrustedEnvironment(inv.DataDir, inv.ReviewerID)
	if err != nil {
		return ports.ReviewCommandSpec{}, fmt.Errorf("vibe reviewer: prepare AO-owned profile: %w", err)
	}
	envVars := env.TUIEnvironment()
	envVars["AO_DATA_DIR"] = env.DataDir
	envVars["VIBE_HOME"] = env.ConfigRoot
	if err := seedHostVibeProfile(env.ConfigRoot); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	if err := r.verifyCompatibility(ctx, binary, envVars); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	argv := []string{binary, "--trust"}
	if strings.TrimSpace(inv.WorkspacePath) != "" {
		argv = append(argv, "--workdir", inv.WorkspacePath)
	}
	if strings.TrimSpace(inv.TaskPromptRoot) != "" {
		argv = append(argv, "--add-dir", inv.TaskPromptRoot)
	}
	argv = append(argv, "--agent", reviewerAgent)
	if strings.TrimSpace(inv.Prompt) != "" {
		argv = append(argv, inv.Prompt)
	}
	return ports.ReviewCommandSpec{Argv: argv, Env: envVars, WorkingDirectory: inv.WorkspacePath}, nil
}

// ReviewRestoreCommand restores a recorded Vibe reviewer pane by relaunching
// with the AO-owned profile and current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewProcessReusable returns false because Vibe reviewer tasks are supplied
// as the launch-time prompt. A later task must start a fresh process instead of
// relying on typed injection into an old TUI.
func (*Reviewer) ReviewProcessReusable() bool { return false }

// ReviewPreflight verifies that the Vibe executable is available. The pinned
// CLI probe runs later with AO-owned state roots in ReviewCommand.
func (r *Reviewer) ReviewPreflight(ctx context.Context, _ string) error {
	_, err := r.resolveBinary(ctx)
	return err
}

func (r *Reviewer) verifyCompatibility(ctx context.Context, binary string, env map[string]string) error {
	version, err := r.run(ctx, env, binary, "--version")
	if err != nil {
		return fmt.Errorf("run vibe --version: %w", err)
	}
	if !isSupportedVersion(string(version)) {
		return fmt.Errorf("installed Vibe %q is incompatible: version %s or newer is required", strings.TrimSpace(string(version)), pinnedVersion)
	}
	help, err := r.run(ctx, env, binary, "--help")
	if err != nil {
		return fmt.Errorf("run vibe --help: %w", err)
	}
	for _, flag := range requiredFlags {
		if !strings.Contains(string(help), flag) {
			return fmt.Errorf("installed Vibe %s is incompatible: required flag %s is unavailable", pinnedVersion, flag)
		}
	}
	return nil
}

func seedHostVibeProfile(vibeHome string) error {
	if strings.TrimSpace(vibeHome) == "" {
		return nil
	}
	srcRoot := hostVibeHome()
	if srcRoot == "" || srcRoot == vibeHome {
		return nil
	}
	for _, name := range []string{"config.toml", ".env"} {
		src := filepath.Join(srcRoot, name)
		data, err := os.ReadFile(src)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("vibe reviewer: read host profile %s: %w", name, err)
		}
		if err := os.MkdirAll(vibeHome, 0o700); err != nil {
			return fmt.Errorf("vibe reviewer: create private profile: %w", err)
		}
		if err := os.WriteFile(filepath.Join(vibeHome, name), data, 0o600); err != nil {
			return fmt.Errorf("vibe reviewer: write private profile %s: %w", name, err)
		}
	}
	return nil
}

func hostVibeHome() string {
	if value := strings.TrimSpace(os.Getenv("VIBE_HOME")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".vibe")
}

func appendEnvironment(base []string, overrides map[string]string) []string {
	result := append([]string(nil), base...)
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

// ReviewMessage reuses the live TUI and injects only AO's opaque task
// reference; the task body never enters argv or inherited process state.
func (*Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel uses Vibe's native one-Escape interrupt binding.
func (*Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInput, Interrupts: 1, Input: "\x1b"}, nil
}

// containedInteractiveSpec models the only future launch shape AO may use.
// The initial task is deliberately delivered through InitialMessage after the
// TUI is ready, never as Vibe's positional prompt or programmatic -p input.
func (r *Reviewer) containedInteractiveSpec(ctx context.Context, inv ports.ReviewInvocation) (stagedCommandSpec, error) {
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return stagedCommandSpec{}, err
	}
	if !filepath.IsAbs(binary) {
		return stagedCommandSpec{}, errors.New("vibe reviewer: resolved binary must be absolute")
	}
	workingDir, env, err := r.prepareNeutralEnvironment(inv.ReviewerID)
	if err != nil {
		return stagedCommandSpec{}, err
	}
	return stagedCommandSpec{
		Command: ports.ReviewCommandSpec{
			Argv:             []string{binary, "--trust", "--workdir", workingDir, "--agent", reviewerAgent},
			Env:              env,
			InitialMessage:   inv.Prompt,
			WorkingDirectory: workingDir,
		},
		EnvironmentReplacement: true,
		// Ctrl+G launches $VISUAL/$EDITOR (or a platform fallback) outside
		// Vibe's tool permissions. The future runtime must swallow it.
		BlockedInput:     []string{"\x07"},
		UncontainedRisks: append([]string(nil), uncontainedInteractiveRisks...),
	}, nil
}

func (r *Reviewer) prepareNeutralEnvironment(reviewerID string) (string, map[string]string, error) {
	if strings.TrimSpace(r.dataDir) == "" || !filepath.IsAbs(r.dataDir) {
		return "", nil, errors.New("vibe reviewer: absolute AO data directory is required")
	}
	if !safeID.MatchString(reviewerID) {
		return "", nil, errors.New("vibe reviewer: invalid reviewer id")
	}
	root := filepath.Join(r.dataDir, "reviewer-runtime", reviewerID, "vibe")
	workingDir := filepath.Join(root, "workspace")
	vibeHome := filepath.Join(root, "home")
	configRoot := filepath.Join(root, "config")
	stateRoot := filepath.Join(root, "state")
	cacheRoot := filepath.Join(root, "cache")
	tempRoot := filepath.Join(root, "tmp")
	for _, dir := range []string{root, workingDir, vibeHome, configRoot, stateRoot, cacheRoot, tempRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", nil, fmt.Errorf("vibe reviewer: create neutral directory: %w", err)
		}
	}
	// Lock the TUI to plan so Shift+Tab cannot reach accept-edits or
	// auto-approve. Restrict built-ins to read-only local tools; the eventual
	// review operations must be supplied by the still-missing gateway MCP.
	config := strings.Join([]string{
		`default_agent = "plan"`,
		`enabled_agents = ["plan"]`,
		`enabled_tools = ["grep", "read"]`,
		`disabled_skills = ["*"]`,
		`mcp_servers = []`,
		`enable_auto_update = false`,
		`enable_telemetry = false`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(vibeHome, "config.toml"), []byte(config), 0o600); err != nil {
		return "", nil, fmt.Errorf("vibe reviewer: write neutral config: %w", err)
	}
	env := map[string]string{
		"HOME":            root,
		"VIBE_HOME":       vibeHome,
		"XDG_CONFIG_HOME": configRoot,
		"XDG_STATE_HOME":  stateRoot,
		"XDG_CACHE_HOME":  cacheRoot,
		"TMPDIR":          tempRoot,
		"TEMP":            tempRoot,
		"TMP":             tempRoot,
	}
	return workingDir, env, nil
}

func isSupportedVersion(output string) bool {
	version := strings.TrimPrefix(strings.TrimSpace(output), "vibe ")
	got, ok := parseVersion(version)
	if !ok {
		return false
	}
	minimum, ok := parseVersion(pinnedVersion)
	if !ok {
		return false
	}
	for i := range minimum {
		if got[i] > minimum[i] {
			return true
		}
		if got[i] < minimum[i] {
			return false
		}
	}
	return true
}

func parseVersion(version string) ([3]int, bool) {
	var result [3]int
	parts := strings.Split(version, ".")
	if len(parts) != len(result) {
		return result, false
	}
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return result, false
		}
		result[i] = value
	}
	return result, true
}
