//go:build !windows

package agentbinary

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

func TestShellIdentityProbeUsesRecoveredInterpreterWithoutChangingDaemonPATH(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "muse")
	nodeDir := t.TempDir()
	for path, script := range map[string]string{
		binary:                         "#!/usr/bin/env node\n",
		filepath.Join(nodeDir, "node"): "#!/bin/sh\nprintf 'Muse Code fixture\\n'\n",
	} {
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", "/daemon/only")
	spec := ports.AgentBinarySpec{Names: []string{"muse"}, Lookup: missing, Presence: missing, Normalize: func(ctx context.Context, path string, purpose ports.BinaryResolvePurpose) (string, error) {
		out, err := aoprocess.CommandContext(ctx, path, "--version").CombinedOutput()
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(string(out), "Muse Code") {
			t.Errorf("identity output=%q", out)
		}
		return path, nil
	}}
	c := New(Config{Specs: map[domain.AgentHarness]ports.AgentBinarySpec{"muse": spec}, Probe: probeFunc(func(context.Context, []string) (ports.AgentShellSnapshot, error) {
		return ports.AgentShellSnapshot{Paths: map[string]string{"muse": binary}, Path: nodeDir}, nil
	})})
	defer c.Close()
	result, err := c.Resolve(context.Background(), "muse", ports.BinaryResolveLaunch)
	if err != nil || result.Executable != binary {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if os.Getenv("PATH") != "/daemon/only" {
		t.Fatal("daemon PATH changed")
	}
}
