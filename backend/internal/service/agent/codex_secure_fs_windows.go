//go:build windows

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type codexWindowsACLHeader struct {
	revision byte
	padding  byte
	size     uint16
	count    uint16
	padding2 uint16
}

type codexWindowsACEPrefix struct {
	header windows.ACE_HEADER
	mask   windows.ACCESS_MASK
}

func codexPrivateFileMode(os.FileInfo) bool { return true }

func openCodexFileNoFollow(path string) (*os.File, error) {
	handle, info, ownerCurrent, _, aclSafe, err := openCodexWindowsPath(path, false, true)
	if err != nil {
		return nil, err
	}
	if !codexWindowsPathMetadataIsSafe(codexWindowsMetadata(info, ownerCurrent, aclSafe), false, true) {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("codex file handle is unsafe")
	}
	return os.NewFile(uintptr(handle), filepath.Base(path)), nil
}

func validateCodexDirectory(path string, requirePrivate bool) error {
	handle, info, ownerCurrent, _, aclSafe, err := openCodexWindowsPath(path, true, requirePrivate)
	if err != nil {
		return errors.New("codex directory is unsafe")
	}
	_ = windows.CloseHandle(handle)
	if !codexWindowsPathMetadataIsSafe(codexWindowsMetadata(info, ownerCurrent, aclSafe), true, false) {
		return errors.New("codex directory owner or ACL is unsafe")
	}
	return validateCodexDirectoryAncestors(path)
}

func validateCodexDirectoryAncestors(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return errors.New("codex directory path is invalid")
	}
	for current := abs; ; current = filepath.Dir(current) {
		ptr, ptrErr := windows.UTF16PtrFromString(current)
		if ptrErr != nil {
			return errors.New("codex directory path is invalid")
		}
		attributes, attrErr := windows.GetFileAttributes(ptr)
		if errors.Is(attrErr, windows.ERROR_FILE_NOT_FOUND) || errors.Is(attrErr, windows.ERROR_PATH_NOT_FOUND) {
			if parent := filepath.Dir(current); parent != current {
				continue
			}
			return errors.New("codex directory has no trusted ancestor")
		}
		if attrErr != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return errors.New("codex directory has an unsafe ancestor")
		}
		handle, info, _, ownerTrusted, aclSafe, openErr := openCodexWindowsPath(current, true, false)
		if openErr != nil {
			return errors.New("codex directory ancestor could not be verified")
		}
		_ = windows.CloseHandle(handle)
		if !codexWindowsPathMetadataIsSafe(codexWindowsMetadata(info, ownerTrusted, aclSafe), true, false) {
			return errors.New("codex directory ancestor ACL is unsafe")
		}
		if parent := filepath.Dir(current); parent == current {
			return nil
		}
	}
}

func openCodexWindowsPath(path string, directory, requirePrivate bool) (windows.Handle, windows.ByHandleFileInformation, bool, bool, bool, error) {
	return openCodexWindowsPathWithAccess(path, directory, windows.GENERIC_READ|windows.READ_CONTROL, requirePrivate)
}

func openCodexWindowsPathWithAccess(path string, directory bool, access uint32, requirePrivate bool) (windows.Handle, windows.ByHandleFileInformation, bool, bool, bool, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, false, false, false, err
	}
	handle, err := windows.CreateFile(
		ptr,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		codexWindowsNoFollowOpenFlags(),
		0,
	)
	if err != nil {
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, false, false, false, err
	}
	fail := func(err error) (windows.Handle, windows.ByHandleFileInformation, bool, bool, bool, error) {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, false, false, false, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fail(err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return fail(errors.New("codex path is a reparse point or has the wrong type"))
	}
	ownerCurrent, ownerTrusted, aclSafe, err := codexWindowsHandleSecurity(handle, requirePrivate)
	if err != nil {
		return fail(err)
	}
	return handle, info, ownerCurrent, ownerTrusted, aclSafe, nil
}

func codexWindowsMetadata(info windows.ByHandleFileInformation, ownerTrusted, aclSafe bool) codexWindowsPathMetadata {
	return codexWindowsPathMetadata{
		Attributes: info.FileAttributes, HardLinks: info.NumberOfLinks, OwnerTrusted: ownerTrusted, ACLSafe: aclSafe,
		VolumeSerial: info.VolumeSerialNumber, FileIndexHigh: info.FileIndexHigh, FileIndexLow: info.FileIndexLow,
	}
}

