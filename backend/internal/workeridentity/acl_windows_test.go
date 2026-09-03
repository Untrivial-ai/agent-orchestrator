//go:build windows

package workeridentity

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSecureContainerRootLetsHostCreateWritableDescendants(t *testing.T) {
	root := t.TempDir()
	workerSID, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := SecureContainerRoot(root, workerSID.String()); err != nil {
		t.Fatal(err)
	}

	child := filepath.Join(root, "project", "session")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatalf("create Host-managed session directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(child, ".git"), []byte("gitdir: fixture"), 0o600); err != nil {
		t.Fatalf("write Host-managed worktree metadata: %v", err)
	}
}
