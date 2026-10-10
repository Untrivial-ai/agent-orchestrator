//go:build darwin && cgo

package local

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestLiveTargetProof is explicitly opt-in. It launches one real target and
// captures one owned window, without input or a Cua MCP server.
func TestLiveTargetProof(t *testing.T) {
	if os.Getenv("LOCAL_TARGET_LIVE") != "1" {
		t.Skip("requires explicit LOCAL_TARGET_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(home, ".ao", "dev", "agentic-target")
	checkout := filepath.Join(base, "checkout")
	var manifest struct {
		CommitSHA string `json:"commitSHA"`
	}
	readJSON(t, filepath.Join(checkout, "frontend", ".vite", "testing-target.json"), &manifest)
	attempt := domain.TestAttemptID(fmt.Sprintf("e-proof-%d", time.Now().UnixMilli()))
	root := filepath.Join(base, string(attempt))
	before := proofCommand(ctx, t, nil, "/usr/bin/memory_pressure")
	a := New()
	target, err := a.Start(ctx, ports.TestingTargetSpec{AttemptID: attempt, Generation: 1, CheckoutPath: checkout, CommitSHA: manifest.CommitSHA, Deadline: time.Now().Add(3 * time.Minute), RecipeSnapshot: `{"visualMarker":true}`})
	t.Logf("target identity: %+v", target)
	if target.ID != "" {
		writeFile(t, filepath.Join(root, "memory-before.txt"), before)
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cleanupCancel()
			result, stopErr := a.Stop(cleanupCtx, target)
			writeJSON(t, filepath.Join(root, "cleanup.json"), map[string]any{"result": result, "error": errorText(stopErr)})
			after := proofCommand(cleanupCtx, t, nil, "/usr/bin/memory_pressure")
			writeFile(t, filepath.Join(root, "memory-after.txt"), after)
			logs, logErr := a.ReadLogs(cleanupCtx, target, domain.TestReadLogsRequest{MaxBytes: 262144})
			writeJSON(t, filepath.Join(root, "final-log-page.json"), map[string]any{"logs": logs, "error": errorText(logErr)})
			t.Logf("cleanup: %+v error=%v", result, stopErr)
			if stopErr != nil {
				t.Error(stopErr)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "memory-before.txt"), before)
	writeJSON(t, filepath.Join(root, "identity.json"), target)
	s, err := a.find(target)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := a.get(ctx, s, "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "readyz.json"), ready)
	writeFile(t, filepath.Join(root, "rss.txt"), ownedRSS(ctx, t, a, s))
	var oracle struct {
		Marker string `json:"marker"`
	}
	readJSON(t, filepath.Join(root, "fixture-oracle.json"), &oracle)
	if oracle.Marker == "" {
		t.Fatal("missing marker")
	}
	for {
		logs, err := a.ReadLogs(ctx, target, domain.TestReadLogsRequest{MaxBytes: 262144})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs.Text, oracle.Marker) {
			t.Fatal("marker leaked to target logs")
		}
		if strings.Contains(logs.Text, "Testing target visual fixture rendered") {
			break
		}
		proofPause(ctx, t)
	}
	for _, resource := range []domain.TestDaemonResource{domain.TestDaemonProjects, domain.TestDaemonSessions} {
		result, err := a.QueryDaemon(ctx, target, domain.TestDaemonQueryRequest{Resource: resource})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(result.Data), oracle.Marker) {
			t.Fatal("marker leaked to target query")
		}
		writeJSON(t, filepath.Join(root, string(resource)+".json"), result)
	}
	captureOneWindow(ctx, t, root, target)
	if err := a.Probe(ctx, target); err != nil {
		t.Fatal(err)
	}
	t.Logf("live proof artifacts: %s", root)
}

func captureOneWindow(ctx context.Context, t *testing.T, root string, target domain.TestTargetIdentity) {
	t.Helper()
	driver := "/Applications/CuaDriver.app/Contents/MacOS/cua-driver"
	socket, pidfile := filepath.Join(root, "cua.sock"), filepath.Join(root, "cua.pid")
	driverHome, tmp := filepath.Join(root, "cua-home"), filepath.Join(root, "tmp")
	for _, dir := range []string{driverHome, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := strippedEnv(os.Environ())
	var clean []string
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "CUA_") && key != "TMPDIR" {
			clean = append(clean, value)
		}
	}
	overrides := []string{"CUA_DRIVER_RS_HOME=" + driverHome, "CUA_DRIVER_TELEMETRY_HOME=" + driverHome, "CUA_DRIVER_RS_TELEMETRY_ENABLED=0", "CUA_DRIVER_RS_UPDATE_CHECK=0", "TMPDIR=" + tmp}
	clean = append(clean, overrides...)
	args := []string{"-n", "-g", "/Applications/CuaDriver.app"}
	for _, entry := range overrides {
		args = append(args, "--env", entry)
	}
	args = append(args, "--stdout", filepath.Join(root, "cua.stdout.log"), "--stderr", filepath.Join(root, "cua.stderr.log"), "--args", "serve", "--socket", socket, "--pid-file", pidfile, "--no-overlay")
	proofCommand(ctx, t, clean, "/usr/bin/open", args...)
	var driverPID int
	for {
		data, err := os.ReadFile(pidfile)
		if err == nil {
			driverPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && driverPID > 0 {
				break
			}
		}
		proofPause(ctx, t)
	}
	driverStart, err := nativeStartTime(driverPID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		current, err := nativeStartTime(driverPID)
		if err == nil && current.Equal(driverStart) {
			command := exec.CommandContext(cleanupCtx, driver, "--socket", socket, "--expected-pid", strconv.Itoa(driverPID), "stop")
			command.Env = clean
			output, stopErr := command.CombinedOutput()
			writeFile(t, filepath.Join(root, "cua-stop.txt"), append(output, []byte(errorText(stopErr))...))
			if stopErr != nil {
				t.Error("Cua stop failed", stopErr)
				return
			}
		} else if err == nil {
			t.Error("Cua PID identity changed")
			return
		}
		for {
			processes, err := processSnapshot(cleanupCtx)
			if err != nil {
				t.Error(err)
				return
			}
			alive := false
			for _, p := range processes {
				if p.PID == driverPID {
					alive = true
				}
			}
			if !alive {
				break
			}
			if cleanupCtx.Err() != nil {
				t.Error("Cua process remains")
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		for _, path := range []string{socket, pidfile} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Errorf("Cua leftover %s: %v", path, err)
			}
		}
	})
	call := func(tool string, input any) []byte {
		payload, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		output := proofCommand(ctx, t, clean, driver, "--socket", socket, "call", tool, string(payload))
		if !json.Valid(output) {
			t.Fatalf("Cua %s returned invalid JSON", tool)
		}
		writeFile(t, filepath.Join(root, "cua-"+tool+".json"), output)
		return output
	}
	permissions := call("check_permissions", map[string]any{"prompt": false, "probe_direct_capture": false})
	var grants struct {
		Accessibility   bool `json:"accessibility"`
		ScreenRecording bool `json:"screen_recording"`
	}
	if err := json.Unmarshal(permissions, &grants); err != nil || !grants.Accessibility || !grants.ScreenRecording {
		t.Fatalf("Cua permissions unavailable: %s", permissions)
	}
	var inventory struct {
		Windows []struct {
			PID    int `json:"pid"`
			ID     int `json:"window_id"`
			Layer  int `json:"layer"`
			Bounds struct {
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			} `json:"bounds"`
		} `json:"windows"`
	}
	windows := call("list_windows", map[string]any{"pid": target.ElectronPID, "session": "ao-sliceE-proof"})
	if err := json.Unmarshal(windows, &inventory); err != nil {
		t.Fatal(err)
	}
	var windowID int
	for _, w := range inventory.Windows {
		if w.PID == target.ElectronPID && w.Layer == 0 && w.Bounds.Width > 300 && w.Bounds.Height > 300 {
			if windowID != 0 {
				t.Fatal("ambiguous target windows")
			}
			windowID = w.ID
		}
	}
	if windowID == 0 {
		t.Fatal("target window unavailable")
	}
	// This is the only capture call. No input calls are permitted in this proof.
	call("get_window_state", map[string]any{"pid": target.ElectronPID, "window_id": windowID, "session": "ao-sliceE-proof", "max_image_dimension": 0, "timeout_ms": 5000, "include_accessibility_tree": false, "screenshot_out_file": filepath.Join(root, "target.png")})
	if _, err := os.Stat(filepath.Join(root, "target.png")); err != nil {
		t.Fatal(err)
	}
}

