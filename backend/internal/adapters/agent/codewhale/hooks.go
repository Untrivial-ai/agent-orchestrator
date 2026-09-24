package codewhale

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

const (
	rulesDirName             = "rules"
	standingInstructionsName = "ao-agent-orchestrator.md"
	standingInstructionsMark = "<!-- agent-orchestrator: managed Codewhale standing instructions; do not edit -->"
	managedConfigEnv         = "CODEWHALE_MANAGED_CONFIG_PATH"
	legacyManagedConfigEnv   = "DEEPSEEK_MANAGED_CONFIG_PATH"
	minLifecycleVersion      = "0.10.0"
	commandTimeout           = 10 * time.Second
)

var versionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

type commandRunner func(ctx context.Context, binary, workingDir string, env map[string]string, args ...string) ([]byte, error)

func runCodewhaleCommand(ctx context.Context, binary, workingDir string, env map[string]string, args ...string) ([]byte, error) {
	cmd := aoprocess.CommandContext(ctx, binary, args...) //nolint:gosec // adapter-resolved binary and static adapter-owned arguments
	if strings.TrimSpace(workingDir) != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = processenv.Merge(env)
	return cmd.CombinedOutput()
}

func (p *Plugin) execute(ctx context.Context, workingDir string, env map[string]string, args ...string) ([]byte, error) {
	binary, err := p.codewhaleBinary(ctx)
	if err != nil {
		return nil, err
	}
	runner := p.runCommand
	if runner == nil {
		runner = runCodewhaleCommand
	}
	probeCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return runner(probeCtx, binary, workingDir, env, args...)
}

// GetAgentHooks installs only AO's hidden standing-instruction rule. Lifecycle
// observation is configured later, after AO has assigned the runtime launch
// generation, and never mutates Codewhale's user hook table.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("codewhale.GetAgentHooks: WorkspacePath is required")
	}
	if err := p.requireLifecycleContract(ctx, cfg.WorkspacePath, cfg.Env); err != nil {
		return err
	}
	if err := p.requireManagedConfigSlot(ctx, cfg.WorkspacePath, cfg.Env); err != nil {
		return err
	}

	dir := filepath.Join(cfg.WorkspacePath, ".codewhale", rulesDirName)
	path := filepath.Join(dir, standingInstructionsName)
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // caller-owned workspace path
		if !strings.Contains(string(data), standingInstructionsMark) {
			return fmt.Errorf("codewhale.GetAgentHooks: refusing to overwrite non-AO file at %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("codewhale.GetAgentHooks: read standing instructions: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("codewhale.GetAgentHooks: create rules directory: %w", err)
	}
	systemPrompt := cfg.SystemPrompt
	if strings.TrimSpace(systemPrompt) == "" && strings.TrimSpace(cfg.SystemPromptFile) != "" {
		data, err := os.ReadFile(cfg.SystemPromptFile) //nolint:gosec // path is AO-owned launch configuration
		if err != nil {
			return fmt.Errorf("codewhale.GetAgentHooks: read system prompt file: %w", err)
		}
		systemPrompt = string(data)
	}
	content := standingInstructionsMark + "\n\n" + strings.TrimSpace(systemPrompt) + "\n"
	if err := hookutil.AtomicWriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("codewhale.GetAgentHooks: write standing instructions: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(dir, standingInstructionsName); err != nil {
		return fmt.Errorf("codewhale.GetAgentHooks: gitignore: %w", err)
	}
	return nil
}

