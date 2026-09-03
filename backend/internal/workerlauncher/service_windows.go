//go:build windows

package workerlauncher

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/aoagents/agent-orchestrator/backend/internal/workeridentity"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const logon32LogonInteractive = 2
const logon32ProviderDefault = 0

var serviceAdvapi32 = windows.NewLazySystemDLL("advapi32.dll")
var procLogonUserW = serviceAdvapi32.NewProc("LogonUserW")
var serviceUserenv = windows.NewLazySystemDLL("userenv.dll")
var procLoadUserProfileW = serviceUserenv.NewProc("LoadUserProfileW")
var procUnloadUserProfile = serviceUserenv.NewProc("UnloadUserProfile")
var procGetUserProfileDirectoryW = serviceUserenv.NewProc("GetUserProfileDirectoryW")

type profileInfo struct {
	Size, Flags                                                uint32
	UserName, ProfilePath, DefaultPath, ServerName, PolicyPath *uint16
	Profile                                                    windows.Handle
}

func RunLauncherService(configPath string) error {
	cfg, err := workeridentity.LoadConfig(configPath)
	if err != nil {
		return err
	}
	return svc.Run(cfg.ServiceName, &launcherService{configPath: configPath, cfg: cfg})
}

type launcherService struct {
	configPath string
	cfg        workeridentity.Config
	listener   net.Listener
	closeOnce  sync.Once
}

func (s *launcherService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}
	securityDescriptor := "D:P(A;;GA;;;SY)(A;;GA;;;" + s.cfg.HostSID + ")"
	listener, err := winio.ListenPipe(s.cfg.PipeName, &winio.PipeConfig{SecurityDescriptor: securityDescriptor,
		MessageMode: false, InputBufferSize: 64 * 1024, OutputBufferSize: 64 * 1024})
	if err != nil {
		return true, 1
	}
	s.listener = listener
	go s.acceptLoop()
	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for request := range requests {
		switch request.Cmd {
		case svc.Interrogate:
			status <- request.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			s.closeOnce.Do(func() { _ = listener.Close() })
			return false, 0
		}
	}
	return false, 0
}

func (s *launcherService) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *launcherService) handle(conn net.Conn) {
	defer conn.Close()
	decoder := json.NewDecoder(bufio.NewReader(io.LimitReader(conn, 1024*1024)))
	var request ServiceLaunchRequest
	if err := decoder.Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(ServiceLaunchResponse{Error: "worker launcher service: invalid request"})
		return
	}
	defer zeroBytes(request.Password)
	err := s.launch(request)
	response := ServiceLaunchResponse{}
	if err != nil {
		response.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(response)
}

func (s *launcherService) launch(request ServiceLaunchRequest) error {
	manifest, err := ReadAndValidateManifest(request.ManifestPath, s.cfg)
	if err != nil {
		return err
	}
	if strings.TrimSpace(request.ReadyAddress) == "" || !noncePattern.MatchString(request.ReadyNonce) {
		return errors.New("worker launcher service: invalid readiness channel")
	}
	allowed := AllowedEnvironmentKeys()
	manifestKeys := map[string]struct{}{}
	for _, key := range manifest.AllowedEnvironmentKeys {
		manifestKeys[key] = struct{}{}
	}
	for key := range request.Environment {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("worker launcher service: environment key %q is not allowed", key)
		}
		if _, ok := manifestKeys[key]; !ok {
			return fmt.Errorf("worker launcher service: environment key %q is absent from manifest", key)
		}
	}
	token, err := logonInteractive(s.cfg.AccountName, request.Password)
	if err != nil {
		return err
	}
	profile, err := loadUserProfile(token, s.cfg.AccountName)
	if err != nil {
		token.Close()
		return err
	}
	profileRoot, err := userProfileDirectory(token)
	if err != nil {
		unloadUserProfile(token, profile)
		token.Close()
		return err
	}
	logonSID, err := tokenLogonSID(token)
	if err != nil {
		unloadUserProfile(token, profile)
		token.Close()
		return err
	}
	for _, root := range []string{manifest.WorkerProfileRoot, manifest.CanonicalWorktree} {
		if err := workeridentity.SecureSessionTreeForLogon(root, logonSID, true); err != nil {
			unloadUserProfile(token, profile)
			token.Close()
			return err
		}
	}
	if err := secureLinkedGitMetadata(manifest.CanonicalWorktree, s.cfg, logonSID); err != nil {
		unloadUserProfile(token, profile)
		token.Close()
		return err
	}
	args := []string{"worker", "--config", s.configPath, "--manifest", request.ManifestPath,
		"--ready", request.ReadyAddress, "--ready-nonce", request.ReadyNonce, "--logon-sid", logonSID,
		"--profile-root", profileRoot}
	cmd := exec.Command(s.cfg.LauncherPath, args...)
	cmd.Dir = manifest.WorkerProfileRoot
	cmd.Env = buildWorkerEnvironment(manifest, s.cfg, profileRoot, request.Environment)
	cmd.SysProcAttr = &windows.SysProcAttr{Token: syscall.Token(token), CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	if err := cmd.Start(); err != nil {
		unloadUserProfile(token, profile)
		token.Close()
		return fmt.Errorf("worker launcher service: start interactive worker: %w", err)
	}
	go func() { _ = cmd.Wait(); _ = unloadUserProfile(token, profile); _ = token.Close() }()
	return nil
}

func logonInteractive(username string, password []byte) (windows.Token, error) {
	user16, _ := windows.UTF16PtrFromString(username)
	domain16, _ := windows.UTF16PtrFromString(".")
	password16, _ := windows.UTF16PtrFromString(string(password))
	defer zeroUTF16Slice(password16, len([]rune(string(password)))+1)
	var token windows.Token
	r1, _, callErr := procLogonUserW.Call(uintptr(unsafe.Pointer(user16)), uintptr(unsafe.Pointer(domain16)), uintptr(unsafe.Pointer(password16)),
		logon32LogonInteractive, logon32ProviderDefault, uintptr(unsafe.Pointer(&token)))
	if r1 == 0 {
		return 0, fmt.Errorf("worker launcher service: interactive logon: %w", callErr)
	}
	return token, nil
}

func userProfileDirectory(token windows.Token) (string, error) {
	var size uint32
	procGetUserProfileDirectoryW.Call(uintptr(token), 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return "", errors.New("worker launcher service: empty Windows profile directory")
	}
	buf := make([]uint16, size)
	r1, _, callErr := procGetUserProfileDirectoryW.Call(uintptr(token), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r1 == 0 {
		return "", fmt.Errorf("worker launcher service: GetUserProfileDirectoryW: %w", callErr)
	}
	return strings.TrimSpace(windows.UTF16ToString(buf)), nil
}

func loadUserProfile(token windows.Token, username string) (windows.Handle, error) {
	user16, _ := windows.UTF16PtrFromString(username)
	info := profileInfo{Size: uint32(unsafe.Sizeof(profileInfo{})), UserName: user16}
	r1, _, callErr := procLoadUserProfileW.Call(uintptr(token), uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		return 0, fmt.Errorf("worker launcher service: LoadUserProfileW: %w", callErr)
	}
	return info.Profile, nil
}

func unloadUserProfile(token windows.Token, profile windows.Handle) error {
	r1, _, callErr := procUnloadUserProfile.Call(uintptr(token), uintptr(profile))
	if r1 == 0 {
		return callErr
	}
	return nil
}

func zeroUTF16Slice(ptr *uint16, count int) {
	if ptr == nil || count <= 0 {
		return
	}
	values := unsafe.Slice(ptr, count)
	for i := range values {
		values[i] = 0
	}
}
