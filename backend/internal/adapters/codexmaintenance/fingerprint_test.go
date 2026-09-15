package codexmaintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutableFingerprintStopsCanceledFilesystemScan(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(dir, "node_modules", "@openai", "codex")
	entry := filepath.Join(pkg, "bin", "codex.js")
	manifest := filepath.Join(pkg, "package.json")
	payload := filepath.Join(pkg, "vendor", "first", "codex", "codex")
	for name, body := range map[string]string{
		entry:    "stable wrapper",
		manifest: `{"name":"@openai/codex","bin":{"codex":"bin/codex.js"}}`,
		payload:  "native payload",
		filepath.Join(pkg, "vendor", "second", "codex", "codex"): "another payload",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Cancel during each phase, including the first of multiple payload stamps.
	// The injected filesystem calls fail the test if the scan continues afterward.
	for _, phase := range []string{"before", "resolve", "shim", "manifest", "payload"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "before" {
				cancel()
			}
			r := &Resolver{
				realpath: func(name string) (string, error) {
					if ctx.Err() != nil {
						t.Fatalf("resolved %q after cancellation", name)
					}
					resolved, err := filepath.EvalSymlinks(name)
					if phase == "resolve" || (phase == "payload" && slash(name) == slash(payload)) {
						cancel()
					}
					return resolved, err
				},
				readFile: func(readCtx context.Context, name string) ([]byte, error) {
					if ctx.Err() != nil {
						t.Fatalf("read %q after cancellation", name)
					}
					data, err := readBoundedFile(readCtx, name)
					if (phase == "shim" && slash(name) == slash(entry)) || (phase == "manifest" && slash(name) == slash(manifest)) {
						cancel()
					}
					return data, err
				},
			}
			if got := r.executableFingerprint(ctx, entry); got != "" {
				t.Fatalf("canceled scan returned partial fingerprint %q", got)
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("scan did not reach cancellation phase")
			}
		})
	}
}

func TestReadBoundedFileHonorsCancellationBeforeOpening(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readBoundedFile(ctx, filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read error = %v, want cancellation before filesystem access", err)
	}
}
