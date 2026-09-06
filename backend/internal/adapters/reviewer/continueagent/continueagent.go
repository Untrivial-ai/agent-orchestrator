// Package continueagent defines AO's experimental host-trusted Continue reviewer.
package continueagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	workercontinue "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/continueagent"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/reviewgateway"
)

const (
	packageName      = "@continuedev/cli"
	packageVersion   = "1.5.47"
	packageIntegrity = "sha512-gtpewV3RoIOD9dyTtKIBi1SY0VOHRu3Ehe7C/mmnswm+j34MPyrcQhQaWj/m+jdfGO4fNIKdrgGIlLso1ULDFw=="

	configPath       = "/ao/config/continue/config.yaml"
	neutralDirectory = "/ao/empty"
)

// HarnessID identifies the Continue reviewer adapter.
const HarnessID domain.ReviewerHarness = "continue"

// HostTrustWarning documents that Continue readonly mode is advisory.
const HostTrustWarning = "experimental host-trusted reviewer: Continue readonly mode is not OS isolation and can be changed from the interactive TUI"

// Reviewer runs Continue's long-lived host-trusted TUI.
type Reviewer struct {
	resolveBinary func(context.Context) (string, error)
}

// New returns the experimental host-trusted adapter.
func New() *Reviewer { return &Reviewer{resolveBinary: workercontinue.ResolveContinueBinary} }

// Harness identifies the Continue reviewer.
func (*Reviewer) Harness() domain.ReviewerHarness { return HarnessID }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)

// ReviewCommand launches Continue in readonly mode and injects the task after
// readiness. CLI discovery and state are redirected to an AO-owned profile.
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
		return ports.ReviewCommandSpec{}, errors.New("continue reviewer: resolved binary must be absolute")
	}
	env, err := reviewgateway.PrepareHostTrustedEnvironment(inv.DataDir, inv.ReviewerID)
	if err != nil {
		return ports.ReviewCommandSpec{}, fmt.Errorf("continue reviewer: prepare AO-owned profile: %w", err)
	}
	if err := seedHostContinueConfig(env.ConfigRoot); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	envVars := env.TUIEnvironment()
	envVars["AO_DATA_DIR"] = env.DataDir
	envVars["CONTINUE_CLI_ENABLE_TELEMETRY"] = "0"
	if key := strings.TrimSpace(os.Getenv("CONTINUE_API_KEY")); key != "" {
		envVars["CONTINUE_API_KEY"] = key
	}
	return ports.ReviewCommandSpec{
		Argv:             []string{binary, "--readonly"},
		Env:              envVars,
		InitialMessage:   inv.Prompt,
		WorkingDirectory: inv.WorkspacePath,
	}, nil
}

func seedHostContinueConfig(configRoot string) error {
	if strings.TrimSpace(configRoot) == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		home = strings.TrimSpace(home)
	}
	if home == "" {
		return nil
	}
	src := filepath.Join(home, ".continue", "config.yaml")
	data, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("continue reviewer: read host config: %w", err)
	}
	dstDir := filepath.Join(configRoot, ".continue")
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return fmt.Errorf("continue reviewer: create private config dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "config.yaml"), data, 0o600); err != nil {
		return fmt.Errorf("continue reviewer: write private config: %w", err)
	}
	return nil
}

// ReviewRestoreCommand restores a recorded Continue reviewer pane by relaunching
// the reviewer command with the current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewPreflight verifies that the Continue executable is available.
func (r *Reviewer) ReviewPreflight(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := r.resolveBinary(ctx)
	return err
}

// ReviewMessage reuses AO's opaque task reference in the already-running TUI.
// It neither grants new authority through environment nor restores a prior
// Continue session.
func (*Reviewer) ReviewMessage(ctx context.Context, inv ports.ReviewInvocation) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return inv.Prompt, nil
}

// ReviewCancel sends exactly one Escape, Continue's active-turn cancellation
// key, while preserving the live pane for a later ReviewMessage.
func (*Reviewer) ReviewCancel(ctx context.Context) (ports.ReviewCancelSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ReviewCancelSpec{}, err
	}
	return ports.ReviewCancelSpec{
		Mode:       ports.ReviewCancelInput,
		Interrupts: 1,
		Input:      "\x1b",
	}, nil
}

// containedLaunch is adapter-local because the current runtime contract has no
// enforceable environment-replacement or containment fields. Returning its
// ReviewCommandSpec from production would therefore be unsafe.
type containedLaunch struct {
	Command              ports.ReviewCommandSpec
	ReplaceEnvironment   bool
	PostReadinessMessage string
	RestoreSupported     bool
}

// containedCommand fixes the future TUI argv, neutral cwd, replacement env,
// and post-readiness task delivery. No prompt or restore token is placed in
// argv. The container image will provide the audited npm artifact above.
func containedCommand(inv ports.ReviewInvocation) containedLaunch {
	return containedLaunch{
		Command: ports.ReviewCommandSpec{
			Argv:             []string{"cn", "--config", configPath, "--readonly"},
			Env:              replacementEnvironment(),
			WorkingDirectory: neutralDirectory,
		},
		ReplaceEnvironment:   true,
		PostReadinessMessage: inv.Prompt,
		RestoreSupported:     false,
	}
}

func replacementEnvironment() map[string]string {
	return map[string]string{
		"HOME":                          "/ao/home",
		"XDG_CONFIG_HOME":               "/ao/config",
		"XDG_STATE_HOME":                "/ao/state",
		"XDG_CACHE_HOME":                "/ao/cache",
		"TMPDIR":                        "/ao/tmp",
		"TMP":                           "/ao/tmp",
		"TEMP":                          "/ao/tmp",
		"PATH":                          "/usr/local/bin:/usr/bin:/bin",
		"CONTINUE_CLI_ENABLE_TELEMETRY": "0",
	}
}
