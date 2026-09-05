// Package qwen contains AO's experimental host-trusted Qwen Code reviewer.
// Qwen runs as a visible, long-lived TUI in an AO-owned neutral directory.
package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	workerqwen "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/qwen"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/reviewgateway"
)

// HostTrustWarning describes the security boundary operators accept by using
// this experimental reviewer. Yolo mode bypasses approval prompts and is not OS
// isolation; a terminal user can still invoke Qwen's ! shell or change mode.
const HostTrustWarning = "experimental host-trusted reviewer: Qwen yolo mode bypasses approval prompts and has no OS isolation; terminal users can invoke the ! shell"

type binaryResolver func(context.Context) (string, error)

// Reviewer builds only Qwen's visible, long-lived interactive TUI command.
type Reviewer struct {
	resolveBinary binaryResolver
}

// New creates the Qwen reviewer. Production launch invocations supply the
// AO-owned data directory used for its neutral working and configuration roots.
func New() *Reviewer {
	return &Reviewer{resolveBinary: workerqwen.ResolveQwenBinary}
}

// Harness returns Qwen's reviewer identity.
func (r *Reviewer) Harness() domain.ReviewerHarness { return domain.ReviewerQwen }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerReusePolicy = (*Reviewer)(nil)
var _ ports.ReviewerPromptReadinessProvider = (*Reviewer)(nil)

// ReviewCommand launches only Qwen's permanent interactive TUI. The task
// reference is injected after runtime creation so neither prompts nor prompt
// paths appear in process argv.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	if !filepath.IsAbs(binary) {
		return ports.ReviewCommandSpec{}, errors.New("qwen reviewer: resolved binary must be absolute")
	}
	// Launcher preflight runs before request-scoped prompt and gateway state
	// exists. It only needs the resolved interactive executable; production
	// launches always carry an absolute task prompt root.
	if strings.TrimSpace(inv.TaskPromptRoot) == "" {
		return ports.ReviewCommandSpec{Argv: []string{binary}}, nil
	}
	env, err := r.prepareEnvironment(inv)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	settingsEnv, err := seedHostQwenSettings(env.ConfigRoot)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	envVars := env.TUIEnvironment()
	for key, value := range settingsEnv {
		envVars[key] = value
	}
	envVars["AO_DATA_DIR"] = env.DataDir
	return ports.ReviewCommandSpec{
		Argv:             []string{binary, "--bare", "--approval-mode", "yolo"},
		Env:              envVars,
		InitialMessage:   inv.Prompt,
		WorkingDirectory: env.WorkingDirectory,
	}, nil
}

// ReviewRestoreCommand restores a recorded Qwen reviewer pane by relaunching
// with a fresh gateway manifest and the current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewMessage reuses AO's normal pane injection for subsequent passes.
func (*Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewProcessReusable returns false because Qwen's request-scoped reviewer
// context is fixed at launch.
func (*Reviewer) ReviewProcessReusable() bool { return false }

// ReviewPromptReadinessHints waits for Qwen's input footer before injecting the
// task reference, avoiding a blind startup race while keeping timeout best-effort.
func (*Reviewer) ReviewPromptReadinessHints(ctx context.Context) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{
		InitialDelay: 2 * time.Second,
		Patterns:     []string{"Type your message or @path/to/file"},
		PollInterval: 200 * time.Millisecond,
		Timeout:      15 * time.Second,
		Lines:        80,
	}, nil
}

// ReviewCancel matches the pinned Qwen TUI behavior: one Escape aborts the
// active turn without exiting the long-lived process. Ctrl-C is a quit action.
func (*Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInput, Input: "\x1b"}, nil
}

func (r *Reviewer) prepareEnvironment(inv ports.ReviewInvocation) (reviewgateway.Environment, error) {
	dataDir := inv.DataDir
	if strings.TrimSpace(dataDir) == "" {
		return reviewgateway.Environment{}, errors.New("qwen reviewer: AO data directory is required")
	}
	env, err := reviewgateway.PrepareHostTrustedEnvironment(dataDir, inv.ReviewerID)
	if err != nil {
		return reviewgateway.Environment{}, fmt.Errorf("qwen reviewer: prepare environment: %w", err)
	}
	return env, nil
}

func seedHostQwenSettings(configRoot string) (map[string]string, error) {
	if strings.TrimSpace(configRoot) == "" {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		home = strings.TrimSpace(home)
	}
	if home == "" {
		return nil, nil
	}
	src := filepath.Join(home, ".qwen", "settings.json")
	data, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("qwen reviewer: read host settings: %w", err)
	}
	dstDir := filepath.Join(configRoot, ".qwen")
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return nil, fmt.Errorf("qwen reviewer: create private settings dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "settings.json"), data, 0o600); err != nil {
		return nil, fmt.Errorf("qwen reviewer: write private settings: %w", err)
	}
	settingsEnv, err := qwenSettingsEnv(data)
	if err != nil {
		return nil, fmt.Errorf("qwen reviewer: parse host settings env: %w", err)
	}
	return settingsEnv, nil
}

func qwenSettingsEnv(data []byte) (map[string]string, error) {
	var root struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for key, value := range root.Env {
		if !validEnvName(key) || strings.TrimSpace(value) == "" {
			continue
		}
		out[key] = value
	}
	return out, nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
