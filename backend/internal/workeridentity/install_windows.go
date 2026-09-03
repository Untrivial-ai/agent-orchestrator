//go:build windows

package workeridentity

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var accountNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,19}$`)
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,79}$`)

type InstallOptions struct {
	DataDir            string
	AccountName        string
	WorkspaceRoot      string
	SessionProfileRoot string
	LauncherPath       string
	PTYHostPath        string
	Executables        map[string]string
	GitMetadataRoots   []string
	ServiceName        string
}

func Install(opts InstallOptions) (Config, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return Config{}, err
	}
	defer token.Close()
	if !token.IsElevated() {
		return Config{}, errors.New("worker identity: installation requires an elevated administrator process")
	}
	account := strings.TrimSpace(opts.AccountName)
	if account == "" {
		account = DefaultAccountName
	}
	if !accountNamePattern.MatchString(account) {
		return Config{}, errors.New("worker identity: invalid local account name")
	}
	for name, value := range map[string]string{"data directory": opts.DataDir, "workspace root": opts.WorkspaceRoot, "profile root": opts.SessionProfileRoot, "launcher": opts.LauncherPath, "pty host": opts.PTYHostPath} {
		if strings.TrimSpace(value) == "" {
			return Config{}, fmt.Errorf("worker identity: %s is required", name)
		}
	}
	password, err := randomPassword()
	if err != nil {
		return Config{}, err
	}
	defer zero(password)
	if err := createOrRotateLocalUser(account, password); err != nil {
		return Config{}, err
	}
	sid, _, _, err := windows.LookupSID("", account)
	if err != nil {
		return Config{}, fmt.Errorf("worker identity: resolve account SID: %w", err)
	}
	if err := removeAdministratorMembership(sid); err != nil {
		return Config{}, err
	}
	if err := addAccountRights(sid, []string{
		"SeInteractiveLogonRight", "SeDenyRemoteInteractiveLogonRight", "SeDenyNetworkLogonRight",
	}); err != nil {
		return Config{}, err
	}
	// Provider OAuth/device-auth uses Windows TLS, CNG and DPAPI facilities that
	// expect a normal interactive logon. Remove rights left by the earlier batch
	// worker prototype when rotating an existing account.
	if err := removeAccountRights(sid, []string{"SeBatchLogonRight", "SeDenyInteractiveLogonRight"}); err != nil {
		return Config{}, err
	}
	hostSID, err := currentUserSID()
	if err != nil {
		return Config{}, fmt.Errorf("worker identity: resolve host SID: %w", err)
	}

	workspaceRoot, err := canonicalDirectory(opts.WorkspaceRoot, true)
	if err != nil {
		return Config{}, fmt.Errorf("worker identity: workspace root: %w", err)
	}
	profileRoot, err := canonicalDirectory(opts.SessionProfileRoot, true)
	if err != nil {
		return Config{}, fmt.Errorf("worker identity: profile root: %w", err)
	}
	launcher, err := canonicalFile(opts.LauncherPath)
	if err != nil {
		return Config{}, fmt.Errorf("worker identity: launcher: %w", err)
	}
	ptyHost, err := canonicalFile(opts.PTYHostPath)
	if err != nil {
		return Config{}, fmt.Errorf("worker identity: pty host: %w", err)
	}
	executables := make(map[string]string, len(opts.Executables))
	executableSHA256 := make(map[string]string, len(opts.Executables))
	for id, path := range opts.Executables {
		canonical, fileErr := canonicalFile(path)
		if fileErr != nil {
			return Config{}, fmt.Errorf("worker identity: executable %q: %w", id, fileErr)
		}
		executables[id] = canonical
		digest, hashErr := fileSHA256(canonical)
		if hashErr != nil {
			return Config{}, fmt.Errorf("worker identity: hash executable %q: %w", id, hashErr)
		}
		executableSHA256[id] = digest
	}
	gitMetadataRoots := make([]string, 0, len(opts.GitMetadataRoots))
	for _, path := range opts.GitMetadataRoots {
		canonical, dirErr := canonicalDirectory(path, false)
		if dirErr != nil {
			return Config{}, fmt.Errorf("worker identity: Git metadata root: %w", dirErr)
		}
		gitMetadataRoots = append(gitMetadataRoots, canonical)
	}

	securityDir := filepath.Join(opts.DataDir, "security")
	if err := os.MkdirAll(securityDir, 0o700); err != nil {
		return Config{}, err
	}
	secretPath := filepath.Join(securityDir, SecretFileName)
	if err := writeProtectedSecret(secretPath, password); err != nil {
		return Config{}, err
	}
	serviceName := strings.TrimSpace(opts.ServiceName)
	if serviceName == "" {
		serviceName = "AOAgentWorkerLauncher"
	}
	if !serviceNamePattern.MatchString(serviceName) {
		return Config{}, errors.New("worker identity: invalid service name")
	}
	pipeHash := sha256.Sum256([]byte(strings.ToUpper(filepath.Clean(opts.DataDir))))
	pipeName := fmt.Sprintf(`\\.\pipe\ao-worker-launcher-%x`, pipeHash[:8])
	cfg := Config{Version: ConfigVersion, AccountName: account, AccountSID: sid.String(), HostSID: hostSID.String(), WorkspaceRoot: workspaceRoot,
		SessionProfileRoot: profileRoot, LauncherPath: launcher, PTYHostPath: ptyHost, Executables: executables,
		ExecutableSHA256: executableSHA256,
		GitMetadataRoots: gitMetadataRoots, ServiceName: serviceName, PipeName: pipeName, SecretPath: secretPath}
	if err := writeConfig(ConfigPath(opts.DataDir), cfg); err != nil {
		return Config{}, err
	}
	if err := SecureContainerRoot(workspaceRoot, cfg.AccountSID); err != nil {
		return Config{}, fmt.Errorf("worker identity: workspace root ACL: %w", err)
	}
	if err := SecureContainerRoot(profileRoot, cfg.AccountSID); err != nil {
		return Config{}, fmt.Errorf("worker identity: profile root ACL: %w", err)
	}
	if err := SecureRuntimeFile(launcher, cfg.AccountSID); err != nil {
		return Config{}, fmt.Errorf("worker identity: launcher ACL: %w", err)
	}
	if err := SecureRuntimeFile(ptyHost, cfg.AccountSID); err != nil {
		return Config{}, fmt.Errorf("worker identity: pty host ACL: %w", err)
	}
	if err := installLauncherService(cfg, ConfigPath(opts.DataDir)); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func canonicalDirectory(path string, create bool) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return "", err
		}
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(abs, resolved) {
		return "", errors.New("junction/symlink/short-name aliases are not allowed")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("not a directory")
	}
	return resolved, nil
}

