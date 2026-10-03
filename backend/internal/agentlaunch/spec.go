// Package agentlaunch persists the exact argv a runtime should execute.
package agentlaunch

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EnvSpecPath is the environment variable that holds the path to the launch spec file.
const EnvSpecPath = "AO_LAUNCH_SPEC"

// Spec describes the agent process the launcher trampoline should exec.
type Spec struct {
	WorkspacePath string   `json:"workspacePath"`
	Argv          []string `json:"argv"`
	FallbackArgv  []string `json:"fallbackArgv,omitempty"`
}

// WriteTemp serialises spec to a temporary JSON file and returns its path.
// It keeps the historical shared-OS-temp behavior for callers without a
// session folder; managed launches should prefer WriteTempDir so the spec
// lives inside the session's project-owned temp folder (issue #5933).
func WriteTemp(spec Spec) (string, error) {
	file, err := os.CreateTemp(os.TempDir(), "ao-launch-*.json")
	if err != nil {
		return "", fmt.Errorf("create launch spec: %w", err)
	}
	path := file.Name()
	enc := json.NewEncoder(file)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(spec); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write launch spec: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close launch spec: %w", err)
	}
	return path, nil
}

// WriteTempDir serialises spec to a temporary JSON file inside dir,
// creating dir when missing, and returns its path. Managed launches pass
// their session's project-owned temp folder so the spec file is attributable
// to that session and never lands in shared OS temp.
func WriteTempDir(dir string, spec Spec) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("create launch spec: directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create launch spec directory: %w", err)
	}
	file, err := os.CreateTemp(dir, "ao-launch-*.json")
	if err != nil {
		return "", fmt.Errorf("create launch spec: %w", err)
	}
	path := file.Name()
	enc := json.NewEncoder(file)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(spec); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write launch spec: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close launch spec: %w", err)
	}
	return path, nil
}

// ReadAndRemove reads and deletes the spec file at path, returning its contents.
func ReadAndRemove(path string) (Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("read launch spec: %w", err)
	}
	_ = os.Remove(path)

	var spec Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return Spec{}, fmt.Errorf("parse launch spec: %w", err)
	}
	if len(spec.Argv) == 0 {
		return Spec{}, fmt.Errorf("launch spec: argv is required")
	}
	return spec, nil
}
