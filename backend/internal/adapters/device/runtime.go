// Package device adapts AO's neutral device contract to the pinned
// agent-device helper shipped with the desktop app.
package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// RuntimeDirEnv selects AO's bundled device helper directory.
	RuntimeDirEnv = "AO_DEVICE_RUNTIME_DIR"
	// ACPRuntimeEnv selects AO's bundled Node runtime directory.
	ACPRuntimeEnv = "AO_ACP_RUNTIME_DIR"
	maxOutput     = 16 << 20
)

// CommandRunner permits deterministic adapter tests without spawning helpers.
type CommandRunner func(context.Context, string, []string, []byte, []string) ([]byte, []byte, error)

// Runtime invokes the audited AO wrapper with AO-owned state and bounded output.
type Runtime struct {
	runtimeDir string
	nodePath   string
	dataDir    string
	stateDir   string
	run        CommandRunner
	lookPath   func(string) (string, error)
	goos       string
}

// New creates a local device runtime. Missing resources are reported as
// capabilities rather than preventing the AO daemon from starting.
func New(dataDir string) *Runtime {
	runtimeDir := strings.TrimSpace(os.Getenv(RuntimeDirEnv))
	acpDir := strings.TrimSpace(os.Getenv(ACPRuntimeEnv))
	node := filepath.Join(acpDir, "node", "bin", "node")
	if runtime.GOOS == "windows" {
		node = filepath.Join(acpDir, "node", "node.exe")
	}
	return &Runtime{
		runtimeDir: runtimeDir,
		nodePath:   node,
		dataDir:    dataDir,
		stateDir:   filepath.Join(dataDir, "devices", "agent-device"),
		run:        runCommand,
		lookPath:   exec.LookPath,
		goos:       runtime.GOOS,
	}
}

// Capabilities reports the host/runtime gate independently for iOS and Android.
func (r *Runtime) Capabilities(context.Context) []domain.DevicePlatformCapability {
	if r.goos != "darwin" {
		return unavailableBoth("HOST_PLATFORM_UNSUPPORTED", "Local virtual devices are currently available only on macOS")
	}
	if !regularFile(r.nodePath) || !regularFile(filepath.Join(r.runtimeDir, "ao-device-runner.mjs")) {
		return unavailableBoth("DEVICE_RUNTIME_UNAVAILABLE", "The AO desktop device runtime is not installed")
	}
	return []domain.DevicePlatformCapability{r.iosCapability(), r.androidCapability()}
}

func (r *Runtime) iosCapability() domain.DevicePlatformCapability {
	if _, err := r.lookPath("xcrun"); err != nil {
		return domain.DevicePlatformCapability{Platform: domain.DevicePlatformIOS, Code: "XCODE_REQUIRED", Message: "Install Xcode and its command-line tools to use iOS Simulator"}
	}
	return domain.DevicePlatformCapability{Platform: domain.DevicePlatformIOS, Available: true}
}

func (r *Runtime) androidCapability() domain.DevicePlatformCapability {
	if regularFile(filepath.Join(r.androidSDKDir(), "platform-tools", "adb")) &&
		regularFile(filepath.Join(r.androidSDKDir(), "emulator", "emulator")) {
		return domain.DevicePlatformCapability{Platform: domain.DevicePlatformAndroid, Available: true}
	}
	if _, err := r.lookPath("adb"); err == nil {
		return domain.DevicePlatformCapability{Platform: domain.DevicePlatformAndroid, Available: true}
	}
	for _, root := range []string{os.Getenv("ANDROID_HOME"), os.Getenv("ANDROID_SDK_ROOT")} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if regularFile(filepath.Join(root, "platform-tools", "adb")) {
			return domain.DevicePlatformCapability{Platform: domain.DevicePlatformAndroid, Available: true}
		}
	}
	return domain.DevicePlatformCapability{Platform: domain.DevicePlatformAndroid, Code: "ANDROID_SDK_REQUIRED", Message: "Install Android Studio and an Android SDK platform-tools package to use Android Emulator"}
}

// List returns the helper's normalized inventory for one platform.
func (r *Runtime) List(ctx context.Context, platform domain.DevicePlatform) ([]domain.Device, error) {
	raw, err := r.Execute(ctx, ports.DeviceRuntimeRequest{Action: "list", Platform: platform})
	if err != nil {
		return nil, err
	}
	var devices []struct {
		ID       string                `json:"id"`
		Name     string                `json:"name"`
		Platform domain.DevicePlatform `json:"platform"`
		Kind     domain.DeviceKind     `json:"kind"`
		Booted   bool                  `json:"booted"`
	}
	if err := json.Unmarshal(raw, &devices); err != nil {
		return nil, fmt.Errorf("decode device inventory: %w", err)
	}
	result := make([]domain.Device, 0, len(devices))
	for _, item := range devices {
		if item.ID == "" || item.Name == "" || item.Platform != platform {
			continue
		}
		result = append(result, domain.Device{
			ID: item.ID, Name: item.Name, Platform: item.Platform, Kind: item.Kind, Booted: item.Booted,
		})
	}
	return result, nil
}