func canonicalFile(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(abs, resolved) {
		return "", errors.New("junction/symlink/short-name aliases are not allowed")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	return resolved, nil
}

func createOrRotateLocalUser(name string, password []byte) error {
	name16, _ := windows.UTF16PtrFromString(name)
	password16, _ := windows.UTF16PtrFromString(string(password))
	defer zeroUTF16(password16, len([]rune(string(password)))+1)
	comment16, _ := windows.UTF16PtrFromString("Agent Orchestrator isolated worker identity")
	const flags = 0x00000001 | 0x00000040 | 0x00000200 | 0x00010000
	info := userInfo1{Name: name16, Password: password16, Privilege: 1, Comment: comment16, Flags: flags}
	var parameterError uint32
	status, _, _ := procNetUserAdd.Call(0, 1, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&parameterError)))
	if status == 2224 {
		passwordInfo := userInfo1003{Password: password16}
		status, _, _ = procNetUserSetInfo.Call(0, uintptr(unsafe.Pointer(name16)), 1003, uintptr(unsafe.Pointer(&passwordInfo)), uintptr(unsafe.Pointer(&parameterError)))
		if status == 0 {
			flagInfo := userInfo1008{Flags: flags}
			status, _, _ = procNetUserSetInfo.Call(0, uintptr(unsafe.Pointer(name16)), 1008, uintptr(unsafe.Pointer(&flagInfo)), uintptr(unsafe.Pointer(&parameterError)))
		}
	}
	if status != 0 {
		return fmt.Errorf("worker identity: NetUserAdd/SetInfo parameter %d: %w", parameterError, windows.Errno(status))
	}
	return nil
}

var netapi32 = windows.NewLazySystemDLL("netapi32.dll")
var procNetLocalGroupDelMembers = netapi32.NewProc("NetLocalGroupDelMembers")
var procNetUserAdd = netapi32.NewProc("NetUserAdd")
var procNetUserSetInfo = netapi32.NewProc("NetUserSetInfo")

type userInfo1 struct {
	Name, Password         *uint16
	PasswordAge, Privilege uint32
	HomeDir, Comment       *uint16
	Flags                  uint32
	ScriptPath             *uint16
}
type userInfo1003 struct{ Password *uint16 }
type userInfo1008 struct{ Flags uint32 }

type localGroupMembersInfo0 struct{ SID *windows.SID }

