package kimi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var kimiAPIKeyLineRE = regexp.MustCompile(`(?m)^\s*api_key\s*=\s*("([^"]*)"|'([^']*)'|([^\s#]+))`)

const (
	kimiInstructionsDirName  = ".kimi"
	kimiInstructionsFileName = "AGENTS.md"
	kimiInstructionsSentinel = "<!-- managed by agent-orchestrator: kimi system prompt -->"
	kimiInstructionsEnd      = "<!-- /managed by agent-orchestrator: kimi system prompt -->"

	kimiHooksSentinelStart = "# managed by agent-orchestrator: kimi hooks"
	kimiHooksSentinelEnd   = "# /managed by agent-orchestrator: kimi hooks"
)

// GetAgentHooks installs AO's standing system prompt through Kimi's
// project-level instruction file. Kimi has no system-prompt argv flag, and its
// user-level config lives outside AO's data dir, so a gitignored worktree-local
// instruction file is the least invasive session-scoped injection point. It
// also installs Kimi lifecycle hooks into the AO-managed Kimi config so AO can
// capture Kimi's native session id for true resume without mutating the user's
// global Kimi profile.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("kimi.GetAgentHooks: WorkspacePath is required")
	}

	if err := installKimiConfigHooks(cfg); err != nil {
		return fmt.Errorf("kimi.GetAgentHooks: %w", err)
	}

	systemPrompt, err := kimiSystemPromptText(cfg.SystemPrompt, cfg.SystemPromptFile)
	if err != nil {
		return fmt.Errorf("kimi.GetAgentHooks: %w", err)
	}
	if err := PrepareACPInstructions(ctx, cfg.WorkspacePath, systemPrompt); err != nil {
		return fmt.Errorf("kimi.GetAgentHooks: %w", err)
	}
	return nil
}

