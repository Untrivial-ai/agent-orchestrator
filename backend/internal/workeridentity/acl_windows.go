//go:build windows

package workeridentity

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func currentUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.StringToSid(user.User.Sid.String())
}

func baseACL(workerSID, logonSID *windows.SID, workerWrite, inherit bool) (*windows.ACL, error) {
	hostSID, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	adminsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	workerMask := windows.ACCESS_MASK(windows.GENERIC_READ | windows.GENERIC_EXECUTE)
	if workerWrite {
		workerMask |= windows.ACCESS_MASK(windows.GENERIC_WRITE | windows.DELETE)
	}
	inheritance := uint32(0)
	if inherit {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entry := func(sid *windows.SID, mask windows.ACCESS_MASK, trusteeType windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{AccessPermissions: mask, AccessMode: windows.SET_ACCESS, Inheritance: inheritance,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: trusteeType, TrusteeValue: windows.TrusteeValueFromSID(sid)}}
	}
	entries := []windows.EXPLICIT_ACCESS{
		entry(hostSID, windows.ACCESS_MASK(windows.GENERIC_ALL), windows.TRUSTEE_IS_USER),
		entry(systemSID, windows.ACCESS_MASK(windows.GENERIC_ALL), windows.TRUSTEE_IS_USER),
		entry(adminsSID, windows.ACCESS_MASK(windows.GENERIC_ALL), windows.TRUSTEE_IS_GROUP),
	}
	if workerSID != nil {
		entries = append(entries, entry(workerSID, workerMask, windows.TRUSTEE_IS_USER))
	}
	if logonSID != nil {
		entries = append(entries, entry(logonSID, workerMask, windows.TRUSTEE_IS_USER))
	}
	return windows.ACLFromEntries(entries, nil)
}

func SecureHostTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("worker identity: reparse/symlink entry rejected: %s", path)
		}
		return setExactACL(path, nil, nil, false, entry.IsDir())
	})
}

func SecureSessionTreeForLogon(root, logonSIDString string, workerWrite bool) error {
	logonSID, err := windows.StringToSid(logonSIDString)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("worker identity: reparse/symlink entry rejected: %s", path)
		}
		return setExactACL(path, nil, logonSID, workerWrite, entry.IsDir())
	})
}

func setExactACL(path string, workerSID, logonSID *windows.SID, workerWrite, inherit bool) error {
	acl, err := baseACL(workerSID, logonSID, workerWrite, inherit)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
}

// SecureContainerRoot lets the worker traverse to its explicitly granted
// session directory without granting directory enumeration or access to a
// sibling session.
func SecureContainerRoot(root, workerSIDString string) error {
	workerSID, err := windows.StringToSid(workerSIDString)
	if err != nil {
		return err
	}
	hostSID, err := currentUserSID()
	if err != nil {
		return err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	adminsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	entry := func(sid *windows.SID, mask windows.ACCESS_MASK, typ windows.TRUSTEE_TYPE, inheritance uint32) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{AccessPermissions: mask, AccessMode: windows.SET_ACCESS, Inheritance: inheritance,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: typ, TrusteeValue: windows.TrusteeValueFromSID(sid)}}
	}
	hostInheritance := uint32(windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		entry(hostSID, windows.ACCESS_MASK(windows.GENERIC_ALL), windows.TRUSTEE_IS_USER, hostInheritance),
		entry(systemSID, windows.ACCESS_MASK(windows.GENERIC_ALL), windows.TRUSTEE_IS_USER, hostInheritance),
		entry(adminsSID, windows.ACCESS_MASK(windows.GENERIC_ALL), windows.TRUSTEE_IS_GROUP, hostInheritance),
		entry(workerSID, windows.ACCESS_MASK(windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE), windows.TRUSTEE_IS_USER, 0),
	}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func SecureRuntimeFile(path, workerSIDString string) error {
	workerSID, err := windows.StringToSid(workerSIDString)
	if err != nil {
		return err
	}
	return setExactACL(path, workerSID, nil, false, false)
}

func secureHostOnlyFile(path string) error {
	hostSID, err := currentUserSID()
	if err != nil {
		return err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	adminsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	entry := func(sid *windows.SID, typ windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{AccessPermissions: windows.ACCESS_MASK(windows.GENERIC_ALL), AccessMode: windows.SET_ACCESS,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: typ, TrusteeValue: windows.TrusteeValueFromSID(sid)}}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		entry(hostSID, windows.TRUSTEE_IS_USER), entry(systemSID, windows.TRUSTEE_IS_USER), entry(adminsSID, windows.TRUSTEE_IS_GROUP),
	}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