// Execute sends one typed request to AO's fixed helper wrapper.
func (r *Runtime) Execute(ctx context.Context, request ports.DeviceRuntimeRequest) (json.RawMessage, error) {
	if !regularFile(r.nodePath) || !regularFile(filepath.Join(r.runtimeDir, "ao-device-runner.mjs")) {
		return nil, &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "The AO desktop device runtime is not installed"}
	}
	input, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode device helper request: %w", err)
	}
	if err := os.MkdirAll(r.stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create device state directory: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env := []string{
		"AGENT_DEVICE_STATE_DIR=" + r.stateDir,
		"AGENT_DEVICE_CLAIMS_DIR=" + filepath.Join(r.stateDir, "claims"),
		"AGENT_DEVICE_IOS_RUNNER_LEASE_DIR=" + filepath.Join(r.stateDir, "apple-runner", "leases"),
		"AGENT_DEVICE_IOS_RUNNER_DERIVED_PATH=" + filepath.Join(r.stateDir, "apple-runner", "derived"),
		"AGENT_DEVICE_SWIFT_CACHE_DIR=" + filepath.Join(r.stateDir, "swift-cache"),
		"AGENT_DEVICE_DAEMON_IDLE_TIMEOUT_MS=300000",
		"AGENT_DEVICE_NO_UPDATE_NOTIFIER=1",
	}
	stdout, stderr, runErr := r.run(requestCtx, r.nodePath, []string{filepath.Join(r.runtimeDir, "ao-device-runner.mjs")}, input, append(r.managedAndroidEnv(), env...))
	if len(stdout) > maxOutput || len(stderr) > maxOutput {
		return nil, &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "Device helper output exceeded AO's limit"}
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &envelope); err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return nil, &ports.DeviceRuntimeError{Code: "DEVICE_BOOT_TIMEOUT", Message: "The device operation timed out"}
		}
		return nil, &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "The device helper returned an invalid response"}
	}
	if !envelope.OK {
		return nil, normalizeError(envelope.Error.Code, envelope.Error.Message)
	}
	if runErr != nil && len(envelope.Result) == 0 {
		return nil, &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "The device helper exited unexpectedly"}
	}
	return envelope.Result, nil
}

func (r *Runtime) androidRoot() string   { return filepath.Join(r.dataDir, "devices", "android") }
func (r *Runtime) androidSDKDir() string { return filepath.Join(r.androidRoot(), "sdk") }
func (r *Runtime) androidAVDDir() string { return filepath.Join(r.androidRoot(), "avd") }

func (r *Runtime) managedAndroidEnv() []string {
	sdk := r.androidSDKDir()
	if !regularFile(filepath.Join(sdk, "platform-tools", "adb")) {
		return nil
	}
	path := strings.Join([]string{
		filepath.Join(sdk, "platform-tools"), filepath.Join(sdk, "emulator"), os.Getenv("PATH"),
	}, string(os.PathListSeparator))
	return []string{
		"ANDROID_HOME=" + sdk, "ANDROID_SDK_ROOT=" + sdk, "ANDROID_AVD_HOME=" + r.androidAVDDir(), "PATH=" + path,
	}
}

func normalizeError(code, _ string) error {
	switch code {
	case "DEVICE_NOT_FOUND":
		return &ports.DeviceRuntimeError{Code: "DEVICE_NOT_FOUND", Message: "The selected virtual device was not found"}
	case "DEVICE_IN_USE":
		return &ports.DeviceRuntimeError{Code: "DEVICE_BUSY", Message: "The selected virtual device is already in use"}
	case "TOOL_MISSING":
		return &ports.DeviceRuntimeError{Code: "DEVICE_TOOLCHAIN_REQUIRED", Message: "The required platform toolchain is not installed"}
	case "UNSUPPORTED_PLATFORM", "UNSUPPORTED_OPERATION", "NOT_IMPLEMENTED":
		return &ports.DeviceRuntimeError{Code: "DEVICE_ACTION_UNSUPPORTED", Message: "This device does not support the requested action"}
	case "INVALID_ARGS":
		return &ports.DeviceRuntimeError{Code: "INVALID_ARGUMENT", Message: "The device action arguments are invalid"}
	default:
		return &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "The local device runtime could not complete the operation"}
	}
}

func unavailableBoth(code, message string) []domain.DevicePlatformCapability {
	return []domain.DevicePlatformCapability{
		{Platform: domain.DevicePlatformIOS, Code: code, Message: message},
		{Platform: domain.DevicePlatformAndroid, Code: code, Message: message},
	}
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func runCommand(ctx context.Context, command string, args []string, stdin []byte, env []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = mergeEnvironment(deviceHostEnv(), env)
	stdout, stderr := newBoundedBuffer(maxOutput), newBoundedBuffer(maxOutput)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// deviceHostEnv deliberately excludes unrelated AO capabilities and API keys
// from the third-party helper while retaining the platform-toolchain settings
// needed to discover Xcode simulators and Android emulators.
func deviceHostEnv() []string {
	allowed := map[string]struct{}{
		"ANDROID_AVD_HOME": {}, "ANDROID_HOME": {}, "ANDROID_SDK_ROOT": {}, "ANDROID_USER_HOME": {},
		"DEVELOPER_DIR": {}, "HOME": {}, "JAVA_HOME": {}, "LANG": {}, "LC_ALL": {}, "LC_CTYPE": {},
		"LOGNAME": {}, "PATH": {}, "SDKROOT": {}, "SHELL": {}, "TMPDIR": {}, "USER": {},
		"XPC_FLAGS": {}, "XPC_SERVICE_NAME": {},
	}
	result := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if _, keep := allowed[name]; ok && keep {
			result = append(result, entry)
		}
	}
	return result
}

func mergeEnvironment(base, overrides []string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	order := make([]string, 0, len(base)+len(overrides))
	for _, entry := range append(append([]string{}, base...), overrides...) {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := values[name]; !exists {
			order = append(order, name)
		}
		values[name] = entry
	}
	result := make([]string, 0, len(order))
	for _, name := range order {
		result = append(result, values[name])
	}
	return result
}

// boundedBuffer retains max+1 bytes so the caller can detect overflow without
// allowing an unexpectedly noisy helper to grow daemon memory without bound.
type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func newBoundedBuffer(maxBytes int) boundedBuffer { return boundedBuffer{limit: maxBytes + 1} }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if remaining < len(p) {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return written, nil
}
