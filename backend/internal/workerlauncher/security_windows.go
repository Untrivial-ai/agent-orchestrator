//go:build windows

package workerlauncher

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	securityAdvapi32         = windows.NewLazySystemDLL("advapi32.dll")
	procLookupPrivilegeNameW = securityAdvapi32.NewProc("LookupPrivilegeNameW")
)

type TokenDetails struct {
	SID        string   `json:"sid"`
	Elevated   bool     `json:"elevated"`
	Admin      bool     `json:"admin"`
	Privileges []string `json:"privileges"`
}

func tokenLogonSID(token windows.Token) (string, error) {
	var needed uint32
	_ = windows.GetTokenInformation(token, windows.TokenGroups, nil, 0, &needed)
	if needed == 0 {
		return "", errors.New("worker launcher: token has no group data")
	}
	buf := make([]byte, needed)
	if err := windows.GetTokenInformation(token, windows.TokenGroups, &buf[0], needed, &needed); err != nil {
		return "", err
	}
	for _, group := range (*windows.Tokengroups)(unsafe.Pointer(&buf[0])).AllGroups() {
		if group.Attributes&windows.SE_GROUP_LOGON_ID == windows.SE_GROUP_LOGON_ID {
			return group.Sid.String(), nil
		}
	}
	return "", errors.New("worker launcher: token has no Logon SID")
}

func tokenContainsEnabledSID(token windows.Token, sid *windows.SID) (bool, error) {
	var needed uint32
	_ = windows.GetTokenInformation(token, windows.TokenGroups, nil, 0, &needed)
	if needed == 0 {
		return false, errors.New("worker launcher: token has no group data")
	}
	buf := make([]byte, needed)
	if err := windows.GetTokenInformation(token, windows.TokenGroups, &buf[0], needed, &needed); err != nil {
		return false, err
	}
	for _, group := range (*windows.Tokengroups)(unsafe.Pointer(&buf[0])).AllGroups() {
		if group.Sid.Equals(sid) {
			return group.Attributes&windows.SE_GROUP_ENABLED != 0, nil
		}
	}
	return false, nil
}

func inspectToken(token windows.Token) (TokenDetails, error) {
	user, err := token.GetTokenUser()
	if err != nil {
		return TokenDetails{}, err
	}
	adminsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return TokenDetails{}, err
	}
	admin, err := tokenHasEnabledGroup(token, adminsSID)
	if err != nil {
		return TokenDetails{}, err
	}
	var needed uint32
	_ = windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &needed)
	buf := make([]byte, needed)
	privileges := []string{}
	if needed > 0 {
		if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buf[0], needed, &needed); err != nil {
			return TokenDetails{}, err
		}
		tp := (*windows.Tokenprivileges)(unsafe.Pointer(&buf[0]))
		for _, p := range tp.AllPrivileges() {
			name, lookupErr := lookupPrivilegeName(p.Luid)
			if lookupErr == nil {
				privileges = append(privileges, name)
			}
		}
	}
	return TokenDetails{SID: user.User.Sid.String(), Elevated: token.IsElevated(), Admin: admin, Privileges: privileges}, nil
}

func tokenHasEnabledGroup(token windows.Token, wanted *windows.SID) (bool, error) {
	var needed uint32
	_ = windows.GetTokenInformation(token, windows.TokenGroups, nil, 0, &needed)
	if needed == 0 {
		return false, errors.New("worker launcher: token has no group data")
	}
	buf := make([]byte, needed)
	if err := windows.GetTokenInformation(token, windows.TokenGroups, &buf[0], needed, &needed); err != nil {
		return false, err
	}
	for _, group := range (*windows.Tokengroups)(unsafe.Pointer(&buf[0])).AllGroups() {
		if group.Sid.Equals(wanted) {
			return group.Attributes&windows.SE_GROUP_ENABLED != 0 && group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0, nil
		}
	}
	return false, nil
}

func lookupPrivilegeName(luid windows.LUID) (string, error) {
	var size uint32
	procLookupPrivilegeNameW.Call(0, uintptr(unsafe.Pointer(&luid)), 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return "", syscall.EINVAL
	}
	buf := make([]uint16, size+1)
	r1, _, callErr := procLookupPrivilegeNameW.Call(0, uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r1 == 0 {
		return "", callErr
	}
	return windows.UTF16ToString(buf[:size]), nil
}

type sessionJob struct{ handle windows.Handle }

func createSessionJob() (*sessionJob, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("worker launcher: create job: %w", err)
	}
	job := &sessionJob{handle: handle}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	if _, err := windows.SetInformationJobObject(handle, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		job.Close()
		return nil, err
	}
	if err := windows.AssignProcessToJobObject(handle, windows.CurrentProcess()); err != nil {
		job.Close()
		return nil, fmt.Errorf("worker launcher: assign self to job: %w", err)
	}
	return job, nil
}

func (j *sessionJob) Close() error {
	if j == nil || j.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(j.handle)
	j.handle = 0
	return err
}

func workerSysProcAttr() *windows.SysProcAttr {
	return &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}

func currentTokenDetails() (TokenDetails, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return TokenDetails{}, err
	}
	defer token.Close()
	return inspectToken(token)
}
