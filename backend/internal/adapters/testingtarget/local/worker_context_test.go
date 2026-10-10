package local

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkerContextUsesOnlyLiveTargetPaths(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	got, err := f.a.WorkerContext(ctx, f.s.target)
	if err != nil || got.CheckoutPath != filepath.Dir(f.s.frontend) || got.CLIPath != filepath.Join(f.s.root, "target-ao") || got.RunFilePath != filepath.Join(f.s.root, "running.json") || got.DataDir != f.s.target.DataDir || got.FixtureDir != filepath.Join(f.s.root, "fixtures") {
		t.Fatalf("worker context = %+v, %v", got, err)
	}
	var facts struct {
		Revision string `json:"revision"`
		Daemon   string `json:"daemonExecutable"`
		Port     int    `json:"port"`
	}
	if err := json.Unmarshal([]byte(got.LaunchContext), &facts); err != nil || facts.Revision != f.s.revision || facts.Daemon != f.s.daemon || facts.Port != f.s.port {
		t.Fatalf("worker did not receive checked launch facts: %s, %v", got.LaunchContext, err)
	}
	foreign := f.s.target
	foreign.Generation++
	if _, err := f.a.WorkerContext(ctx, foreign); err == nil {
		t.Fatal("foreign target received worker context")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.a.WorkerContext(cancelled, f.s.target); err == nil {
		t.Fatal("cancelled lookup succeeded")
	}
	f.s.stopped = true
	if _, err := f.a.WorkerContext(ctx, f.s.target); err == nil {
		t.Fatal("stopped target received worker context")
	}
}

func TestTargetCLIUsesTargetBinaryDirectoryEnvironmentAndExitCode(t *testing.T) {
	f := fixture(t)
	f.s.frontend = filepath.Join(f.s.root, "checkout with 'quotes'", "frontend")
	f.s.daemon = filepath.Join(f.s.root, "owned daemon with 'quotes'", "ao")
	executable := f.s.daemon
	if err := os.MkdirAll(filepath.Dir(f.s.frontend), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	stub := `#!/usr/bin/env python3
import json, os, sys
print(json.dumps({"args": sys.argv[1:], "cwd": os.getcwd(),
                  "aoEnv": {k: v for k, v in os.environ.items() if k.startswith("AO_")},
                  "nested": [k for k in ("CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "NODE_OPTIONS", "ELECTRON_RUN_AS_NODE") if k in os.environ],
                  "other": os.environ.get("TARGET_TEST_OTHER")}))
sys.exit(17)
`
	if err := os.WriteFile(executable, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeTargetCLI(f.s); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(f.s.root, "target-ao"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("wrapper must be owner-readable without execute permissions: %v", err)
	}
	args := []string{"project", "add", "path with spaces", "literal$(value)'quote"}
	command := exec.CommandContext(context.Background(), "python3", append([]string{filepath.Join(f.s.root, "target-ao")}, args...)...)
	command.Env = append(os.Environ(), "AO_RUN_FILE=/supervisor/run", "AO_DATA_DIR=/supervisor/data", "AO_SESSION_ID=supervisor-worker", "AO_TEST_CAPABILITY=test-secret", "AO_FUTURE_SETTING=test-secret", "CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli", "NODE_OPTIONS=unsafe", "ELECTRON_RUN_AS_NODE=1", "TARGET_TEST_OTHER=kept")
	output, err := command.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 {
		t.Fatalf("wrapper did not preserve exit code: %v, %s", err, output)
	}
	var got struct {
		Args   []string          `json:"args"`
		Cwd    string            `json:"cwd"`
		AOEnv  map[string]string `json:"aoEnv"`
		Nested []string          `json:"nested"`
		Other  string            `json:"other"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("target CLI output: %v, %s", err, output)
	}
	wantEnv := map[string]string{"AO_RUN_FILE": filepath.Join(f.s.root, "running.json"), "AO_DATA_DIR": f.s.target.DataDir}
	if !reflect.DeepEqual(got.Args, args) || got.Cwd != filepath.Dir(f.s.frontend) || !reflect.DeepEqual(got.AOEnv, wantEnv) || len(got.Nested) != 0 || got.Other != "kept" {
		t.Fatalf("target CLI inherited supervisor state or lost arguments: %+v", got)
	}
}
