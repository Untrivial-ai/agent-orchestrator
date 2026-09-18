package agentlaunch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// AugmentDiscoveredRuntimeEnv uses shell directories only to locate a required interpreter.
// It does not import shell environment variables or change the daemon PATH.
func AugmentDiscoveredRuntimeEnv(ctx context.Context, env map[string]string, argv []string, pinnedDir, search string) {
	lookup := func(name string) (string, error) {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
		names := []string{name}
		if runtime.GOOS == "windows" {
			names = []string{name + ".exe", name + ".cmd", name + ".bat"}
		}
		for _, dir := range filepath.SplitList(search) {
			if !filepath.IsAbs(dir) {
				continue
			}
			for _, candidate := range names {
				path := filepath.Join(dir, candidate)
				if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
					return path, nil
				}
			}
		}
		return "", exec.ErrNotFound
	}
	if env["PATH"] == "" {
		env["PATH"] = os.Getenv("PATH")
	}
	// Normalize the Windows spelling so child overlays cannot carry two PATHs.
	if runtime.GOOS == "windows" {
		for key, value := range env {
			if key != "PATH" && strings.EqualFold(key, "PATH") {
				env["PATH"] = value
				delete(env, key)
			}
		}
	}
	AugmentRuntimePATHForLaunchBinary(ctx, env, argv, lookup, pinnedDir)
}
