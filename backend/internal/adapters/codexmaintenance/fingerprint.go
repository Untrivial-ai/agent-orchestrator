package codexmaintenance

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExecutableFingerprint includes recognized shim targets, package manifests and
// native payloads. A package manager can replace these without changing its shim.
// It is a bounded filesystem read and never launches Codex or a package manager.
// Cancellation stops between filesystem operations and returns no fingerprint.
func ExecutableFingerprint(ctx context.Context, binary string) string {
	r := &Resolver{realpath: filepath.EvalSymlinks, readFile: readBoundedFile, goos: runtime.GOOS}
	return r.executableFingerprint(ctx, binary)
}

func (r *Resolver) executableFingerprint(ctx context.Context, binary string) string {
	resolved := r.canonical(ctx, binary)
	target := r.shimTarget(ctx, resolved)
	if target == "" {
		target = resolved
	}
	files := []string{binary, resolved, target}
	if pkg := r.packageRoot(ctx, target); pkg != "" {
		files = append(files, pkg+"/package.json")
		for _, pattern := range []string{"vendor/*/codex/codex*", "vendor/*/bin/codex*", "node_modules/@openai/codex-*/package.json", "node_modules/@openai/codex-*/vendor/*/codex/codex*", "node_modules/@openai/codex-*/vendor/*/bin/codex*"} {
			if ctx.Err() != nil {
				return ""
			}
			matches, _ := filepath.Glob(filepath.Join(pkg, pattern))
			if len(matches) > 64 {
				matches = matches[:64]
			}
			files = append(files, matches...)
		}
	}
	// Vite+ dispatchers keep stable executable bytes while changing metadata.
	bin := filepath.Dir(binary)
	files = append(files, filepath.Join(bin, "codex.shim"), filepath.Join(bin, "..", "bins", "codex.json"), filepath.Join(bin, "..", "packages", "@openai", "codex.json"))
	stamps := make([]string, 0, len(files))
	for _, file := range files {
		if ctx.Err() != nil {
			return ""
		}
		stamps = append(stamps, r.fileStamp(ctx, file))
	}
	if ctx.Err() != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(stamps, "\x00")))
	return fmt.Sprintf("%x", sum)
}

func (r *Resolver) fileStamp(ctx context.Context, file string) string {
	if ctx.Err() != nil {
		return ""
	}
	resolved, err := r.realpath(file)
	if ctx.Err() != nil {
		return ""
	}
	if err != nil {
		return file + ":missing"
	}
	info, err := os.Stat(resolved)
	if ctx.Err() != nil {
		return ""
	}
	if err != nil {
		return resolved + ":unreadable"
	}
	return fmt.Sprintf("%s:%d:%d", resolved, info.Size(), info.ModTime().UnixNano())
}
