package omp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	ompExtensionsDirName  = "extensions"
	ompExtensionFileName  = "ao-activity.ts"
	ompExtensionSentinel  = "agent-orchestrator: managed omp activity extension"
	minOMPActivityVersion = "17.1.0"
)

var ompVersionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func ompExtensionPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".omp", ompExtensionsDirName, ompExtensionFileName)
}

// GetAgentHooks installs AO's project-local OMP activity extension. Launch and
// restore also pass this exact file explicitly, so status tracking does not
// depend on extension auto-discovery.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("omp.GetAgentHooks: WorkspacePath is required")
	}

	path := ompExtensionPath(cfg.WorkspacePath)
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // caller-owned workspace path
		if !strings.Contains(string(data), ompExtensionSentinel) {
			return fmt.Errorf("omp.GetAgentHooks: refusing to overwrite non-AO file at %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("omp.GetAgentHooks: read managed extension: %w", err)
	}
	if err := p.requireActivityContract(ctx); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("omp.GetAgentHooks: create extension dir: %w", err)
	}
	if err := hookutil.AtomicWriteFile(path, []byte(ompActivityExtensionSource()), 0o600); err != nil {
		return fmt.Errorf("omp.GetAgentHooks: write extension: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(path), ompExtensionFileName); err != nil {
		return fmt.Errorf("omp.GetAgentHooks: gitignore: %w", err)
	}
	return nil
}

func (p *Plugin) requireActivityContract(ctx context.Context) error {
	binary, err := p.ompBinary(ctx)
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, binary, "--version").CombinedOutput() //nolint:gosec // binary is adapter-resolved, args are static.
	if err != nil {
		return fmt.Errorf("omp.GetAgentHooks: probe omp --version: %w", err)
	}
	version, ok := parseOMPVersion(string(out))
	if !ok {
		return fmt.Errorf("omp.GetAgentHooks: parse omp --version output %q", strings.TrimSpace(string(out)))
	}
	minimum, _ := parseOMPVersion(minOMPActivityVersion)
	if version.less(minimum) {
		return fmt.Errorf("omp.GetAgentHooks: activity tracking requires OMP %s or newer (found %s)", minOMPActivityVersion, version)
	}
	return nil
}

type ompVersion [3]int

func parseOMPVersion(output string) (ompVersion, bool) {
	match := ompVersionPattern.FindStringSubmatch(output)
	if match == nil {
		return ompVersion{}, false
	}
	var version ompVersion
	for i := range version {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return ompVersion{}, false
		}
		version[i] = n
	}
	return version, true
}

func (v ompVersion) less(other ompVersion) bool {
	for i := range v {
		if v[i] < other[i] {
			return true
		}
		if v[i] > other[i] {
			return false
		}
	}
	return false
}

func (v ompVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

func appendOMPExtensionFlag(cmd *[]string, workspacePath string) {
	if strings.TrimSpace(workspacePath) == "" {
		return
	}
	*cmd = append(*cmd, "--extension", ompExtensionPath(workspacePath))
}

func ompActivityExtensionSource() string {
	return `// ` + ompExtensionSentinel + ` (do not edit)
import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import { spawnSync } from "node:child_process";

// OMP caps session_shutdown handlers at 2s. Keep the synchronous AO delivery
// comfortably below that budget so a hung hook cannot hold TUI teardown open.
const HOOK_TIMEOUT_MS = 1_250;

function callHookSync(hookName: string, payload: Record<string, unknown>) {
  try {
    const result = spawnSync("ao", ["hooks", "omp", hookName], {
      input: JSON.stringify(payload) + "\n",
      encoding: "utf8",
      stdio: ["pipe", "ignore", "pipe"],
      timeout: HOOK_TIMEOUT_MS,
      windowsHide: true,
    });
    // The hook command records daemon delivery failures itself. Reporting is
    // best-effort and must never interrupt the OMP session.
    void result;
  } catch {
    // A missing AO executable or timeout must not interrupt OMP.
  }
}

function sessionID(ctx: any): string {
  return ctx.sessionManager.getSessionId() ?? "";
}

function isRootSession(ctx: any): boolean {
  return ctx.hasUI === true;
}

export default function (omp: ExtensionAPI) {
  omp.on("session_start", async (_event, ctx) => {
    if (!isRootSession(ctx)) return;
    callHookSync("session-start", { session_id: sessionID(ctx) });
  });
  omp.on("session_switch", async (_event, ctx) => {
    if (!isRootSession(ctx)) return;
    callHookSync("session-start", { session_id: sessionID(ctx) });
  });
  omp.on("before_agent_start", async (event, ctx) => {
    if (!isRootSession(ctx)) return;
    callHookSync("user-prompt-submit", { session_id: sessionID(ctx), prompt: event.prompt });
  });
  omp.on("tool_approval_requested", async (event, ctx) => {
    if (!isRootSession(ctx)) return;
    callHookSync("permission-request", {
      session_id: sessionID(ctx),
      tool_name: event.toolName,
      tool_use_id: event.toolCallId,
    });
  });
  omp.on("tool_approval_resolved", async (event, ctx) => {
    if (!isRootSession(ctx)) return;
    callHookSync("permission-resolved", {
      session_id: sessionID(ctx),
      tool_name: event.toolName,
      tool_use_id: event.toolCallId,
      approved: event.approved,
    });
  });
  omp.on("agent_end", async (event, ctx) => {
    if (!isRootSession(ctx)) return;
    if (!event.willContinue) {
      callHookSync("stop", { session_id: sessionID(ctx) });
    }
  });
  omp.on("session_shutdown", async (_event, ctx) => {
    if (!isRootSession(ctx)) return;
    callHookSync("session-end", { session_id: sessionID(ctx) });
  });
}
`
}