func removeAdministratorMembership(memberSID *windows.SID) error {
	adminsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	group, _, _, err := adminsSID.LookupAccount("")
	if err != nil {
		return err
	}
	group16, _ := windows.UTF16PtrFromString(group)
	info := localGroupMembersInfo0{SID: memberSID}
	status, _, _ := procNetLocalGroupDelMembers.Call(0, uintptr(unsafe.Pointer(group16)), 0, uintptr(unsafe.Pointer(&info)), 1)
	if status != 0 && status != 1377 && status != 1387 {
		return fmt.Errorf("worker identity: remove Administrators membership: %w", windows.Errno(status))
	}
	return nil
}

func zeroUTF16(ptr *uint16, count int) {
	if ptr == nil || count <= 0 {
		return
	}
	values := unsafe.Slice(ptr, count)
	for i := range values {
		values[i] = 0
	}
}

func writeConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".worker-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	workerSID, err := windows.StringToSid(cfg.AccountSID)
	if err != nil {
		return err
	}
	return setExactACL(path, workerSID, nil, false, false)
}

type lsaUnicodeString struct {
	Length, MaximumLength uint16
	Buffer                *uint16
}
type lsaObjectAttributes struct {
	Length                                       uint32
	RootDirectory                                uintptr
	Attributes                                   uint32
	SecurityDescriptor, SecurityQualityOfService uintptr
}

var (
	advapi32                   = windows.NewLazySystemDLL("advapi32.dll")
	procLsaOpenPolicy          = advapi32.NewProc("LsaOpenPolicy")
	procLsaAddAccountRights    = advapi32.NewProc("LsaAddAccountRights")
	procLsaRemoveAccountRights = advapi32.NewProc("LsaRemoveAccountRights")
	procLsaClose               = advapi32.NewProc("LsaClose")
	procLsaNtStatusToWinError  = advapi32.NewProc("LsaNtStatusToWinError")
)

func addAccountRights(sid *windows.SID, names []string) error {
	attrs := lsaObjectAttributes{Length: uint32(unsafe.Sizeof(lsaObjectAttributes{}))}
	var policy windows.Handle
	status, _, _ := procLsaOpenPolicy.Call(0, uintptr(unsafe.Pointer(&attrs)), 0x00000810, uintptr(unsafe.Pointer(&policy)))
	if status != 0 {
		return lsaStatusError("open policy", status)
	}
	defer procLsaClose.Call(uintptr(policy))
	rights := make([]lsaUnicodeString, len(names))
	buffers := make([][]uint16, len(names))
	for i, name := range names {
		buf, err := windows.UTF16FromString(name)
		if err != nil {
			return err
		}
		buffers[i] = buf
		rights[i] = lsaUnicodeString{Length: uint16((len(buf) - 1) * 2), MaximumLength: uint16(len(buf) * 2), Buffer: &buf[0]}
	}
	status, _, _ = procLsaAddAccountRights.Call(uintptr(policy), uintptr(unsafe.Pointer(sid)), uintptr(unsafe.Pointer(&rights[0])), uintptr(len(rights)))
	runtime.KeepAlive(buffers)
	if status != 0 {
		return lsaStatusError("add logon rights", status)
	}
	return nil
}

func removeAccountRights(sid *windows.SID, names []string) error {
	attrs := lsaObjectAttributes{Length: uint32(unsafe.Sizeof(lsaObjectAttributes{}))}
	var policy windows.Handle
	status, _, _ := procLsaOpenPolicy.Call(0, uintptr(unsafe.Pointer(&attrs)), 0x00000810, uintptr(unsafe.Pointer(&policy)))
	if status != 0 {
		return lsaStatusError("open policy", status)
	}
	defer procLsaClose.Call(uintptr(policy))
	rights := make([]lsaUnicodeString, len(names))
	buffers := make([][]uint16, len(names))
	for i, name := range names {
		buf, err := windows.UTF16FromString(name)
		if err != nil {
			return err
		}
		buffers[i] = buf
		rights[i] = lsaUnicodeString{Length: uint16((len(buf) - 1) * 2), MaximumLength: uint16(len(buf) * 2), Buffer: &buf[0]}
	}
	status, _, _ = procLsaRemoveAccountRights.Call(uintptr(policy), uintptr(unsafe.Pointer(sid)), 0,
		uintptr(unsafe.Pointer(&rights[0])), uintptr(len(rights)))
	runtime.KeepAlive(buffers)
	// STATUS_OBJECT_NAME_NOT_FOUND means the account did not have the right.
	if status != 0 && status != 0xC0000034 {
		return lsaStatusError("remove incompatible logon rights", status)
	}
	return nil
}

func lsaStatusError(action string, status uintptr) error {
	code, _, _ := procLsaNtStatusToWinError.Call(status)
	return fmt.Errorf("worker identity: %s: %w", action, windows.Errno(code))
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
