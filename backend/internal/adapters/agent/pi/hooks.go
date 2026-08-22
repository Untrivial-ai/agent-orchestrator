package pi

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
	piExtensionsDirName = "extensions"
	piExtensionFileName = "ao-activity.ts"
	piExtensionSentinel = "agent-orchestrator: managed pi activity extension"
	minPiSettledVersion = "0.80.6"
)

var piVersionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func piExtensionPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".pi", piExtensionsDirName, piExtensionFileName)
}

// GetAgentHooks installs AO's project-local Pi extension. Launch and restore
// pass it explicitly with --extension, so activity reporting does not depend on
// Pi's trust-gated project auto-discovery.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("pi.GetAgentHooks: WorkspacePath is required")
	}

	path := piExtensionPath(cfg.WorkspacePath)
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // caller-owned workspace path
		if !strings.Contains(string(data), piExtensionSentinel) {
			return fmt.Errorf("pi.GetAgentHooks: refusing to overwrite non-AO file at %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("pi.GetAgentHooks: read managed extension: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("pi.GetAgentHooks: create extension dir: %w", err)
	}
	settledSupported, err := p.piAgentSettledSupported(ctx)
	if err != nil {
		return err
	}
	if err := hookutil.AtomicWriteFile(path, []byte(piActivityExtensionSource(settledSupported)), 0o600); err != nil {
		return fmt.Errorf("pi.GetAgentHooks: write extension: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(path), piExtensionFileName); err != nil {
		return fmt.Errorf("pi.GetAgentHooks: gitignore: %w", err)
	}
	return nil
}

func appendPiExtensionFlag(cmd *[]string, workspacePath string) {
	if strings.TrimSpace(workspacePath) == "" {
		return
	}
	*cmd = append(*cmd, "--extension", piExtensionPath(workspacePath))
}

func (p *Plugin) piAgentSettledSupported(ctx context.Context) (bool, error) {
	binary, err := p.piBinary(ctx)
	if err != nil {
		return false, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, binary, "--version").CombinedOutput() //nolint:gosec // binary is adapter-resolved, args are static.
	if err != nil {
		return false, fmt.Errorf("pi.GetAgentHooks: probe pi --version: %w", err)
	}
	version, ok := parsePiVersion(string(out))
	if !ok {
		return false, fmt.Errorf("pi.GetAgentHooks: parse pi --version output %q", strings.TrimSpace(string(out)))
	}
	minimum, _ := parsePiVersion(minPiSettledVersion)
	return !version.less(minimum), nil
}

type piVersion [3]int

func parsePiVersion(output string) (piVersion, bool) {
	match := piVersionPattern.FindStringSubmatch(output)
	if match == nil {
		return piVersion{}, false
	}
	var version piVersion
	for i := range version {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return piVersion{}, false
		}
		version[i] = n
	}
	return version, true
}

func (v piVersion) less(other piVersion) bool {
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

func piActivityExtensionSource(settledSupported bool) string {
	return `// ` + piExtensionSentinel + ` (do not edit)
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { spawnSync } from "node:child_process";

const HOOK_TIMEOUT_MS = 5_000;
const AGENT_SETTLED_SUPPORTED = ` + strconv.FormatBool(settledSupported) + `;

function callHookSync(hookName: string, payload: Record<string, unknown>) {
  try {
    const result = spawnSync("ao", ["hooks", "pi", hookName], {
      input: JSON.stringify(payload) + "\n",
      encoding: "utf8",
      stdio: ["pipe", "ignore", "pipe"],
      timeout: HOOK_TIMEOUT_MS,
      windowsHide: true,
    });
    // AO's hook command records daemon delivery failures itself. A missing AO
    // executable or timeout is deliberately ignored so Pi remains usable.
    void result;
  } catch {
    // Activity reporting is best-effort and must never interrupt Pi.
  }
}

function sessionID(ctx: any): string {
  return ctx.sessionManager.getSessionId() ?? "";
}

export default function (pi: ExtensionAPI) {
  pi.on("session_start", async (_event, ctx) => {
    callHookSync("session-start", { session_id: sessionID(ctx) });
  });
  pi.on("before_agent_start", async (event, ctx) => {
    callHookSync("user-prompt-submit", { session_id: sessionID(ctx), prompt: event.prompt });
  });
  // agent_end is the completion event in Pi 0.80.x. Newer releases may retry,
  // compact, or queue follow-up work after it; a subsequent start immediately
  // reactivates AO, while agent_settled below confirms the final idle state.
  pi.on("agent_end", async (_event, ctx) => {
	    if (!AGENT_SETTLED_SUPPORTED) callHookSync("stop", { session_id: sessionID(ctx) });
  });
  pi.on("agent_settled", async (_event, ctx) => {
	    if (AGENT_SETTLED_SUPPORTED) callHookSync("stop", { session_id: sessionID(ctx) });
  });
  pi.on("session_shutdown", async (event, ctx) => {
	    if (event.reason === "quit") {
	      callHookSync("session-end", { session_id: sessionID(ctx), reason: event.reason });
	    }
  });
}
`
}