func codexWindowsHandleSecurity(handle windows.Handle, requirePrivate bool) (bool, bool, bool, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false, false, false, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return false, false, false, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false, false, false, err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, false, false, err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return false, false, false, errors.New("codex path security descriptor is unavailable")
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false, false, false, errors.New("codex path owner is unavailable")
	}
	ownerCurrent := owner.Equals(user.User.Sid)
	ownerTrusted := ownerCurrent || owner.Equals(system) || owner.Equals(administrators)
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return ownerCurrent, ownerTrusted, false, nil
	}
	header := (*codexWindowsACLHeader)(unsafe.Pointer(dacl))
	aces := make([]codexWindowsACE, 0, header.count)
	for index := uint32(0); index < uint32(header.count); index++ {
		var raw *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &raw); err != nil || raw == nil {
			return ownerCurrent, ownerTrusted, false, errors.New("codex path ACL is unreadable")
		}
		prefix := (*codexWindowsACEPrefix)(unsafe.Pointer(raw))
		if prefix.header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		allowed := prefix.header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE
		ace := codexWindowsACE{Allowed: allowed, Mask: uint32(prefix.mask)}
		if allowed {
			sid := (*windows.SID)(unsafe.Pointer(&raw.SidStart))
			ace.PrincipalTrusted = sid.Equals(user.User.Sid) || sid.Equals(system) || sid.Equals(administrators)
		} else if prefix.header.AceType == 5 || prefix.header.AceType == 9 || prefix.header.AceType == 11 {
			ace.Allowed = true
		}
		aces = append(aces, ace)
	}
	aclSafe := codexWindowsAncestorACLIsSafe(ownerTrusted, aces)
	if requirePrivate {
		aclSafe = codexWindowsVaultACLIsSafe(ownerTrusted, aces)
	}
	return ownerCurrent, ownerTrusted, aclSafe, nil
}

func protectCodexPrivateDirectory(path string) error {
	handle, _, ownerCurrent, _, _, err := openCodexWindowsPathWithAccess(
		path,
		true,
		windows.WRITE_DAC|windows.READ_CONTROL,
		false,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if !ownerCurrent {
		return errors.New("codex private directory owner is unsafe")
	}
	return setCodexWindowsPrivateDACL(handle, true)
}

func protectCodexPrivateFile(path string, file *os.File) error {
	if file == nil {
		return errors.New("codex private file handle is unavailable")
	}
	var original windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &original); err != nil {
		return err
	}
	handle, opened, ownerCurrent, _, _, err := openCodexWindowsPathWithAccess(
		path,
		false,
		windows.WRITE_DAC|windows.READ_CONTROL,
		false,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if !ownerCurrent || !codexWindowsSameStableIdentity(
		codexWindowsMetadata(original, true, true),
		codexWindowsMetadata(opened, true, true),
	) {
		return errors.New("codex private file changed before ACL protection")
	}
	return setCodexWindowsPrivateDACL(handle, false)
}

func setCodexWindowsPrivateDACL(handle windows.Handle, directory bool) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entry := func(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  trusteeType,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	dacl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		entry(user.User.Sid, windows.TRUSTEE_IS_USER),
		entry(system, windows.TRUSTEE_IS_USER),
		entry(administrators, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	}, nil)
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return err
	}
	ownerCurrent, _, aclSafe, err := codexWindowsHandleSecurity(handle, true)
	if err != nil || !ownerCurrent || !aclSafe {
		return errors.New("codex private ACL could not be verified")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return errors.New("codex private ACL protection is unavailable")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("codex private ACL inheritance is not protected")
	}
	return nil
}

func syncDirectory(path string) error {
	handle, info, ownerCurrent, _, aclSafe, err := openCodexWindowsPathWithAccess(path, true, codexWindowsDirectoryFlushAccess(), false)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if !codexWindowsPathMetadataIsSafe(codexWindowsMetadata(info, ownerCurrent, aclSafe), true, false) {
		return errors.New("codex directory owner or ACL is unsafe")
	}
	return windows.FlushFileBuffers(handle)
}
