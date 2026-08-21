package claudeacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const planUsageTimeout = 20 * time.Second

// QuotaRefresher reads Claude subscription limits without creating an AO
// session, worktree, or visible conversation. The child deliberately ignores
// API-key routing so Claude Code can use the user's existing claude.ai login.
type QuotaRefresher struct{ plugin claudePlugin }

// NewQuotaRefresher creates the daemon-owned Claude plan usage reader.
func NewQuotaRefresher(plugin claudePlugin) *QuotaRefresher {
	return &QuotaRefresher{plugin: plugin}
}

// RefreshQuota implements quota.Refresher for Claude's default local account.
func (r *QuotaRefresher) RefreshQuota(ctx context.Context, provider domain.QuotaProviderID, accountID domain.QuotaAccountID) (domain.QuotaSnapshot, error) {
	if r == nil || r.plugin == nil || provider != "claude" || accountID != "default" {
		return domain.QuotaSnapshot{}, ports.ErrQuotaRefreshUnsupported
	}
	runtimeLaunch, err := resolvePlanUsageRuntime(ctx)
	if err != nil {
		return domain.QuotaSnapshot{}, err
	}
	claudeBinary, err := r.plugin.ResolveBinary(ctx)
	if err != nil {
		return domain.QuotaSnapshot{}, fmt.Errorf("resolve Claude Code: %w", err)
	}

	readCtx, cancel := context.WithTimeout(ctx, planUsageTimeout)
	defer cancel()
	cmd := aoprocess.CommandContext(readCtx, runtimeLaunch.command, runtimeLaunch.args...) //nolint:gosec // Packaged runtime and fixed entrypoint.
	cmd.Env = claudeSubscriptionEnv(os.Environ(), claudeBinary)
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return domain.QuotaSnapshot{}, fmt.Errorf("claude plan usage read timed out: %w", readCtx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := strings.TrimSpace(string(exitErr.Stderr))
			if message != "" {
				return domain.QuotaSnapshot{}, fmt.Errorf("claude plan usage helper failed: %s", message)
			}
		}
		return domain.QuotaSnapshot{}, fmt.Errorf("run Claude plan usage helper: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return domain.QuotaSnapshot{}, fmt.Errorf("decode Claude plan usage: %w", err)
	}
	limits := acpdriver.NormalizeClaudePlanUsage(raw)
	if limits == nil || limits.Quota == nil {
		return domain.QuotaSnapshot{}, errors.New("claude plan usage helper returned no quota snapshot")
	}
	return domain.NormalizeQuotaSnapshot(*limits.Quota), nil
}

func resolvePlanUsageRuntime(ctx context.Context) (runtimeLaunch, error) {
	launch, err := resolveRuntime(ctx)
	if err != nil {
		return runtimeLaunch{}, err
	}
	if len(launch.args) == 0 {
		return runtimeLaunch{}, errors.New("AO_CLAUDE_ACP_COMMAND does not expose the packaged plan usage helper")
	}
	runtimeDir := strings.TrimSpace(os.Getenv("AO_ACP_RUNTIME_DIR"))
	if runtimeDir == "" {
		runtimeDir = runtimeDirectoryBesideExecutable()
	}
	entry := filepath.Join(runtimeDir, "ao-claude-plan-usage.mjs")
	if err := requireFile(entry, "AO Claude plan usage entrypoint"); err != nil {
		return runtimeLaunch{}, err
	}
	launch.args = []string{entry}
	return launch, nil
}

func claudeSubscriptionEnv(base []string, claudeBinary string) []string {
	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if key == "ANTHROPIC_API_KEY" || key == "ANTHROPIC_AUTH_TOKEN" || key == "CLAUDE_CODE_EXECUTABLE" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "CLAUDE_CODE_EXECUTABLE="+claudeBinary)
}