// PrepareACPInstructions installs AO's standing instructions where Kimi Code
// discovers project-level AGENTS.md files. Chat sessions call this immediately
// before launching `kimi acp`; unlike TUI sessions, they do not run
// GetAgentHooks during workspace preparation.
func PrepareACPInstructions(ctx context.Context, workspacePath, systemPrompt string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return nil
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("kimi: workspace path is required for ACP instructions")
	}
	instructionsPath := kimiInstructionsPath(workspacePath)
	var existing []byte
	existing, err := os.ReadFile(instructionsPath) //nolint:gosec // path built from caller-owned workspace dir
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("kimi: read ACP instructions %s: %w", instructionsPath, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(instructionsPath), 0o750); err != nil {
		return fmt.Errorf("kimi: create ACP instruction dir: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	body := mergeKimiInstructionFile(string(existing), systemPrompt)
	if err := hookutil.AtomicWriteFile(instructionsPath, []byte(body), 0o600); err != nil {
		return fmt.Errorf("kimi: write ACP instructions %s: %w", instructionsPath, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(instructionsPath), kimiInstructionsFileName); err != nil {
		return fmt.Errorf("kimi: gitignore ACP instructions: %w", err)
	}
	return ctx.Err()
}

func kimiInstructionsPath(workspacePath string) string {
	return filepath.Join(workspacePath, kimiInstructionsDirName, kimiInstructionsFileName)
}

func installKimiConfigHooks(cfg ports.WorkspaceHookConfig) error {
	home, ok := kimiCodeHomeFromEnv(cfg.Env)
	if !ok {
		return errors.New("kimi: AO-managed Kimi Code home is unavailable")
	}
	if err := seedKimiCredentials(home); err != nil {
		return err
	}
	path := filepath.Join(home, "config.toml")
	data, err := os.ReadFile(path) //nolint:gosec // path is the AO-managed Kimi config under KIMI_CODE_HOME.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if seeded, ok, err := kimiSeedConfig(path, data); err != nil {
		return err
	} else if ok {
		data = seeded
	}
	body := mergeKimiHooksConfig(string(data))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create Kimi config dir: %w", err)
	}
	if err := hookutil.AtomicWriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func seedKimiCredentials(targetHome string) error {
	sourceHome, ok := kimiCodeHome()
	if !ok {
		return nil
	}
	sourceConfigPath := filepath.Join(sourceHome, "config.toml")
	sourcePaths, err := kimiConfigOAuthCredentialPaths(sourceConfigPath)
	if err != nil {
		return fmt.Errorf("read source Kimi config %s: %w", sourceConfigPath, err)
	}
	if len(sourcePaths) == 0 {
		// Preserve the legacy credential-only seed path for profiles created by
		// Kimi versions that did not persist an OAuth reference in config.toml.
		sourcePaths = []string{filepath.Join(sourceHome, "credentials", "kimi-code.json")}
	}
	for _, sourcePath := range sourcePaths {
		targetPath := filepath.Join(targetHome, "credentials", filepath.Base(sourcePath))
		if err := seedKimiCredential(sourcePath, targetPath); err != nil {
			return err
		}
	}
	return nil
}

func seedKimiCredential(sourcePath, targetPath string) error {
	if sameKimiConfigPath(sourcePath, targetPath) {
		return nil
	}
	if _, err := os.Stat(targetPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat target Kimi credentials %s: %w", targetPath, err)
	}
	status, ok, err := kimiCredentialsAuthStatus(sourcePath)
	if err != nil {
		return fmt.Errorf("read source Kimi credentials %s: %w", sourcePath, err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		return nil
	}
	data, err := os.ReadFile(sourcePath) //nolint:gosec // user Kimi credentials copied into AO's isolated Kimi home.
	if err != nil {
		return fmt.Errorf("read source Kimi credentials %s: %w", sourcePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return fmt.Errorf("create target Kimi credentials dir: %w", err)
	}
	if err := hookutil.AtomicWriteFile(targetPath, data, 0o600); err != nil {
		return fmt.Errorf("write target Kimi credentials %s: %w", targetPath, err)
	}
	return nil
}

func kimiCodeHomeFromEnv(env map[string]string) (string, bool) {
	if env != nil {
		if home := strings.TrimSpace(env[kimiCodeHomeEnv]); home != "" {
			return home, true
		}
	}
	return "", false
}

func kimiSeedConfig(targetPath string, existing []byte) ([]byte, bool, error) {
	if kimiConfigHasAPIKey(existing) {
		return nil, false, nil
	}
	if !kimiConfigCanSeed(existing) {
		return nil, false, nil
	}
	sourceHome, ok := kimiCodeHome()
	if !ok {
		return nil, false, nil
	}
	sourcePath := filepath.Join(sourceHome, "config.toml")
	if sameKimiConfigPath(sourcePath, targetPath) {
		return nil, false, nil
	}
	source, err := os.ReadFile(sourcePath) //nolint:gosec // user/process Kimi config used only as a seed for AO-managed home.
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read source Kimi config %s: %w", sourcePath, err)
	}
	if !kimiConfigHasAPIKey(source) {
		// Device-code logins leave api_key empty and keep their tokens in the
		// credential file. The full config is still required for default_model,
		// the provider/OAuth mapping, model aliases, services, and permissions.
		authorized, err := kimiSourceOAuthAuthorized(sourceHome)
		if err != nil {
			return nil, false, err
		}
		if !authorized {
			return nil, false, nil
		}
	}
	return source, true, nil
}

func kimiSourceOAuthAuthorized(sourceHome string) (bool, error) {
	configPath := filepath.Join(sourceHome, "config.toml")
	paths, err := kimiConfigOAuthCredentialPaths(configPath)
	if err != nil {
		return false, fmt.Errorf("read source Kimi config %s: %w", configPath, err)
	}
	for _, path := range paths {
		status, ok, err := kimiCredentialsAuthStatus(path)
		if err != nil {
			return false, fmt.Errorf("read source Kimi credentials %s: %w", path, err)
		}
		if ok && status == ports.AgentAuthStatusAuthorized {
			return true, nil
		}
	}
	return false, nil
}

func kimiConfigCanSeed(existing []byte) bool {
	text := strings.TrimSpace(string(existing))
	if text == "" {
		return true
	}
	return strings.TrimSpace(removeKimiManagedHooks(text)) == ""
}

func removeKimiManagedHooks(existing string) string {
	start := strings.Index(existing, kimiHooksSentinelStart)
	if start < 0 {
		return existing
	}
	afterStart := existing[start+len(kimiHooksSentinelStart):]
	endRel := strings.Index(afterStart, kimiHooksSentinelEnd)
	if endRel < 0 {
		return strings.TrimRight(existing[:start], "\n")
	}
	end := start + len(kimiHooksSentinelStart) + endRel + len(kimiHooksSentinelEnd)
	return existing[:start] + existing[end:]
}

func kimiConfigHasAPIKey(data []byte) bool {
	for _, match := range kimiAPIKeyLineRE.FindAllStringSubmatch(string(data), -1) {
		for _, group := range match[2:] {
			if strings.TrimSpace(group) != "" {
				return true
			}
		}
	}
	return false
}

func sameKimiConfigPath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return absA == absB
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func mergeKimiHooksConfig(existing string) string {
	block := kimiHooksConfigBlock()
	start := strings.Index(existing, kimiHooksSentinelStart)
	if start < 0 {
		return joinKimiConfigParts(existing, block, "")
	}
	afterStart := existing[start+len(kimiHooksSentinelStart):]
	endRel := strings.Index(afterStart, kimiHooksSentinelEnd)
	if endRel < 0 {
		return joinKimiConfigParts(existing[:start], block, "")
	}
	end := start + len(kimiHooksSentinelStart) + endRel + len(kimiHooksSentinelEnd)
	return joinKimiConfigParts(existing[:start], block, existing[end:])
}

func kimiHooksConfigBlock() string {
	return kimiHooksSentinelStart + "\n\n" +
		kimiHookEntry("SessionStart", "startup", "ao hooks kimi session-start") +
		kimiHookEntry("UserPromptSubmit", "", "ao hooks kimi user-prompt-submit") +
		kimiHookEntry("PermissionRequest", "", "ao hooks kimi permission-request") +
		kimiHookEntry("Stop", "", "ao hooks kimi stop") +
		kimiHooksSentinelEnd + "\n"
}

func kimiHookEntry(event, matcher, command string) string {
	var b strings.Builder
	b.WriteString("[[hooks]]\n")
	b.WriteString("event = ")
	b.WriteString(quoteTOMLString(event))
	b.WriteByte('\n')
	if matcher != "" {
		b.WriteString("matcher = ")
		b.WriteString(quoteTOMLString(matcher))
		b.WriteByte('\n')
	}
	b.WriteString("command = ")
	b.WriteString(quoteTOMLString(command))
	b.WriteByte('\n')
	b.WriteString("timeout = 5\n\n")
	return b.String()
}

func quoteTOMLString(s string) string {
	return fmt.Sprintf("%q", s)
}

func joinKimiConfigParts(prefix, block, suffix string) string {
	var b strings.Builder
	prefix = strings.TrimRight(prefix, "\n")
	if prefix != "" {
		b.WriteString(prefix)
		b.WriteString("\n\n")
	}
	b.WriteString(block)
	suffix = strings.TrimLeft(suffix, "\n")
	if suffix != "" {
		b.WriteString("\n")
		b.WriteString(suffix)
	}
	return b.String()
}

func kimiSystemPromptText(inline, file string) (string, error) {
	if strings.TrimSpace(inline) != "" {
		return strings.TrimRight(inline, "\n"), nil
	}
	if strings.TrimSpace(file) == "" {
		return "", nil
	}
	data, err := os.ReadFile(file) //nolint:gosec // path is AO-owned launch config
	if err != nil {
		return "", fmt.Errorf("read system prompt file: %w", err)
	}
	return strings.TrimRight(string(data), "\n"), nil
}

func kimiInstructionFile(systemPrompt string) string {
	return kimiInstructionsSentinel + "\n\n" +
		"# Agent Orchestrator Session Instructions\n\n" +
		strings.TrimRight(systemPrompt, "\n") + "\n\n" +
		kimiInstructionsEnd + "\n"
}

func mergeKimiInstructionFile(existing, systemPrompt string) string {
	block := kimiInstructionFile(systemPrompt)
	start := strings.Index(existing, kimiInstructionsSentinel)
	if start < 0 {
		return joinKimiInstructionParts(existing, block, "")
	}

	afterStart := existing[start+len(kimiInstructionsSentinel):]
	endRel := strings.Index(afterStart, kimiInstructionsEnd)
	if endRel < 0 {
		// Older AO-managed files did not have an end marker. Treat the marker as
		// owning the rest of the file so stale AO instructions are replaced.
		return joinKimiInstructionParts(existing[:start], block, "")
	}

	end := start + len(kimiInstructionsSentinel) + endRel + len(kimiInstructionsEnd)
	return joinKimiInstructionParts(existing[:start], block, existing[end:])
}

func joinKimiInstructionParts(prefix, block, suffix string) string {
	var b strings.Builder
	prefix = strings.TrimRight(prefix, "\n")
	if prefix != "" {
		b.WriteString(prefix)
		b.WriteString("\n\n")
	}
	b.WriteString(block)
	suffix = strings.TrimLeft(suffix, "\n")
	if suffix != "" {
		b.WriteString("\n")
		b.WriteString(suffix)
	}
	return b.String()
}
