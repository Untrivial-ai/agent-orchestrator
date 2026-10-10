//go:build darwin && cua_live

package cua

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// TestLiveOwnedAO runs only against an explicitly launched private AO window.
// The caller owns the launch lock and must stop the fixture after this test.
func TestLiveOwnedAO(t *testing.T) {
	root := os.Getenv("CUA_OWNED_AO_ROOT")
	if root == "" {
		t.Skip("explicit owned AO fixture required")
	}
	data, err := os.ReadFile(filepath.Join(root, "fixture-pid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		PID      int    `json:"pid"`
		WindowID string `json:"windowId"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	birth, err := process.StartTime(fixture.PID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	a, err := New(Config{DataDir: filepath.Join(root, "adapter"), DeliveryMode: Foreground})
	if err != nil {
		t.Fatal(err)
	}
	a.runner = liveDiagnosticRunner{Runner: a.runner, t: t, windowListPath: filepath.Join(root, "input-window-order.json")}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	target, err := a.BindWindow(ctx, domain.TestTargetIdentity{ID: "owned-ao", LaunchID: "live-capture", Generation: 1, ElectronPID: fixture.PID, ElectronStartedAt: birth, WindowID: fixture.WindowID})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("owned pid=%d window=%s driver=%d root=%s", target.ElectronPID, target.WindowID, a.driver.pid, a.root)
	capture := func(label string) domain.TestScreenshot {
		shot, err := a.Screenshot(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, label+".png"), shot.Data, 0o600); err != nil {
			t.Fatal(err)
		}
		shot.Frame.ScreenshotID = label
		return shot
	}
	before := capture("owned-before")
	for _, e := range before.Elements {
		if e.Label != "" {
			t.Logf("element %s %q %.0f %.0f", e.Role, e.Label, e.Frame.X, e.Frame.Y)
		}
	}
	if os.Getenv("CUA_OWNED_AO_STAGE") == "capture" {
		return
	}
	if os.Getenv("CUA_OWNED_AO_STAGE") == "release" {
		driver := a.driver
		if err := a.Release(ctx, target); err != nil {
			t.Fatal(err)
		}
		if _, err := process.StartTime(driver.pid); !errors.Is(err, process.ErrNotRunning) {
			t.Fatalf("Release left its owned driver alive: %v", err)
		}
		for _, path := range []string{a.socket(), a.pidFile()} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("Release left its driver path: %s: %v", path, err)
			}
		}
		return
	}
	// A second controller must coexist without sharing, adopting or stopping ours.
	other, err := New(Config{DataDir: filepath.Join(root, "adapter-"+strings.Repeat("long-root-", 10))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := other.Close(cleanup); err != nil {
			t.Error(err)
		}
	})
	if _, err := other.BindWindow(ctx, target); err != nil {
		t.Fatal(err)
	}
	if other.driver.pid == a.driver.pid || other.socket() == a.socket() {
		t.Fatal("driver ownership shared")
	}
	otherDriver := other.driver
	driverProof := []map[string]any{
		{"pid": a.driver.pid, "birth": a.driver.started, "socket": a.socket(), "pidFile": a.pidFile()},
		{"pid": otherDriver.pid, "birth": otherDriver.started, "socket": other.socket(), "pidFile": other.pidFile()},
	}
	data, err = json.MarshalIndent(driverProof, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(root, "owned-driver-receipts.json"), data, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := process.StartTime(otherDriver.pid); !errors.Is(err, process.ErrNotRunning) {
		t.Fatalf("second driver still present: %v", err)
	}
	for _, path := range []string{other.socket(), other.pidFile()} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("second driver path remains: %s: %v", path, err)
		}
	}
	if err := a.checkDriver(ctx); err != nil {
		t.Fatal(err)
	}
	recording, err := a.StartRecording(ctx, target, filepath.Join(root, "owned-video"))
	if err != nil {
		t.Fatal(err)
	}
	before = capture("owned-recording-before")
	settings := ""
	for _, e := range before.Elements {
		if e.Label == "Settings" && e.Role == "AXButton" {
			settings = e.ElementID
			break
		}
	}
	request := domain.TestClickRequest{ScreenshotID: before.Frame.ScreenshotID, ElementID: settings, X: 60, Y: before.Frame.Height - 31}
	if _, err := a.Click(ctx, target, before.Frame, request); err != nil {
		t.Fatal(err)
	}
	after := capture("owned-after-input")
	if bytes.Equal(before.Data, after.Data) {
		t.Fatal("input produced no visible change")
	}
	found := false
	for _, e := range after.Elements {
		if strings.Contains(e.Label, "General") || strings.Contains(e.Label, "Appearance") {
			found = true
		}
	}
	if !found {
		t.Fatal("Settings content was not observed after input")
	}
	final, err := a.StopRecording(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if final.Duration <= 0 || final.Path != recording.Path || final.Gap != "" || final.StagingPath != "" {
		t.Fatalf("invalid movie %+v", final)
	}
	data, err = json.MarshalIndent(final, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "owned-video.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("playable movie %s duration=%s dimensions=%dx%d recorder=%d", final.Path, final.Duration, final.Width, final.Height, final.RecorderPID)
	driver := a.driver
	if err := a.Release(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err := process.StartTime(driver.pid); !errorsIsNotRunning(err) {
		t.Fatalf("driver still present: %v", err)
	}
	for _, path := range []string{a.socket(), a.pidFile()} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("driver path remains: %s %v", path, err)
		}
	}
	if _, err := process.StartTime(final.RecorderPID); !errorsIsNotRunning(err) {
		t.Fatalf("recorder still present: %v", err)
	}
}

func errorsIsNotRunning(err error) bool { return errors.Is(err, process.ErrNotRunning) }

type liveDiagnosticRunner struct {
	Runner
	t              *testing.T
	windowListPath string
}

func (r liveDiagnosticRunner) Run(ctx context.Context, executable string, args, env []string) (Output, error) {
	out, err := r.Runner.Run(ctx, executable, args, env)
	if executable == "/usr/bin/osascript" && err == nil && r.windowListPath != "" {
		var snapshot windowSnapshot
		if decodeErr := json.Unmarshal(out.Stdout, &snapshot); decodeErr != nil {
			r.t.Fatal(decodeErr)
		}
		// Retain the admission's actual order and bounds, without window titles.
		data, writeErr := json.MarshalIndent(snapshot, "", "  ")
		if writeErr == nil {
			writeErr = os.WriteFile(r.windowListPath, data, 0o600)
		}
		if writeErr != nil {
			r.t.Fatal(writeErr)
		}
	}
	if err != nil {
		r.t.Logf("provider failure %s %v stdout=%s stderr=%s", executable, args, out.Stdout, out.Stderr)
	}
	return out, err
}

// Cancel immediately after LaunchServices accepts the owned driver launch.
// Cleanup must still observe and reap that launch, even before BindWindow.
func TestLiveOwnedStartupCancellation(t *testing.T) {
	root := os.Getenv("CUA_OWNED_AO_ROOT")
	if root == "" {
		t.Skip("explicit owned native fixture required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(Config{DataDir: filepath.Join(root, "startup-cancel")})
	if err != nil {
		t.Fatal(err)
	}
	a.runner = cancelAfterOwnedLaunch{Runner: a.runner, cancel: cancel, pidFile: a.pidFile()}
	// Keep recovery within the same controller and its private launch paths.
	// This cleanup also lets the pre-fix reproduction leave no owned driver.
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if a.driver.pid == 0 && a.pendingDriver.pid == 0 {
			data, err := os.ReadFile(a.pidFile())
			if err == nil {
				pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil {
					t.Error(err)
					return
				}
				birth, err := process.StartTime(pid)
				if err != nil {
					t.Error(err)
					return
				}
				a.pendingDriver = driverIdentity{pid: pid, started: birth}
			}
		}
		if err := a.Close(cleanup); err != nil {
			t.Error(err)
		}
	})
	if os.Getenv("CUA_OWNED_STARTUP_BIND") == "1" {
		birth, err := process.StartTime(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		target := domain.TestTargetIdentity{ID: "owned-cancel", LaunchID: "owned-cua", Generation: 1, ElectronPID: os.Getpid(), ElectronStartedAt: birth}
		if _, err := a.BindWindow(ctx, target); !errors.Is(err, context.Canceled) {
			t.Fatalf("partial binding lost cancellation: %v", err)
		}
		driver := a.pendingDriver
		if driver.pid == 0 {
			t.Fatal("cancelled bind did not retain its launched driver receipt")
		}
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := a.Release(cleanup, target); err != nil {
			t.Fatal(err)
		}
		if err := a.Release(cleanup, target); err != nil {
			t.Fatal("partial Release was not idempotent", err)
		}
		for _, path := range []string{a.socket(), a.pidFile()} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("partial binding left owned driver path: %s: %v", path, err)
			}
		}
		if birth, err := process.StartTime(driver.pid); !errors.Is(err, process.ErrNotRunning) && (err != nil || birth.Equal(driver.started)) {
			t.Fatalf("partial release did not prove driver exit: %d: %v", driver.pid, err)
		}
		t.Logf("Release alone reaped driver %d; PID file and socket absent; exact second Release succeeded", driver.pid)
		return
	}
	if err := a.ensureDriver(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("launch cancellation not retained: %v", err)
	}
	cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	if err := a.Close(cleanup); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{a.socket(), a.pidFile()} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("startup cancellation left owned driver path: %s: %v", path, err)
		}
	}
}

type cancelAfterOwnedLaunch struct {
	Runner
	cancel  context.CancelFunc
	pidFile string
}

func (r cancelAfterOwnedLaunch) Run(ctx context.Context, executable string, args, env []string) (Output, error) {
	out, err := r.Runner.Run(ctx, executable, args, env)
	if executable == "/usr/bin/open" && err == nil {
		// Establish that the accepted launch has produced its owned PID file,
		// then cancel before the controller observes the kernel birth receipt.
		wait, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, readErr := os.ReadFile(r.pidFile); readErr == nil {
				r.cancel()
				break
			}
			select {
			case <-wait.Done():
				return out, wait.Err()
			case <-ticker.C:
			}
		}
	}
	return out, err
}
