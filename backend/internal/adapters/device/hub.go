package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// HubOrigin starts the pinned Expo Device Hub on a loopback-only socket and
// returns its origin. The daemon is the sole process supervisor; renderer and
// agent callers only reach the restricted proxy exposed by the HTTP layer.
func (r *Runtime) HubOrigin(ctx context.Context) (string, error) {
	r.hubMu.Lock()
	defer r.hubMu.Unlock()
	if r.hubOrigin != "" && hubHealthy(ctx, r.hubOrigin) {
		return r.hubOrigin, nil
	}
	r.stopHubLocked()
	entry := filepath.Join(r.runtimeDir, "node_modules", "expo-device-hub", "dist", "server", "cli.mjs")
	if !regularFile(entry) {
		return "", &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "The embedded device streaming runtime is not installed"}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("allocate device hub port: %w", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return "", fmt.Errorf("allocate device hub port: unexpected listener address")
	}
	port := address.Port
	_ = listener.Close()

	logDir := filepath.Join(r.dataDir, "devices", "hub")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", fmt.Errorf("create device hub directory: %w", err)
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "hub.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("open device hub log: %w", err)
	}
	cmd := exec.Command(r.nodePath, entry,
		"--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--transport", "h264", "--max-dimension", "1000", "--video-fps", "30",
		// The hub defaults Android to gRPC screenshots, which requires a separate
		// ffmpeg/libx264 installation. scrcpy streams H.264 directly from the
		// AO-managed emulator and keeps the device runtime self-contained.
		"--mjpeg-quality", "0.65", "--stream-source", "scrcpy", "--grpc-image-mode", "png",
		"--hide-sidebar", "--hide-boot-device",
	)
	cmd.Env = mergeEnvironment(deviceHostEnv(), append(r.managedAndroidEnv(), r.managedJavaEnv()...))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return "", fmt.Errorf("start device hub: %w", err)
	}
	origin := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if hubHealthy(ctx, origin) {
			r.hubCmd, r.hubLog, r.hubOrigin = cmd, logFile, origin
			go r.observeHub(cmd)
			return origin, nil
		}
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = logFile.Close()
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	_ = logFile.Close()
	return "", &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "The embedded device streaming runtime did not become ready"}
}

func hubHealthy(ctx context.Context, origin string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, origin+"/api/devices?booted=1", http.NoBody)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode == http.StatusOK
}

// PrepareStream starts the selected simulator's serve-sim helper before AO
// hands its stream URL to the renderer. expo-device-hub's browser client does
// this itself, but AO intentionally embeds only the stream rather than that
// client shell, so the daemon owns the equivalent lifecycle step.
func (r *Runtime) PrepareStream(ctx context.Context, platform domain.DevicePlatform, deviceID string) error {
	if platform != domain.DevicePlatformIOS {
		return nil
	}
	origin, err := r.HubOrigin(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"udid": deviceID})
	if err != nil {
		return fmt.Errorf("encode iOS stream request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/vendor/serve-sim/grid/api/start", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create iOS stream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "The iOS device stream could not start"}
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var result struct {
		OK bool `json:"ok"`
	}
	if readErr != nil || json.Unmarshal(body, &result) != nil || response.StatusCode != http.StatusOK || !result.OK {
		return &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "The iOS device stream could not start"}
	}
	healthURL := origin + "/vendor/serve-sim/helper/" + url.PathEscape(deviceID) + "/health"
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if hubRouteHealthy(ctx, healthURL) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return &ports.DeviceRuntimeError{Code: "DEVICE_RUNTIME_UNAVAILABLE", Message: "The iOS device stream did not become ready"}
}

func hubRouteHealthy(ctx context.Context, route string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, route, http.NoBody)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode == http.StatusOK
}

func (r *Runtime) observeHub(cmd *exec.Cmd) {
	_ = cmd.Wait()
	r.hubMu.Lock()
	defer r.hubMu.Unlock()
	if r.hubCmd == cmd {
		if r.hubLog != nil {
			_ = r.hubLog.Close()
		}
		r.hubCmd, r.hubLog, r.hubOrigin = nil, nil, ""
	}
}

func (r *Runtime) managedJavaEnv() []string {
	if java := findJavaFile(filepath.Join(r.androidRoot(), "jdk")); java != "" {
		return []string{"JAVA_HOME=" + filepath.Dir(filepath.Dir(java))}
	}
	return nil
}

// Close stops only AO's helper process. Simulators and emulators remain alive.
func (r *Runtime) Close(context.Context) error {
	r.hubMu.Lock()
	defer r.hubMu.Unlock()
	r.stopHubLocked()
	return nil
}

func (r *Runtime) stopHubLocked() {
	if r.hubCmd != nil && r.hubCmd.Process != nil {
		_ = r.hubCmd.Process.Kill()
	}
	if r.hubLog != nil {
		_ = r.hubLog.Close()
	}
	r.hubCmd, r.hubLog, r.hubOrigin = nil, nil, ""
}
