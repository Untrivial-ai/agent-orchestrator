// Package kimi adapts Kimi Code CLI as an experimental host-trusted AO
// reviewer. It always runs Kimi's visible, long-lived interactive TUI.
//
// Kimi's CLI has no read-only sandbox flag, no tool allow/deny list, and no
// permission-policy injection point (unlike claude-code's AllowedTools, codex's
// --sandbox read-only, or opencode's OPENCODE_CONFIG_CONTENT). The only launch
// flags it exposes are approval-mode flags (--auto, -y) that make the model
// more autonomous, not more restricted, so none of those three mechanisms
// apply here. AO instead applies the same AO-owned isolation the
// reviewgateway package already gives Kimi's fellow host-trusted reviewers
// (agy, continueagent, goose, qwen, vibe): Kimi's profile, config, credentials,
// and hooks are redirected to a private root under AO_DATA_DIR instead of the
// user's real ~/.kimi-code. This keeps a shell escape or config change inside
// the reviewer pane from reading or overwriting the user's real Kimi profile.
// It does not make the checkout itself read-only; see HostTrustWarning.
package kimi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	workerkimi "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kimi"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/reviewgateway"
)

const skillsDirectoryName = "kimi-reviewer-skills"

// kimiCodeHomeEnv is Kimi CLI's documented environment variable for pointing
// the CLI at an alternate profile/config/credentials/hooks home. Mirrors the
// worker Kimi adapter's private kimiCodeHomeEnv constant
// (backend/internal/adapters/agent/kimi/kimi.go), which uses the same
// variable to keep worker sessions off the user's real Kimi profile.
const kimiCodeHomeEnv = "KIMI_CODE_HOME"

// kimiAPIKeyEnvVars are the environment variables Kimi CLI reads for API-key
// auth (mirrors backend/internal/adapters/agent/kimi/auth.go's
// kimiAPIKeyEnvVars). The reviewer launch replaces the process environment
// with an AO-owned one, so any of these present in the daemon's own
// environment must be forwarded explicitly or env-var auth would silently stop
// working once the profile is isolated.
var kimiAPIKeyEnvVars = []string{"KIMI_API_KEY", "KIMI_CODE_API_KEY", "MOONSHOT_API_KEY"}

// HostTrustWarning describes the authority retained by Kimi's interactive
// terminal. Plan mode limits Kimi to read-only tools, while auto mode handles
// approvals and questions without pausing for user input, and AO isolates
// Kimi's own profile/config/credentials/hooks to an AO-owned root (see the
// package doc) — but none of that is OS isolation or a tool allow/deny list: a
// terminal user can still invoke shell mode, change Plan mode, open an
// external editor, or alter configuration, and read/write the review checkout
// itself. AO deliberately does not replace the TUI with print/headless mode or
// place it in a sandbox.
const HostTrustWarning = "experimental host-trusted reviewer: Kimi has no OS isolation; terminal users can invoke shell mode, change Plan mode, open an external editor, or alter configuration"

// Reviewer builds Kimi's persistent interactive reviewer command.
type Reviewer struct {
	resolveBinary func(context.Context) (string, error)
}

// New returns the production Kimi reviewer adapter.
func New() *Reviewer {
	return &Reviewer{resolveBinary: workerkimi.ResolveKimiBinary}
}

// Harness returns Kimi's reviewer identity.
func (*Reviewer) Harness() domain.ReviewerHarness { return domain.ReviewerKimi }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerPromptReadinessProvider = (*Reviewer)(nil)

// ReviewPromptReadinessHints reuses Kimi's worker-TUI prompt markers.
func (*Reviewer) ReviewPromptReadinessHints(ctx context.Context) (ports.PromptReadinessHints, error) {
	return workerkimi.New().PromptReadinessHints(ctx, ports.LaunchConfig{})
}

