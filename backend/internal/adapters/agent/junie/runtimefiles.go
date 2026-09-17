// Package junie contains the experimental AO integration for the Junie CLI.
package junie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
)

// RuntimeFileRequest identifies the AO-owned Junie overlay for one session.
type RuntimeFileRequest struct {
	DataDir, SessionID, SystemPrompt, SystemPromptFile string
}

// RuntimeFiles names the generated Junie runtime overlay files. A non-empty
// GuidelinesPath is experimental: Junie uses the explicit AO guidelines in
// place of its native project guidelines rather than appending them.
type RuntimeFiles struct {
	ConfigPath, GuidelinesPath string
}

// RuntimeFileBuilder prepares AO-owned Junie runtime files without modifying
// Junie's user or project configuration.
type RuntimeFileBuilder interface {
	Prepare(context.Context, RuntimeFileRequest) (RuntimeFiles, error)
}

type runtimeFileBuilder struct{}

// NewRuntimeFileBuilder returns a builder for isolated Junie runtime overlays.
func NewRuntimeFileBuilder() RuntimeFileBuilder {
	return runtimeFileBuilder{}
}

func (runtimeFileBuilder) Prepare(ctx context.Context, request RuntimeFileRequest) (RuntimeFiles, error) {
	if err := ctx.Err(); err != nil {
		return RuntimeFiles{}, err
	}
	if strings.TrimSpace(request.DataDir) == "" {
		return RuntimeFiles{}, errors.New("junie: AO data directory is required")
	}
	if !filepath.IsAbs(request.DataDir) {
		return RuntimeFiles{}, errors.New("junie: AO data directory must be absolute")
	}
	if !safeRuntimePathComponent(request.SessionID) {
		return RuntimeFiles{}, errors.New("junie: invalid session ID")
	}

	guidelines, err := runtimeGuidelines(request)
	if err != nil {
		return RuntimeFiles{}, err
	}
	config, err := runtimeConfigJSON()
	if err != nil {
		return RuntimeFiles{}, err
	}
	if err := ctx.Err(); err != nil {
		return RuntimeFiles{}, err
	}

	dataDir := filepath.Clean(request.DataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return RuntimeFiles{}, fmt.Errorf("junie: create AO data directory: %w", err)
	}
	if err := requireRealDirectory(dataDir); err != nil {
		return RuntimeFiles{}, err
	}

	runtimeRoot := filepath.Join(dataDir, "agent-runtime")
	junieRoot := filepath.Join(runtimeRoot, "junie")
	sessionDir := filepath.Join(junieRoot, request.SessionID)
	for _, dir := range []string{runtimeRoot, junieRoot, sessionDir} {
		if err := ctx.Err(); err != nil {
			return RuntimeFiles{}, err
		}
		if err := ensurePrivateRuntimeDirectory(dir); err != nil {
			return RuntimeFiles{}, err
		}
	}

	files := RuntimeFiles{
		ConfigPath: filepath.Join(sessionDir, "config.json"),
	}
	if len(guidelines) > 0 {
		files.GuidelinesPath = filepath.Join(sessionDir, "guidelines.md")
	}
	if err := validateRuntimeDirectoryChain(dataDir, runtimeRoot, junieRoot, sessionDir); err != nil {
		return RuntimeFiles{}, err
	}
	paths := []string{files.ConfigPath}
	if files.GuidelinesPath != "" {
		paths = append(paths, files.GuidelinesPath)
	}
	for _, path := range paths {
		if err := requireSafeRuntimeFile(path); err != nil {
			return RuntimeFiles{}, err
		}
	}

	if files.GuidelinesPath != "" {
		if err := ctx.Err(); err != nil {
			return RuntimeFiles{}, err
		}
		if err := validateRuntimeDirectoryChain(dataDir, runtimeRoot, junieRoot, sessionDir); err != nil {
			return RuntimeFiles{}, err
		}
		if err := hookutil.AtomicWriteFile(files.GuidelinesPath, guidelines, 0o600); err != nil {
			return RuntimeFiles{}, fmt.Errorf("junie: write guidelines: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return RuntimeFiles{}, err
	}
	if err := validateRuntimeDirectoryChain(dataDir, runtimeRoot, junieRoot, sessionDir); err != nil {
		return RuntimeFiles{}, err
	}
	if err := hookutil.AtomicWriteFile(files.ConfigPath, config, 0o600); err != nil {
		return RuntimeFiles{}, fmt.Errorf("junie: write config: %w", err)
	}
	return files, nil
}

func runtimeGuidelines(request RuntimeFileRequest) ([]byte, error) {
	text := request.SystemPrompt
	if text == "" && request.SystemPromptFile != "" {
		data, err := os.ReadFile(request.SystemPromptFile) //nolint:gosec // caller supplies the AO standing-instructions path
		if err != nil {
			return nil, fmt.Errorf("junie: read standing instructions: %w", err)
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	return []byte(strings.TrimRight(text, "\n") + "\n"), nil
}

type runtimeConfig struct {
	Hooks map[string][]runtimeHookRule `json:"hooks"`
}

type runtimeHookRule struct {
	Matcher string               `json:"matcher,omitempty"`
	Hooks   []runtimeHookCommand `json:"hooks"`
}

type runtimeHookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

func runtimeConfigJSON() ([]byte, error) {
	command := func(name string) []runtimeHookCommand {
		return []runtimeHookCommand{{
			Type:    "command",
			Command: "ao hooks junie " + name,
			Timeout: 2,
		}}
	}
	config := runtimeConfig{Hooks: map[string][]runtimeHookRule{
		"SessionStart":     {{Matcher: "startup|resume|clear", Hooks: command("session-start")}},
		"UserPromptSubmit": {{Hooks: command("user-prompt-submit")}},
		"PreToolUse":       {{Matcher: ".*", Hooks: command("pre-tool-use")}},
		"Stop":             {{Hooks: command("stop")}},
		"StopFailure":      {{Matcher: ".*", Hooks: command("stop-failure")}},
		"SessionEnd":       {{Matcher: "prompt_input_exit|logout|other", Hooks: command("session-end")}},
	}}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("junie: encode config: %w", err)
	}
	return append(data, '\n'), nil
}

func safeRuntimePathComponent(value string) bool {
	return strings.TrimSpace(value) != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, "/\\\x00")
}

func ensurePrivateRuntimeDirectory(path string) error {
	err := os.Mkdir(path, 0o700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("junie: create runtime directory %s: %w", path, err)
	}
	if err := requireRealDirectory(path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // runtime directories contain private session instructions
		return fmt.Errorf("junie: secure runtime directory %s: %w", path, err)
	}
	return nil
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("junie: inspect runtime directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("junie: runtime path %s is not a real directory", path)
	}
	return nil
}

func validateRuntimeDirectoryChain(paths ...string) error {
	for _, path := range paths {
		if err := requireRealDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func requireSafeRuntimeFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("junie: inspect runtime file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("junie: runtime file %s is not a regular file", path)
	}
	return nil
}
