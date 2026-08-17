// Package goose defines AO's experimental host-trusted Goose reviewer.
package goose

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	workergoose "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/goose"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/internal/reviewgateway"
)

const (
	pinnedVersion     = "1.38.0"
	gooseBinary       = "/opt/ao/bin/goose"
	gatewayMCPBinary  = "/opt/ao/bin/ao-review-gateway-mcp"
	containedRoot     = "/opt/ao/reviewer"
	neutralWorkingDir = containedRoot + "/work"
	isolatedHome      = containedRoot + "/home"
	isolatedGooseRoot = containedRoot + "/goose"
	isolatedTemp      = containedRoot + "/tmp"
	systemPromptPath  = containedRoot + "/prompts/system.md"
	modelBrokerHost   = "http://ao-review-model-broker/v1"

	// This is the SHA-256 of the exact stdout emitted by the official Goose
	// 1.38.0 binary for `goose session --help`. Version equality alone is not
	// enough: a rebuilt binary with changed flags or semantics must fail closed.
	pinnedSessionHelpSHA256 = "7da45db07c6fd73d17f7cf4d6267f345ece2c033bb6a212fd54dd6738d9fbbf2"
)

// HarnessID identifies the Goose reviewer adapter.
const HarnessID domain.ReviewerHarness = "goose"

// HostTrustWarning documents the authority retained by Goose's interactive TUI.
const HostTrustWarning = "experimental host-trusted reviewer: Goose can use developer tools and the network without OS or network isolation"

// Reviewer records Goose's pinned compatibility and TUI lifecycle contracts.
// The injected runner keeps preflight tests independent of a Goose install.
type Reviewer struct {
	resolveBinary func(context.Context) (string, error)
	run           func(context.Context, map[string]string, string, ...string) ([]byte, error)
}

// New returns the experimental host-trusted adapter.
func New() *Reviewer {
	return &Reviewer{
		resolveBinary: workergoose.ResolveGooseBinary,
		run: func(ctx context.Context, env map[string]string, binary string, args ...string) ([]byte, error) {
			cmd := aoprocess.CommandContext(ctx, binary, args...)
			cmd.Env = appendEnvironment(os.Environ(), env)
			return cmd.CombinedOutput()
		},
	}
}

// Harness identifies the Goose reviewer.
func (*Reviewer) Harness() domain.ReviewerHarness { return HarnessID }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerReusePolicy = (*Reviewer)(nil)

// ReviewCommand launches Goose's normal interactive run mode with AO-owned CLI
// state. The first review task is passed as Goose's native instructions file so
// AO never has to type a backtick-wrapped task path into a pane that may have
// fallen back to the user's shell. Host tools, inherited credentials, and
// network remain host-trusted.
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
	if strings.TrimSpace(inv.TaskPromptFile) == "" {
		return ports.ReviewCommandSpec{}, errors.New("goose reviewer: task prompt file is required")
	}
	if !filepath.IsAbs(binary) {
		return ports.ReviewCommandSpec{}, errors.New("goose reviewer: resolved binary must be absolute")
	}
	profile, err := reviewgateway.PrepareHostTrustedEnvironment(inv.DataDir, inv.ReviewerID)
	if err != nil {
		return ports.ReviewCommandSpec{}, fmt.Errorf("goose reviewer: prepare AO-owned profile: %w", err)
	}
	env := hostGooseEnvironment(profile)
	if strings.TrimSpace(os.Getenv("ZHIPU_API_KEY")) == "" {
		if value := strings.TrimSpace(os.Getenv("ZHIPUAI_API_KEY")); value != "" {
			env["ZHIPU_API_KEY"] = value
		}
	}
	for key, value := range map[string]string{
		"CONTEXT_FILE_NAMES":           "[]",
		"GOOSE_DISABLE_SESSION_NAMING": "true",
		"GOOSE_MODE":                   "smart_approve",
		"GOOSE_TELEMETRY_OFF":          "1",
	} {
		env[key] = value
	}
	if strings.TrimSpace(inv.SystemPromptFile) != "" {
		env["GOOSE_SYSTEM_PROMPT_FILE_PATH"] = inv.SystemPromptFile
	}
	if err := r.verifyCompatibility(ctx, binary, env); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{
		Argv:             []string{binary, "run", "--instructions", inv.TaskPromptFile, "--interactive"},
		Env:              env,
		WorkingDirectory: inv.WorkspacePath,
	}, nil
}

