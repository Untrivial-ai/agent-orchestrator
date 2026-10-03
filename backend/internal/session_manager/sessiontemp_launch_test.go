package sessionmanager

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/sessiontemp"
)

// Managed launches must direct temp files at the project's per-run scratch
// folder instead of inheriting the daemon's shared OS temp (issue #5933).
func TestRuntimeEnvPinsSessionTemp(t *testing.T) {
	dataDir := t.TempDir()
	manager := &Manager{
		dataDir:    dataDir,
		executable: func() (string, error) { return filepath.Join(t.TempDir(), "ao"), nil },
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env, err := manager.runtimeEnv("ses-1", "proj-1", "issue-9", map[string]string{
		"TMPDIR": "/shared/tmp",
		"TEMP":   "/shared/tmp",
		"TMP":    "/shared/tmp",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := sessiontemp.TempDirFor(dataDir, "proj-1", "ses-1")
	for _, key := range []string{"TMPDIR", "TEMP", "TMP"} {
		if env[key] != want {
			t.Fatalf("%s = %q, want project-owned %q", key, env[key], want)
		}
	}
	if _, err := os.Stat(filepath.Join(want)); err != nil {
		t.Fatalf("per-run temp dir missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessiontemp.RunDir(dataDir, "proj-1", "ses-1"), "owner.json")); err != nil {
		t.Fatalf("ownership record missing: %v", err)
	}
}

// A project must not redirect its sessions back into shared OS temp: the
// AO-owned temp values always win, matching the other protected vars.
func TestLaunchRuntimeEnvTempWinsOverProject(t *testing.T) {
	dataDir := t.TempDir()
	manager := &Manager{
		dataDir:    dataDir,
		executable: func() (string, error) { return filepath.Join(t.TempDir(), "ao"), nil },
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env, _, err := manager.launchRuntimeEnv("ses-1", "proj-1", "", map[string]string{
		"TMPDIR": "/tmp",
		"TMP":    `/Windows/Temp`,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := sessiontemp.TempDirFor(dataDir, "proj-1", "ses-1")
	if env["TMPDIR"] != want || env["TEMP"] != want || env["TMP"] != want {
		t.Fatalf("temp env = TMPDIR:%q TEMP:%q TMP:%q, want %q", env["TMPDIR"], env["TEMP"], env["TMP"], want)
	}
}

// An unusable scratch root refuses the launch clearly instead of silently
// inheriting shared OS temp.
func TestLaunchRuntimeEnvRefusesUnusableTempRoot(t *testing.T) {
	for _, dataDir := range []string{"relative/path", string([]byte{0})} {
		manager := &Manager{
			dataDir:    dataDir,
			executable: func() (string, error) { return filepath.Join(t.TempDir(), "ao"), nil },
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		_, _, err := manager.launchRuntimeEnv("ses-1", "proj-1", "", nil)
		if err == nil || !errors.Is(err, sessiontemp.ErrTempRootUnusable) {
			t.Fatalf("dataDir %q err = %v, want ErrTempRootUnusable", dataDir, err)
		}
		if !strings.Contains(err.Error(), "session temp") {
			t.Fatalf("err = %v, want a clear session-temp message", err)
		}
	}
}

// A blank data dir configures no scratch root (focused tests/embedders), so
// launches keep the historical behavior instead of failing on temp setup;
// production always resolves a real data dir, where the pin always applies.
func TestLaunchRuntimeEnvWithoutDataDirSkipsTempPin(t *testing.T) {
	manager := &Manager{
		executable: func() (string, error) { return filepath.Join(t.TempDir(), "ao"), nil },
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env, _, err := manager.launchRuntimeEnv("ses-1", "proj-1", "", map[string]string{
		"TMPDIR": "/shared/tmp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["TMPDIR"] != "/shared/tmp" {
		t.Fatalf("TMPDIR = %q, want untouched project value when no data dir is configured", env["TMPDIR"])
	}
}