// ReviewCommand starts only Kimi's normal interactive TUI. The initial task is
// injected after the pane starts; --prompt, --print, --quiet, --command, ACP,
// Wire, AFK, YOLO, and sandbox wrappers are never emitted. Auto mode is paired
// with Plan mode so read-only reviewer tools run unattended. Kimi's own
// profile/config/credentials/hooks are redirected to an AO-owned root (see the
// package doc) instead of the user's real ~/.kimi-code so a shell escape or
// config change inside the pane cannot read or overwrite it; the checkout
// itself remains host-trusted because Kimi's CLI has no read-only mechanism.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	if !filepath.IsAbs(binary) {
		return ports.ReviewCommandSpec{}, errors.New("kimi reviewer: resolved binary must be absolute")
	}
	// Preflight builds the permanent interactive command before request-scoped
	// prompt paths exist.
	if strings.TrimSpace(inv.TaskPromptRoot) == "" {
		return ports.ReviewCommandSpec{Argv: []string{binary, "--plan", "--auto"}}, nil
	}
	if strings.TrimSpace(inv.SystemPromptFile) == "" {
		return ports.ReviewCommandSpec{}, errors.New("kimi reviewer: system prompt file is required")
	}
	skillsDir := filepath.Join(inv.TaskPromptRoot, skillsDirectoryName)
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		return ports.ReviewCommandSpec{}, fmt.Errorf("kimi reviewer: create empty skills directory: %w", err)
	}
	env, err := reviewgateway.PrepareHostTrustedEnvironment(inv.DataDir, inv.ReviewerID)
	if err != nil {
		return ports.ReviewCommandSpec{}, fmt.Errorf("kimi reviewer: prepare AO-owned profile: %w", err)
	}
	if err := seedHostKimiProfile(env.ConfigRoot); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	envVars := env.TUIEnvironment()
	envVars["AO_DATA_DIR"] = env.DataDir
	envVars[kimiCodeHomeEnv] = env.ConfigRoot
	for _, name := range kimiAPIKeyEnvVars {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			envVars[name] = value
		}
	}
	message := fmt.Sprintf(
		"Read and follow the AO reviewer role in `%s`, then %s",
		filepath.ToSlash(inv.SystemPromptFile),
		strings.TrimSpace(inv.Prompt),
	)
	return ports.ReviewCommandSpec{
		Argv:             []string{binary, "--plan", "--auto", "--skills-dir", skillsDir},
		Env:              envVars,
		InitialMessage:   message,
		WorkingDirectory: inv.WorkspacePath,
	}, nil
}

// seedHostKimiProfile copies the user's real Kimi config and credentials into
// the AO-owned private profile root so authentication keeps working once
// KIMI_CODE_HOME points away from the user's real ~/.kimi-code. It never
// writes back to the host profile, and it is a best-effort copy: a missing
// source file is not an error.
func seedHostKimiProfile(configRoot string) error {
	if strings.TrimSpace(configRoot) == "" {
		return nil
	}
	srcHome := hostKimiCodeHome()
	if srcHome == "" || sameKimiHomePath(srcHome, configRoot) {
		return nil
	}
	if err := copyHostKimiFile(filepath.Join(srcHome, "config.toml"), filepath.Join(configRoot, "config.toml")); err != nil {
		return err
	}
	credentialsName := filepath.Join("credentials", "kimi-code.json")
	if err := copyHostKimiFile(filepath.Join(srcHome, credentialsName), filepath.Join(configRoot, credentialsName)); err != nil {
		return err
	}
	return nil
}

// copyHostKimiFile copies one file from the user's real Kimi profile into the
// AO-owned private profile, preserving the destination directory structure. A
// missing source file is not an error since not every install has one.
func copyHostKimiFile(src, dst string) error {
	data, err := os.ReadFile(src) //nolint:gosec // src is derived from the resolved host Kimi profile directory.
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("kimi reviewer: read host profile %s: %w", filepath.Base(src), err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("kimi reviewer: create private profile directory: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("kimi reviewer: write private profile %s: %w", filepath.Base(dst), err)
	}
	return nil
}

// hostKimiCodeHome resolves the user's real Kimi profile directory, mirroring
// the worker Kimi adapter's private kimiCodeHome() lookup
// (backend/internal/adapters/agent/kimi/auth.go): the KIMI_CODE_HOME
// environment variable if set, otherwise ~/.kimi-code.
func hostKimiCodeHome() string {
	if value := strings.TrimSpace(os.Getenv(kimiCodeHomeEnv)); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".kimi-code")
}

func sameKimiHomePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return absA == absB
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// ReviewRestoreCommand restores a recorded Kimi reviewer pane by relaunching
// the reviewer command with the current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewMessage reuses the same Kimi TUI for subsequent review passes.
func (*Reviewer) ReviewMessage(ctx context.Context, inv ports.ReviewInvocation) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return inv.Prompt, nil
}

// ReviewCancel sends Kimi's Ctrl-C sequence twice so an active operation is
// interrupted and the reviewer cannot continue executing after cancellation.
func (*Reviewer) ReviewCancel(ctx context.Context) (ports.ReviewCancelSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ReviewCancelSpec{}, err
	}
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 2}, nil
}