// ReviewRestoreCommand restores a recorded Goose reviewer pane by relaunching
// with the AO-owned profile and current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewProcessReusable returns false because Goose reviewer tasks are supplied
// as launch-time instruction files. A new review task needs a fresh process
// with a fresh immutable task file, not a message typed into an old pane.
func (*Reviewer) ReviewProcessReusable() bool { return false }

// ReviewPreflight verifies that the Goose executable is available. The pinned
// CLI probe runs later with AO-owned state roots in ReviewCommand.
func (r *Reviewer) ReviewPreflight(ctx context.Context, _ string) error {
	_, err := r.resolveBinary(ctx)
	return err
}

func (r *Reviewer) verifyCompatibility(ctx context.Context, binary string, env map[string]string) error {
	version, err := r.run(ctx, env, binary, "--version")
	if err != nil {
		return fmt.Errorf("run goose --version: %w", err)
	}
	if got := strings.TrimSpace(string(version)); got != pinnedVersion {
		return fmt.Errorf("installed Goose %q is incompatible: exactly version %s is required", got, pinnedVersion)
	}

	help, err := r.run(ctx, env, binary, "session", "--help")
	if err != nil {
		return fmt.Errorf("run goose session --help: %w", err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(help)); got != pinnedSessionHelpSHA256 {
		return fmt.Errorf("installed Goose %s is incompatible: session help contract drifted (sha256 %s)", pinnedVersion, got)
	}
	return nil
}

func appendEnvironment(base []string, overrides map[string]string) []string {
	result := append([]string(nil), base...)
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

// ReviewMessage keeps subsequent reviews in the already-running TUI. AO's
// launcher supplies only a short reference to an immutable task file here.
func (*Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel sends exactly one Ctrl-C. Goose treats it as cancellation of an
// active turn; AO must not send a second interrupt that could exit the TUI.
func (*Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 1}, nil
}

func hostGooseEnvironment(profile reviewgateway.Environment) map[string]string {
	env := map[string]string{
		"AO_DATA_DIR": profile.DataDir,
		"TMPDIR":      profile.TempRoot,
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		env["HOME"] = home
		setDefaultEnv(env, "XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		setDefaultEnv(env, "XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
		setDefaultEnv(env, "XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
		setDefaultEnv(env, "XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			env[key] = value
		}
	}
	return env
}

func setDefaultEnv(env map[string]string, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	if _, ok := env[key]; !ok {
		env[key] = value
	}
}

// containedProcessSpec is the future OCI supervisor contract. Environment is
// the complete child environment, not values to merge into os.Environ.
type containedProcessSpec struct {
	Argv                 []string
	Environment          []string
	WorkingDirectory     string
	ReadOnlyFiles        []string
	InitialMessage       string
	InjectAfterReadiness bool
}

// containedCommand models the sole launch shape an eventual OCI supervisor may
// accept. It has no resume form: a dead Goose reviewer starts a new session.
func containedCommand(initialMessage string) containedProcessSpec {
	return containedProcessSpec{
		Argv: []string{
			gooseBinary,
			"session",
			"--no-profile",
			"--with-extension", gatewayMCPBinary,
		},
		Environment: []string{
			"CONTEXT_FILE_NAMES=[]",
			"GOOSE_DISABLE_KEYRING=1",
			"GOOSE_DISABLE_SESSION_NAMING=true",
			"GOOSE_MODEL=ao-reviewer",
			"GOOSE_MODE=auto",
			"GOOSE_PATH_ROOT=" + isolatedGooseRoot,
			"GOOSE_PROVIDER=openai",
			"GOOSE_SYSTEM_PROMPT_FILE_PATH=" + systemPromptPath,
			"GOOSE_TELEMETRY_OFF=1",
			"HOME=" + isolatedHome,
			"OPENAI_BASE_URL=" + modelBrokerHost,
			"PATH=/opt/ao/bin:/usr/bin:/bin",
			"TERM=xterm-256color",
			"TMPDIR=" + isolatedTemp,
		},
		WorkingDirectory:     neutralWorkingDir,
		ReadOnlyFiles:        []string{systemPromptPath},
		InitialMessage:       initialMessage,
		InjectAfterReadiness: true,
	}
}
