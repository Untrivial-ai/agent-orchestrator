//go:build windows

package agent

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func ensurePrivateTestDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	// Elevated Windows runners can assign BUILTIN\Administrators as the owner
	// of t.TempDir(). Normalize the fixture root before creating descendants so
	// the production ownership checks exercise the current-user layout.
	handle := openWindowsTestFile(t, path, windows.WRITE_OWNER|windows.READ_CONTROL)
	defer windows.CloseHandle(handle)
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
		currentWindowsTestUser(t),
		nil,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
}
