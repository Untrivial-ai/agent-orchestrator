//go:build windows

package codexappserver

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type accountHomeACLHeader struct {
	revision byte
	padding  byte
	size     uint16
	count    uint16
	padding2 uint16
}

type accountHomeACEPrefix struct {
	header windows.ACE_HEADER
	mask   windows.ACCESS_MASK
}

// managedHomePrivate reports whether an AO-owned Codex credential home is still
// private to this user.
//
// Windows has no POSIX mode bits: a directory created with os.Mkdir(path, 0o700)
// is reported back by Lstat as 0777, so the Unix mode comparison can never
// succeed here and every managed-home open failed. AO instead protects a managed
// home with an explicit owner-only DACL when it creates it, so that is what gets
// verified: the current user owns the directory, its DACL is protected from
// inheritance, and no untrusted principal holds any allow ACE on it.
func managedHomePrivate(path string, _ os.FileInfo) bool {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	handle, err := windows.CreateFile(
		ptr,
		windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return false
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return false
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false
	}
	header := (*accountHomeACLHeader)(unsafe.Pointer(dacl))
	for index := uint32(0); index < uint32(header.count); index++ {
		var raw *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &raw); err != nil || raw == nil {
			return false
		}
		prefix := (*accountHomeACEPrefix)(unsafe.Pointer(raw))
		if prefix.header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		// Every callback and object allow ACE type is treated as an allow ACE:
		// the conditional expression is not evaluated here, so it cannot be
		// relied on to withhold the access the ACE grants.
		allowed := prefix.header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE ||
			prefix.header.AceType == 5 || prefix.header.AceType == 9 || prefix.header.AceType == 11
		if !allowed || uint32(prefix.mask) == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&raw.SidStart))
		if !sid.Equals(user.User.Sid) && !sid.Equals(system) && !sid.Equals(administrators) {
			return false
		}
	}
	return true
}