// PrepareRuntimeLaunch is an optional session-manager capability invoked after
// AO assigns AO_RUNTIME_LAUNCH_ID. It creates a per-generation lifecycle
// overlay under AO_DATA_DIR and points only this Codewhale process at it.
func (p *Plugin) PrepareRuntimeLaunch(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dataDir := strings.TrimSpace(cfg.DataDir)
	sessionID := strings.TrimSpace(cfg.SessionID)
	launchID := strings.TrimSpace(cfg.Env["AO_RUNTIME_LAUNCH_ID"])
	runFilePath := strings.TrimSpace(cfg.Env["AO_RUN_FILE"])
	if dataDir == "" || sessionID == "" || launchID == "" || runFilePath == "" {
		return errors.New("codewhale.PrepareRuntimeLaunch: data dir, session id, launch id, and run file are required")
	}
	if filepath.Base(sessionID) != sessionID || sessionID == "." || sessionID == ".." {
		return fmt.Errorf("codewhale.PrepareRuntimeLaunch: unsafe session id %q", sessionID)
	}
	info, err := runfile.Read(runFilePath)
	if err != nil {
		return fmt.Errorf("codewhale.PrepareRuntimeLaunch: read daemon run file: %w", err)
	}
	if info == nil || info.Port < 1 || info.Port > 65535 {
		return errors.New("codewhale.PrepareRuntimeLaunch: daemon run file has no valid port")
	}

	dir := filepath.Join(dataDir, "agent-runtime", adapterID, "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("codewhale.PrepareRuntimeLaunch: create runtime config directory: %w", err)
	}
	outbox := filepath.Join(dir, "lifecycle.jsonl")
	if err := os.Remove(outbox); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("codewhale.PrepareRuntimeLaunch: reset lifecycle outbox: %w", err)
	}

	webhook := url.URL{
		Scheme: "http",
		Host:   "127.0.0.1:" + strconv.Itoa(info.Port),
		Path:   "/api/v1/sessions/" + url.PathEscape(sessionID) + "/activity/codewhale",
	}
	query := webhook.Query()
	query.Set("launchId", launchID)
	webhook.RawQuery = query.Encode()
	config := "# managed by agent-orchestrator; Codewhale user config and hooks remain authoritative\n" +
		"[lifecycle_outbox]\n" +
		"path = " + strconv.Quote(outbox) + "\n" +
		"webhook_url = " + strconv.Quote(webhook.String()) + "\n"
	configPath := filepath.Join(dir, "managed-config.toml")
	if err := hookutil.AtomicWriteFile(configPath, []byte(config), 0o600); err != nil {
		return fmt.Errorf("codewhale.PrepareRuntimeLaunch: write managed config: %w", err)
	}
	cfg.Env[managedConfigEnv] = configPath
	return nil
}

func (p *Plugin) requireLifecycleContract(ctx context.Context, workingDir string, env map[string]string) error {
	out, err := p.execute(ctx, workingDir, env, "--version")
	if err != nil {
		return fmt.Errorf("codewhale.GetAgentHooks: probe codewhale --version: %w", err)
	}
	version, ok := parseVersion(string(out))
	if !ok {
		return fmt.Errorf("codewhale.GetAgentHooks: parse codewhale --version output %q", strings.TrimSpace(string(out)))
	}
	minimum, _ := parseVersion(minLifecycleVersion)
	if version.less(minimum) {
		return fmt.Errorf("codewhale.GetAgentHooks: lifecycle tracking requires Codewhale %s or newer (found %s)", minLifecycleVersion, version)
	}
	return nil
}

func (p *Plugin) requireManagedConfigSlot(ctx context.Context, workingDir string, env map[string]string) error {
	for _, name := range []string{managedConfigEnv, legacyManagedConfigEnv} {
		if strings.TrimSpace(env[name]) != "" {
			return fmt.Errorf("codewhale.GetAgentHooks: %s is already set; refusing to replace user-managed configuration", name)
		}
	}
	for _, path := range defaultManagedConfigPaths() {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return fmt.Errorf("codewhale.GetAgentHooks: managed configuration already exists at %s", path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("codewhale.GetAgentHooks: inspect managed configuration %s: %w", path, err)
		}
	}
	out, err := p.execute(ctx, workingDir, env, "config", "get", "managed_config_path")
	value := strings.TrimSpace(string(out))
	if err == nil {
		if value != "" {
			return fmt.Errorf("codewhale.GetAgentHooks: user config already selects managed_config_path %q", value)
		}
		return nil
	}
	if strings.Contains(strings.ToLower(value), "key not found") {
		return nil
	}
	return fmt.Errorf("codewhale.GetAgentHooks: could not verify managed_config_path is unused: %w: %s", err, value)
}

func defaultManagedConfigPaths() []string {
	if runtime.GOOS != "windows" {
		return []string{"/etc/deepseek/managed_config.toml"}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".codewhale", "managed_config.toml"),
		filepath.Join(home, ".deepseek", "managed_config.toml"),
	}
}

type semanticVersion [3]int

func parseVersion(output string) (semanticVersion, bool) {
	match := versionPattern.FindStringSubmatch(output)
	if match == nil {
		return semanticVersion{}, false
	}
	var version semanticVersion
	for i := range version {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return semanticVersion{}, false
		}
		version[i] = n
	}
	return version, true
}

func (v semanticVersion) less(other semanticVersion) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

func (v semanticVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}
