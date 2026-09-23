package amp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	ampPluginDirName  = ".amp"
	ampPluginSubDir   = "plugins"
	ampPluginFileName = "ao-system-prompt.ts"
	ampPluginSentinel = "agent-orchestrator: managed amp system prompt plugin"
)

// GetAgentHooks installs AO's Amp integration plugin into the worktree-local
// .amp/plugins directory. It injects hidden standing instructions at turn start
// and forwards Amp's thread lifecycle to AO. AO owns only ao-system-prompt.ts;
// other user plugin files are preserved.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("amp.GetAgentHooks: WorkspacePath is required")
	}

	pluginPath := ampPluginPath(cfg.WorkspacePath)
	if _, err := os.Stat(pluginPath); err == nil {
		managed, err := isAOManagedAmpPlugin(pluginPath)
		if err != nil {
			return fmt.Errorf("amp.GetAgentHooks: %w", err)
		}
		if !managed {
			return fmt.Errorf("amp.GetAgentHooks: refusing to overwrite non-AO file at %s", pluginPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("amp.GetAgentHooks: stat plugin: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o750); err != nil {
		return fmt.Errorf("amp.GetAgentHooks: create plugin dir: %w", err)
	}
	source := ampSystemPromptPluginSource(cfg.SystemPrompt, cfg.SystemPromptFile)
	if err := hookutil.AtomicWriteFile(pluginPath, []byte(source), 0o600); err != nil {
		return fmt.Errorf("amp.GetAgentHooks: write plugin: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(pluginPath), ampPluginFileName); err != nil {
		return fmt.Errorf("amp.GetAgentHooks: gitignore: %w", err)
	}
	return nil
}

func ampPluginPath(workspacePath string) string {
	return filepath.Join(workspacePath, ampPluginDirName, ampPluginSubDir, ampPluginFileName)
}

func isAOManagedAmpPlugin(path string) (bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path built from caller-owned workspace dir
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read plugin: %w", err)
	}
	return strings.Contains(string(data), ampPluginSentinel), nil
}

func ampSystemPromptPluginSource(inline, file string) string {
	file = strings.TrimSpace(file)
	inline = strings.TrimRight(inline, "\n")

	var b strings.Builder
	b.WriteString("// ")
	b.WriteString(ampPluginSentinel)
	b.WriteString("\n")
	b.WriteString("import type { PluginAPI } from \"@ampcode/plugin\";\n")
	b.WriteString("import { readFile } from \"node:fs/promises\";\n\n")
	b.WriteString("const systemPromptFile = ")
	fmt.Fprintf(&b, "%q", file)
	b.WriteString(";\n")
	b.WriteString("const inlineSystemPrompt = ")
	if file == "" {
		fmt.Fprintf(&b, "%q", inline)
	} else {
		b.WriteString("\"\"")
	}
	b.WriteString(";\n\n")
	b.WriteString("const HOOK_TIMEOUT_MS = 5_000;\n")
	b.WriteString("const threadSubscriptions = new Map<string, { unsubscribe(): void }>();\n\n")
	b.WriteString("let activeThreadID = \"\";\n\n")
	b.WriteString("function reportHookFailure(amp: any, hookName: string, detail: string) {\n")
	b.WriteString("  try { amp.logger.log(`AO activity hook ${hookName} failed`, { detail }); } catch {}\n")
	b.WriteString("}\n\n")
	b.WriteString("function callHookSync(amp: any, hookName: string, payload: Record<string, unknown>) {\n")
	b.WriteString("  try {\n")
	b.WriteString("    const executable = Bun.which(\"ao\");\n")
	b.WriteString("    if (!executable) return;\n")
	b.WriteString("    const result = Bun.spawnSync([executable, \"hooks\", \"amp\", hookName], {\n")
	b.WriteString("      stdin: new TextEncoder().encode(JSON.stringify(payload) + \"\\n\"),\n")
	b.WriteString("      stdout: \"ignore\",\n")
	b.WriteString("      stderr: \"pipe\",\n")
	b.WriteString("      timeout: HOOK_TIMEOUT_MS,\n")
	b.WriteString("    });\n")
	b.WriteString("    if (!result.success) {\n")
	b.WriteString("      const detail = result.stderr ? new TextDecoder().decode(result.stderr).trim() : `exit ${result.exitCode}`;\n")
	b.WriteString("      reportHookFailure(amp, hookName, detail);\n")
	b.WriteString("    }\n")
	b.WriteString("  } catch (error) {\n")
	b.WriteString("    reportHookFailure(amp, hookName, error instanceof Error ? error.message : String(error));\n")
	b.WriteString("  }\n")
	b.WriteString("}\n\n")
	b.WriteString("function reportThreadState(amp: any, sessionID: string, state: string) {\n")
	b.WriteString("  if (amp.activeThread.current?.id !== sessionID || sessionID !== activeThreadID) return;\n")
	b.WriteString("  callHookSync(amp, \"thread-state\", { session_id: sessionID, state });\n")
	b.WriteString("}\n\n")
	b.WriteString("function observeThread(amp: any, thread: any) {\n")
	b.WriteString("  if (amp.activeThread.current?.id !== thread.id) return;\n")
	b.WriteString("  if (activeThreadID === thread.id) return;\n")
	b.WriteString("  if (activeThreadID) {\n")
	b.WriteString("    threadSubscriptions.get(activeThreadID)?.unsubscribe();\n")
	b.WriteString("    threadSubscriptions.delete(activeThreadID);\n")
	b.WriteString("  }\n")
	b.WriteString("  activeThreadID = thread.id;\n")
	b.WriteString("  const subscription = thread.state.subscribe((state: string) => reportThreadState(amp, thread.id, state));\n")
	b.WriteString("  threadSubscriptions.set(thread.id, subscription);\n")
	b.WriteString("  void thread.state.get().then((state: string) => reportThreadState(amp, thread.id, state)).catch((error: unknown) => {\n")
	b.WriteString("    reportHookFailure(amp, \"thread-state\", error instanceof Error ? error.message : String(error));\n")
	b.WriteString("  });\n")
	b.WriteString("}\n\n")
	b.WriteString("async function loadSystemPrompt(amp: any): Promise<string> {\n")
	b.WriteString("  if (systemPromptFile) {\n")
	b.WriteString("    try {\n")
	b.WriteString("      const content = await readFile(systemPromptFile, \"utf8\");\n")
	b.WriteString("      const trimmed = content.trim();\n")
	b.WriteString("      if (trimmed) return trimmed;\n")
	b.WriteString("      amp.logger.log(\"AO system prompt file is empty\", { systemPromptFile });\n")
	b.WriteString("    } catch (error) {\n")
	b.WriteString("      amp.logger.log(\"AO system prompt file is unavailable\", { systemPromptFile, error });\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("  return inlineSystemPrompt.trim();\n")
	b.WriteString("}\n\n")
	b.WriteString("export default function (amp: PluginAPI) {\n")
	b.WriteString("  amp.on(\"session.start\", async (event, ctx) => {\n")
	b.WriteString("    observeThread(amp, ctx.thread);\n")
	b.WriteString("    if (event.thread.id === amp.activeThread.current?.id) callHookSync(amp, \"session-start\", { session_id: event.thread.id });\n")
	b.WriteString("  });\n")
	b.WriteString("  amp.on(\"agent.start\", async (event, ctx) => {\n")
	b.WriteString("    observeThread(amp, ctx.thread);\n")
	b.WriteString("    if (event.thread.id !== amp.activeThread.current?.id) return {};\n")
	b.WriteString("    callHookSync(amp, \"user-prompt-submit\", { session_id: event.thread.id, prompt: event.message });\n")
	b.WriteString("    const systemPrompt = await loadSystemPrompt(amp);\n")
	b.WriteString("    if (!systemPrompt) return {};\n")
	b.WriteString("    return { message: { content: systemPrompt, display: false } };\n")
	b.WriteString("  });\n")
	b.WriteString("  amp.on(\"agent.end\", async (event) => {\n")
	b.WriteString("    if (event.thread.id === amp.activeThread.current?.id && event.thread.id === activeThreadID) callHookSync(amp, \"stop\", { session_id: event.thread.id, status: event.status });\n")
	b.WriteString("  });\n")
	b.WriteString("}\n")
	return b.String()
}
