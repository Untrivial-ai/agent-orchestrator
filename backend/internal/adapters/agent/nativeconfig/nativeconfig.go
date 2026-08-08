// Package nativeconfig resolves provider-owned session state directories.
package nativeconfig

import (
	"os"
	"path/filepath"
	"strings"
)

// Resolve returns the directory selected for a provider invocation. An entry
// in env, including an explicitly empty one, takes precedence over the daemon
// environment because env describes the child process being launched.
func Resolve(env map[string]string, envKey, defaultDir string) (string, error) {
	if value, ok := env[envKey]; ok {
		if dir := strings.TrimSpace(value); dir != "" {
			return filepath.Clean(dir), nil
		}
		return homeDefault(env, defaultDir)
	}
	if dir := strings.TrimSpace(os.Getenv(envKey)); dir != "" {
		return filepath.Clean(dir), nil
	}
	return homeDefault(env, defaultDir)
}

func homeDefault(env map[string]string, defaultDir string) (string, error) {
	if home := strings.TrimSpace(env["HOME"]); home != "" {
		return filepath.Join(home, defaultDir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, defaultDir), nil
}
