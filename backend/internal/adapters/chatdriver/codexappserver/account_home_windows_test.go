//go:build windows

package codexappserver

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestManagedHomeDACLRejectsUnsupportedEffectiveAllowACEs(t *testing.T) {
	user := currentAccountHomeTestUser(t)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	untrusted, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	for name, aceType := range map[string]uint8{
		"compound allow":        0x04,
		"object allow":          0x05,
		"callback allow":        0x09,
		"callback object allow": 0x0b,
	} {
		t.Run(name, func(t *testing.T) {
			raw, dacl := rawAccountHomeTestDACL(t, aceType, 0, uint32(windows.GENERIC_READ), untrusted)
			private := managedHomeDACLPrivate(dacl, user, system, administrators)
			runtime.KeepAlive(raw)
			if private {
				t.Fatalf("effective ACE type %#x was accepted", aceType)
			}
		})
	}
}

func TestManagedHomeDACLKeepsKnownDeniesAndInheritOnlyNonGranting(t *testing.T) {
	user := currentAccountHomeTestUser(t)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	untrusted, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		aceType uint8
		flags   uint8
	}{
		"simple deny":                 {aceType: windows.ACCESS_DENIED_ACE_TYPE},
		"object deny":                 {aceType: accountHomeAccessDeniedObjectACEType},
		"callback deny":               {aceType: accountHomeAccessDeniedCallbackACEType},
		"callback object deny":        {aceType: accountHomeAccessDeniedCallbackObjectACEType},
		"inherit-only compound allow": {aceType: 0x04, flags: windows.INHERIT_ONLY_ACE},
	} {
		t.Run(name, func(t *testing.T) {
			raw, dacl := rawAccountHomeTestDACL(t, tc.aceType, tc.flags, uint32(windows.GENERIC_ALL), untrusted)
			private := managedHomeDACLPrivate(dacl, user, system, administrators)
			runtime.KeepAlive(raw)
			if !private {
				t.Fatalf("non-granting ACE type %#x was rejected", tc.aceType)
			}
		})
	}
}

func currentAccountHomeTestUser(t *testing.T) *windows.SID {
	t.Helper()
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := user.User.Sid.Copy()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func rawAccountHomeTestDACL(t *testing.T, aceType, aceFlags uint8, mask uint32, sid *windows.SID) ([]byte, *windows.ACL) {
	t.Helper()
	if sid == nil || !sid.IsValid() {
		t.Fatal("raw test ACL requires a valid SID")
	}
	extraPrefix := 0
	aclRevision := byte(2)
	switch aceType {
	case 0x04: // ACCESS_ALLOWED_COMPOUND_ACE_TYPE
		extraPrefix = 4
	case 0x05, 0x06, 0x0b, 0x0c: // object ACE layouts
		extraPrefix = 4
		aclRevision = 4
	}
	aceSize := 8 + extraPrefix + sid.Len()
	aclSize := 8 + aceSize
	raw := make([]byte, aclSize)
	raw[0] = aclRevision
	binary.LittleEndian.PutUint16(raw[2:4], uint16(aclSize))
	binary.LittleEndian.PutUint16(raw[4:6], 1)
	raw[8] = aceType
	raw[9] = aceFlags
	binary.LittleEndian.PutUint16(raw[10:12], uint16(aceSize))
	binary.LittleEndian.PutUint32(raw[12:16], mask)
	if aceType == 0x04 {
		binary.LittleEndian.PutUint16(raw[16:18], 1) // COMPOUND_ACE_IMPERSONATION
	}
	sidOffset := 16 + extraPrefix
	copy(raw[sidOffset:], unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len()))
	return raw, (*windows.ACL)(unsafe.Pointer(&raw[0]))
}

func protectManagedHomeForTest(t *testing.T, path string) {
	t.Helper()
	applyOwnerOnlyDACL(t, path)
}

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
