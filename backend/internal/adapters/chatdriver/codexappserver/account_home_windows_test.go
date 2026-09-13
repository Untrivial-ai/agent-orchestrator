//go:build windows

package codexappserver

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestManagedHomePrivateOnWindows pins the Windows meaning of "private managed
// home". os.Mkdir(path, 0o700) reports mode 0777 back through Lstat on Windows,
// so a POSIX permission comparison can never accept a directory AO just
// created; the owner-only protected DACL is what actually makes it private.
func TestManagedHomePrivateOnWindows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode().Perm() == 0o700 {
		t.Fatal("Windows reported POSIX 0700 on a directory; this test no longer covers the bug it was written for")
	}
	if managedHomePrivate(dir, info) {
		t.Fatal("inherited-DACL directory accepted as a private managed home")
	}

	applyOwnerOnlyDACL(t, dir)
	info, err = os.Lstat(dir)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if !managedHomePrivate(dir, info) {
		t.Fatal("owner-only protected DACL rejected as a private managed home")
	}
}

// applyOwnerOnlyDACL mirrors the protection AO applies when it creates a
// managed credential home.
func applyOwnerOnlyDACL(t *testing.T, path string) {
	t.Helper()
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatalf("open token: %v", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatalf("token user: %v", err)
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}
	dacl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, nil)
	if err != nil {
		t.Fatalf("build DACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	); err != nil {
		t.Fatalf("set DACL: %v", err)
	}
}
