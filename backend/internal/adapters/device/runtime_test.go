package device

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCapabilitiesGateHostRuntimeAndToolchainsIndependently(t *testing.T) {
	t.Setenv("ANDROID_HOME", "")
	t.Setenv("ANDROID_SDK_ROOT", "")
	runtime := testRuntime(t)
	runtime.goos = "linux"
	capabilities := runtime.Capabilities(context.Background())
	if capabilities[0].Code != "HOST_PLATFORM_UNSUPPORTED" || capabilities[1].Code != "HOST_PLATFORM_UNSUPPORTED" {
		t.Fatalf("unsupported host capabilities = %#v", capabilities)
	}

	runtime.goos = "darwin"
	runtime.lookPath = func(name string) (string, error) {
		if name == "xcrun" {
			return "/usr/bin/xcrun", nil
		}
		return "", errors.New("missing")
	}
	capabilities = runtime.Capabilities(context.Background())
	if !capabilities[0].Available || capabilities[1].Code != "ANDROID_SDK_REQUIRED" {
		t.Fatalf("independent toolchain capabilities = %#v", capabilities)
	}
}

func TestListFiltersUnexpectedHelperDevicesAndConfinesState(t *testing.T) {
	runtime := testRuntime(t)
	var input []byte
	var env []string
	runtime.run = func(_ context.Context, _ string, _ []string, stdin []byte, commandEnv []string) ([]byte, []byte, error) {
		input, env = stdin, commandEnv
		return []byte(`{"ok":true,"result":[{"id":"ios-1","name":"iPhone","platform":"ios","kind":"simulator","booted":true},{"id":"android-1","name":"Pixel","platform":"android","kind":"emulator"},{"id":"","name":"invalid","platform":"ios"}]}`), nil, nil
	}
	devices, err := runtime.List(context.Background(), domain.DevicePlatformIOS)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != "ios-1" || !devices[0].Booted {
		t.Fatalf("devices = %#v", devices)
	}
	var request ports.DeviceRuntimeRequest
	if err := json.Unmarshal(input, &request); err != nil || request.Action != "list" || request.Platform != domain.DevicePlatformIOS {
		t.Fatalf("request = %#v, err = %v", request, err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "AGENT_DEVICE_STATE_DIR="+runtime.stateDir) || !strings.Contains(joined, "AGENT_DEVICE_DAEMON_IDLE_TIMEOUT_MS=300000") {
		t.Fatalf("helper environment did not confine state: %s", joined)
	}
}

func TestExecuteRedactsHelperFailures(t *testing.T) {
	runtime := testRuntime(t)
	runtime.run = func(context.Context, string, []string, []byte, []string) ([]byte, []byte, error) {
		return []byte(`{"ok":false,"error":{"code":"TOOL_MISSING","message":"secret path /Users/alice"}}`), []byte("private stderr"), errors.New("exit 1")
	}
	_, err := runtime.Execute(context.Background(), ports.DeviceRuntimeRequest{Action: "list", Platform: domain.DevicePlatformIOS})
	var runtimeErr *ports.DeviceRuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != "DEVICE_TOOLCHAIN_REQUIRED" || strings.Contains(runtimeErr.Message, "alice") {
		t.Fatalf("error = %#v", err)
	}
}

func TestDeviceHostEnvExcludesSecrets(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("ANDROID_HOME", "/opt/android")
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("AO_DEVICE_CAPABILITY", "must-not-leak")
	joined := strings.Join(deviceHostEnv(), "\n")
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "ANDROID_HOME=/opt/android") {
		t.Fatalf("toolchain environment missing: %s", joined)
	}
	if strings.Contains(joined, "must-not-leak") || strings.Contains(joined, "OPENAI_API_KEY") || strings.Contains(joined, "AO_DEVICE_CAPABILITY") {
		t.Fatalf("secret environment leaked: %s", joined)
	}
}

func TestMergeEnvironmentUsesManagedOverridesWithoutDuplicates(t *testing.T) {
	merged := mergeEnvironment([]string{"PATH=/host", "HOME=/home"}, []string{"PATH=/managed", "ANDROID_HOME=/sdk"})
	joined := strings.Join(merged, "\n")
	if strings.Count(joined, "PATH=") != 1 || !strings.Contains(joined, "PATH=/managed") || !strings.Contains(joined, "ANDROID_HOME=/sdk") {
		t.Fatalf("merged environment = %q", joined)
	}
}

func TestDownloadResumableUsesRangeAndVerifiesChecksum(t *testing.T) {
	payload := []byte("complete verified vendor archive")
	sum := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=8-" {
			t.Errorf("Range = %q", r.Header.Get("Range"))
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[8:])
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "archive.part")
	if err := os.WriteFile(target, payload[:8], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := downloadResumable(context.Background(), server.URL, fmt.Sprintf("%x", sum), target, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("archive = %q, %v", got, err)
	}
}

func TestManagedAndroidCapabilityUsesAOOwnedSDKAndAVD(t *testing.T) {
	runtime := testRuntime(t)
	runtime.dataDir = t.TempDir()
	for _, path := range []string{filepath.Join(runtime.androidSDKDir(), "platform-tools", "adb"), filepath.Join(runtime.androidSDKDir(), "emulator", "emulator"), filepath.Join(runtime.androidAVDDir(), androidAVDName+".avd", "config.ini")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if capability := runtime.androidCapability(); !capability.Available {
		t.Fatalf("capability = %#v", capability)
	}
	plan, err := runtime.SetupPlan(context.Background(), domain.DevicePlatformAndroid)
	if err != nil || !plan.Ready || plan.InstalledVersion != androidToolsVersion {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
}

func TestBoundedBufferCapsRetainedOutputAndReportsOverflow(t *testing.T) {
	buffer := newBoundedBuffer(4)
	input := []byte("123456789")
	n, err := buffer.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if got := buffer.String(); got != "12345" {
		t.Fatalf("retained output = %q", got)
	}
}

func testRuntime(t *testing.T) *Runtime {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	node := filepath.Join(root, "node")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{node, filepath.Join(runtimeDir, "ao-device-runner.mjs")} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &Runtime{
		runtimeDir: runtimeDir,
		nodePath:   node,
		stateDir:   filepath.Join(root, "state"),
		goos:       "darwin",
		lookPath:   func(string) (string, error) { return "/tool", nil },
	}
}
