//go:build !windows

package agentbase

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type shellPATHDiscovery struct{ path string }

func (s shellPATHDiscovery) ShellPATH() string { return s.path }
func (shellPATHDiscovery) Resolve(context.Context, domain.AgentHarness, ports.BinaryResolvePurpose) (ports.AgentBinaryResolution, error) {
	return ports.AgentBinaryResolution{}, nil
}
func (shellPATHDiscovery) Invalidate(domain.AgentHarness) {}

func TestAugmentBinaryRuntimeEnvFindsShellOnlyNodeAndPreservesPinnedAO(t *testing.T) {
	launcherDir := t.TempDir()
	nodeDir := t.TempDir()
	pinnedDir := t.TempDir()
	launcher := filepath.Join(launcherDir, "codex")
	node := filepath.Join(nodeDir, "node")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(node, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/daemon/only")
	env := map[string]string{"PATH": pinnedDir + string(os.PathListSeparator) + "/daemon/only"}
	b := Base{BinaryDiscovery: shellPATHDiscovery{path: nodeDir}}
	b.AugmentBinaryRuntimeEnv(context.Background(), env, []string{launcher}, pinnedDir)

	want := strings.Join([]string{pinnedDir, launcherDir, nodeDir, "/daemon/only"}, string(os.PathListSeparator))
	if env["PATH"] != want {
		t.Fatalf("child PATH = %q, want %q", env["PATH"], want)
	}
	if os.Getenv("PATH") != "/daemon/only" {
		t.Fatalf("daemon PATH mutated to %q", os.Getenv("PATH"))
	}
}