func ownedRSS(ctx context.Context, t *testing.T, a *Adapter, s *launch) []byte {
	t.Helper()
	if err := a.captureTree(ctx, s); err != nil {
		t.Fatal(err)
	}
	data := proofCommand(ctx, t, nil, "/bin/ps", "-axo", "pid=,rss=")
	var out strings.Builder
	total := 0
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, owned := s.owned[pid]; owned {
			rss, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatal(err)
			}
			total += rss
			fmt.Fprintf(&out, "pid=%d rss_kib=%d\n", pid, rss)
		}
	}
	fmt.Fprintf(&out, "total_rss_kib=%d\n", total)
	return []byte(out.String())
}

func proofCommand(ctx context.Context, t *testing.T, env []string, exe string, args ...string) []byte {
	t.Helper()
	command := exec.CommandContext(ctx, exe, args...)
	if env == nil {
		env = strippedEnv(os.Environ())
	}
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", filepath.Base(exe), err, output)
	}
	return output
}
func proofPause(ctx context.Context, t *testing.T) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-time.After(100 * time.Millisecond):
	}
}
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, append(data, '\n'))
}
func readJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

// TestLiveLeftoverAudit observes a stopped proof without attaching or signalling.
func TestLiveLeftoverAudit(t *testing.T) {
	root := os.Getenv("LOCAL_TARGET_AUDIT")
	if root == "" {
		t.Skip("requires LOCAL_TARGET_AUDIT")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !beneath(filepath.Join(home, ".ao", "dev", "agentic-target"), root) {
		t.Fatal("invalid audit root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var target domain.TestTargetIdentity
	readJSON(t, filepath.Join(root, "identity.json"), &target)
	data, err := os.ReadFile(filepath.Join(root, "rss.txt"))
	if err != nil {
		t.Fatal(err)
	}
	known := map[int]bool{target.ElectronPID: true, target.DaemonPID: true}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasPrefix(fields[0], "pid=") {
			pid, err := strconv.Atoi(strings.TrimPrefix(fields[0], "pid="))
			if err != nil {
				t.Fatal(err)
			}
			known[pid] = true
		}
	}
	// The failed fourth proof also observed this newly listed child.
	if strings.HasSuffix(root, "1791276169852") {
		known[54651] = true
	}
	inventory, err := processSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var remaining []int
	for _, p := range inventory {
		if known[p.PID] {
			remaining = append(remaining, p.PID)
		}
	}
	windows, windowErr := nativeWindows(ctx, target.ElectronPID)
	output, err := os.ReadFile(filepath.Join(root, "target.log"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`daemon listening" addr=127\.0\.0\.1:(\d+)`).FindSubmatch(output)
	if len(match) != 2 {
		t.Fatal("target listener identity missing")
	}
	port, err := strconv.Atoi(string(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	listenerErr := listenerGone(ctx, port)
	_, runErr := os.Stat(filepath.Join(root, "running.json"))
	writeJSON(t, filepath.Join(root, "post-stop-audit.json"), map[string]any{"remainingRecordedPIDs": remaining, "windows": windows, "windowError": errorText(windowErr), "port": port, "listenerError": errorText(listenerErr), "runFileAbsent": os.IsNotExist(runErr)})
	t.Logf("audit pids=%v windows=%v port=%d listener=%v runAbsent=%v", remaining, windows, port, listenerErr, os.IsNotExist(runErr))
	if len(remaining) > 0 || len(windows) > 0 || windowErr != nil || listenerErr != nil || !os.IsNotExist(runErr) {
		t.Fatal("leftover audit incomplete")
	}
}
