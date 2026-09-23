package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SessionPromptDir is the session-private directory a binding writes launch
// inputs to. Standing instructions are process input rather than workspace
// content, so they live under AO's data directory keyed by session, never in
// the user's checkout.
func SessionPromptDir(cfg LaunchConfig, binding string) string {
	return filepath.Join(cfg.DataDir, "prompts", string(cfg.SessionID), binding)
}

// WriteSessionPrompt writes content to a named file in the binding's session
// prompt directory and returns the path, for providers that accept standing
// instructions as a file rather than as a flag or protocol field.
func WriteSessionPrompt(
	ctx context.Context,
	cfg LaunchConfig,
	binding, name, content string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return "", fmt.Errorf("%s standing instructions require AO data directory", binding)
	}
	dir := SessionPromptDir(cfg, binding)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s prompt directory: %w", binding, err)
	}
	path := filepath.Join(dir, name)
	body := strings.TrimRight(content, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("write %s prompt file: %w", binding, err)
	}
	return path, nil
}
