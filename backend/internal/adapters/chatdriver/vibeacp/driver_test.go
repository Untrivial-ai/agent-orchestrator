package vibeacp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPermissionPolicyNeverSelectsVibesPermanentGrant(t *testing.T) {
	session := acpsdk.PermissionOption{
		OptionId: "allow_always", Kind: acpsdk.PermissionOptionKindAllowAlways,
	}
	permanent := acpsdk.PermissionOption{
		OptionId: "allow_always_permanent", Kind: acpsdk.PermissionOptionKindAllowAlways,
	}
	once := acpsdk.PermissionOption{
		OptionId: "allow_once", Kind: acpsdk.PermissionOptionKindAllowOnce,
	}
	for _, tt := range []struct {
		options []acpsdk.PermissionOption
		want    acpsdk.PermissionOptionId
	}{
		{options: []acpsdk.PermissionOption{session, permanent, once}, want: session.OptionId},
		{options: []acpsdk.PermissionOption{permanent, session, once}, want: session.OptionId},
		{options: []acpsdk.PermissionOption{permanent, once}, want: once.OptionId},
	} {
		id, handled := permissionPolicy(ports.PermissionModeAuto,
			acpsdk.RequestPermissionRequest{Options: tt.options})
		if !handled || id != tt.want {
			t.Fatalf("selection = (%q, %v), want (%q, true)", id, handled, tt.want)
		}
	}
}

// The adapter beside the resolved `vibe` wins over any `vibe-acp` on PATH, so
// a second Vibe installation cannot be spliced into the session.
func TestResolveVibeACPBinaryPrefersTheAdapterBesideVibe(t *testing.T) {
	dir := t.TempDir()
	vibe := filepath.Join(dir, binaryName("vibe"))
	acp := filepath.Join(dir, binaryName("vibe-acp"))
	writeExecutable(t, vibe)
	writeExecutable(t, acp)
	pathDir := t.TempDir()
	writeExecutable(t, filepath.Join(pathDir, binaryName("vibe-acp")))
	t.Setenv("PATH", pathDir)

	got, err := resolveVibeACPBinary(context.Background(), fixedPlugin(vibe))
	if err != nil {
		t.Fatalf("resolveVibeACPBinary: %v", err)
	}
	if got != acp {
		t.Fatalf("resolved = %q, want the sibling %q", got, acp)
	}
}

func TestResolveVibeACPBinaryFallsBackToPATH(t *testing.T) {
	dir := t.TempDir()
	vibe := filepath.Join(dir, binaryName("vibe"))
	writeExecutable(t, vibe)
	pathDir := t.TempDir()
	acp := filepath.Join(pathDir, binaryName("vibe-acp"))
	writeExecutable(t, acp)
	t.Setenv("PATH", pathDir)

	got, err := resolveVibeACPBinary(context.Background(), fixedPlugin(vibe))
	if err != nil {
		t.Fatalf("resolveVibeACPBinary: %v", err)
	}
	if got != acp {
		t.Fatalf("resolved = %q, want %q", got, acp)
	}
}

type fixedPlugin string

func (p fixedPlugin) ResolveBinary(context.Context) (string, error) { return string(p), nil }
func (fixedPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusUnknown, nil
}

func binaryName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
