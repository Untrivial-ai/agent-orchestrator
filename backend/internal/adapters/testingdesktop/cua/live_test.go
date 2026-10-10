//go:build darwin && cua_live

package cua

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// TestLiveElectron requires an explicitly created disposable fixture. Ordinary
// package tests cannot launch desktop processes or dispatch real input.
func TestLiveElectron(t *testing.T) {
	root := os.Getenv("CUA_LIVE_ROOT")
	if root == "" {
		t.Skip("explicit disposable Electron fixture required")
	}
	data, err := os.ReadFile(filepath.Join(root, "fixture-pid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		PID      int    `json:"pid"`
		WindowID string `json:"windowId,omitempty"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if os.Getenv("CUA_LIVE_STAGE") == "close-driver" {
		data, err := os.ReadFile(filepath.Join(root, "video-driver-listeners.json"))
		if err != nil {
			t.Fatal(err)
		}
		var driver struct {
			PID   int       `json:"pid"`
			Birth time.Time `json:"birth"`
		}
		if err := json.Unmarshal(data, &driver); err != nil {
			t.Fatal(err)
		}
		birth, err := process.StartTime(driver.PID)
		if err != nil || !birth.Equal(driver.Birth) {
			t.Fatal("owned Driver identity changed before cleanup")
		}
		a, err := New(Config{DataDir: filepath.Join(root, "adapter")})
		if err != nil {
			t.Fatal(err)
		}
		a.driver = driverIdentity{pid: driver.PID, started: driver.Birth}
		if err := a.Close(ctx); err != nil {
			t.Fatal(err)
		}
		return
	}
	started, err := process.StartTime(fixture.PID)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CUA_LIVE_STAGE") == "close-fixture" {
		observed, err := process.StartTime(fixture.PID)
		if err != nil || !observed.Equal(started) {
			t.Fatal("fixture birth identity changed before cleanup")
		}
		p, err := os.FindProcess(fixture.PID)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		return
	}
	mode := DeliveryMode(os.Getenv("CUA_LIVE_DELIVERY"))
	a, err := New(Config{DataDir: filepath.Join(root, "adapter"), DeliveryMode: mode, Runner: liveRunner{root: root}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Close(cleanup); err != nil {
			t.Error(err)
		}
	})
	target, err := a.BindWindow(ctx, domain.TestTargetIdentity{ID: "disposable-electron", LaunchID: "live-proof", Generation: 1,
		ElectronPID: fixture.PID, ElectronStartedAt: started, DataDir: filepath.Join(root, "user-data"), WindowID: fixture.WindowID})
	if err != nil {
		t.Fatal(err)
	}
	fixture.WindowID = target.WindowID
	data, err = json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fixture-pid.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("target PID=%d start=%s window=%s driver PID=%d mode=%s", target.ElectronPID, target.ElectronStartedAt.Format(time.RFC3339Nano), target.WindowID, a.driver.pid, a.DeliveryMode())
	stage := os.Getenv("CUA_LIVE_STAGE")
	if stage == "" {
		stage = "capture"
	}
	write := func(name string, value any) {
		encoded, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, stage+"-"+name+".json"), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.checkDriver(ctx); err != nil {
		t.Fatal(err)
	}
	listeners, listenerErr := a.run(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(a.driver.pid), "-iTCP", "-sTCP:LISTEN")
	if err := a.checkDriver(ctx); err != nil {
		t.Fatal(err)
	}
	write("driver-listeners", map[string]any{"pid": a.driver.pid, "birth": a.driver.started, "stdout": string(listeners.Stdout), "stderr": string(listeners.Stderr), "exit_error": fmt.Sprint(listenerErr)})
	if len(listeners.Stdout) != 0 || len(listeners.Stderr) != 0 {
		t.Fatal("Cua listener check was not empty")
	}
	capture := func(label string) domain.TestDesktopFrame {
		shot, err := a.Screenshot(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, stage+"-"+label+".png"), shot.Data, 0o600); err != nil {
			t.Fatal(err)
		}
		shot.Frame.ScreenshotID = stage + "-" + label
		write(label+"-frame", shot.Frame)
		t.Logf("%s: %dx%d pixels, %.0fx%.0f points, scale %.1f", label, shot.Frame.Width, shot.Frame.Height, shot.Frame.Bounds.Width, shot.Frame.Bounds.Height, shot.Frame.Scale)
		return shot.Frame
	}
	frame := capture("before")
	if stage == "video" || stage == "video-close" {
		windows, err := a.windows(ctx, a.bindings[target.ID])
		write("recording-windows", windows)
		if err != nil {
			t.Fatal(err)
		}
		recording, err := a.StartRecording(ctx, target, filepath.Join(root, "evidence"))
		write("start-recording", map[string]any{"result": recording, "error": fmt.Sprint(err)})
		if err != nil {
			t.Fatal(err)
		}
		frame = capture("recording-before-click")
		if _, err := a.Click(ctx, target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 300, Y: 440}); err != nil {
			t.Fatal(err)
		}
		capture("recording-after-click")
		// This interval is the requested sample duration, not a readiness retry.
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if stage == "video-close" {
			if err := a.checkProcess(ctx, target); err != nil {
				t.Fatal(err)
			}
			p, err := os.FindProcess(target.ElectronPID)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			for {
				birth, probeErr := process.StartTime(target.ElectronPID)
				if probeErr != nil || !birth.Equal(target.ElectronStartedAt) {
					break
				}
				select {
				case <-time.After(100 * time.Millisecond):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		}
		final, err := a.StopRecording(ctx, target)
		write("stop-recording", map[string]any{"result": final, "error": fmt.Sprint(err)})
		if err != nil {
			t.Fatal(err)
		}
		if final.Gap != "" || final.Duration <= 0 || final.Width != frame.Width || final.Height != frame.Height || final.Path != recording.Path {
			t.Fatalf("invalid finalized recording: %+v", final)
		}
		t.Logf("finalized %s, %s, %dx%d, recorder PID=%d, target-closed=%t", final.Path, final.Duration, final.Width, final.Height, final.RecorderPID, stage == "video-close")
		return
	}
	if stage == "video-front" {
		var result any
		err := a.call(ctx, "bring_to_front", a.targetArgs(a.bindings[target.ID]), &result)
		write("activation", map[string]any{"result": result, "error": fmt.Sprint(err)})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if stage == "capture" {
		return
	}
	observeCursor := func(label string) {
		var cursor any
		if err := a.call(ctx, "get_cursor_position", map[string]any{"session": a.bindings[target.ID].session}, &cursor); err != nil {
			t.Fatal(err)
		}
		write(label+"-cursor", cursor)
		var applications struct {
			Apps []struct {
				PID    int  `json:"pid"`
				Active bool `json:"active"`
			} `json:"apps"`
		}
		if err := a.call(ctx, "list_apps", map[string]any{}, &applications); err != nil {
			t.Fatal(err)
		}
		active := []int{}
		for _, application := range applications.Apps {
			if application.Active {
				active = append(active, application.PID)
			}
		}
		write(label+"-focus", map[string]any{"active_pids": active, "observed_at": time.Now().UTC()})
	}
	observeCursor("before")
	logBefore, err := os.ReadFile(filepath.Join(root, "fixture.stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	if stage == "ax" || stage == "ax-foreground" || stage == "key-token" {
		args := a.targetArgs(a.bindings[target.ID])
		args["include_accessibility_tree"] = true
		args["max_image_dimension"] = 0
		var snapshot struct {
			Elements []struct {
				Role  string `json:"role"`
				Token string `json:"element_token"`
				Label string `json:"label"`
			} `json:"elements"`
		}
		if err := a.call(ctx, "get_window_state", args, &snapshot); err != nil {
			t.Fatal(err)
		}
		for _, element := range snapshot.Elements {
			if element.Role != "AXTextField" {
				continue
			}
			args := a.inputArgs(a.bindings[target.ID])
			args["element_token"] = element.Token
			args["text"] = "AX ELECTRON PROOF"
			if stage == "key-token" {
				delete(args, "text")
				args["key"] = "return"
			}
			var result any
			tool := "type_text"
			if stage == "key-token" {
				tool = "press_key"
			}
			err := a.call(ctx, tool, args, &result)
			write("ax-result", map[string]any{"result": result, "error": fmt.Sprint(err), "element": element})
			capture("after-ax")
			return
		}
		t.Fatal("fixture AXTextField not found")
	}
	if stage == "finish" {
		if !strings.Contains(string(logBefore), `"event":"return","value":"ELECTRON PIXEL PROOF"`) {
			t.Fatal("earlier type/key effect missing")
		}
		result, err := a.Click(ctx, target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 300, Y: 440})
		write("click", map[string]any{"result": result, "error": fmt.Sprint(err)})
		if err != nil {
			t.Fatal(err)
		}
		observeCursor("after-click")
		capture("after-click")
		observed, err := os.ReadFile(filepath.Join(root, "fixture.stdout.log"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(observed[len(logBefore):]), `"state":"BUTTON CLICK OBSERVED"`) {
			t.Fatal("button effect missing")
		}
		return
	}
	result, err := a.Type(ctx, target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: 240, Y: 280, Text: "ELECTRON PIXEL PROOF"})
	write("type", map[string]any{"result": result, "error": fmt.Sprint(err)})
	if err != nil {
		t.Logf("provider type error retained; checking fixture effect before proceeding: %v", err)
	}
	observeCursor("after-type")
	frame = capture("after-type")
	if os.Getenv("CUA_LIVE_EXPECT_EFFECT") == "1" {
		observed, readErr := os.ReadFile(filepath.Join(root, "fixture.stdout.log"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(observed[len(logBefore):]), `"value":"ELECTRON PIXEL PROOF"`) {
			t.Fatalf("typed marker not observed after provider result: %v", err)
		}
	}
	result, err = a.Key(ctx, target, frame, domain.TestKeyRequest{ScreenshotID: frame.ScreenshotID, Keys: []string{"return"}})
	write("key", map[string]any{"result": result, "error": fmt.Sprint(err)})
	if err != nil {
		t.Fatal(err)
	}
	observeCursor("after-key")
	frame = capture("after-key")
	result, err = a.Click(ctx, target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 300, Y: 440})
	write("click", map[string]any{"result": result, "error": fmt.Sprint(err)})
	if err != nil {
		t.Fatal(err)
	}
	observeCursor("after-click")
	capture("after-click")
	if os.Getenv("CUA_LIVE_EXPECT_EFFECT") == "1" {
		logAfter, err := os.ReadFile(filepath.Join(root, "fixture.stdout.log"))
		if err != nil {
			t.Fatal(err)
		}
		events := string(logAfter[len(logBefore):])
		for _, expected := range []string{`"value":"ELECTRON PIXEL PROOF"`, `"event":"return"`, `"state":"BUTTON CLICK OBSERVED"`} {
			if !strings.Contains(events, expected) {
				t.Fatalf("fixture did not observe %s: %s", expected, events)
			}
		}
	}
}

type liveRunner struct{ root string }

func (r liveRunner) Run(ctx context.Context, executable string, args, env []string) (Output, error) {
	out, err := (commandRunner{}).Run(ctx, executable, args, env)
	if len(args) == 5 && args[2] == "call" && args[3] == "list_windows" {
		if writeErr := os.WriteFile(filepath.Join(r.root, "provider-windows.json"), out.Stdout, 0o600); writeErr != nil {
			return out, writeErr
		}
	}
	if len(args) == 5 && args[2] == "call" && args[3] == "get_window_state" {
		var state map[string]json.RawMessage
		if json.Unmarshal(out.Stdout, &state) == nil {
			delete(state, "elements")
			delete(state, "tree_markdown")
			data, encodeErr := json.MarshalIndent(state, "", "  ")
			if encodeErr != nil {
				return out, encodeErr
			}
			if writeErr := os.WriteFile(filepath.Join(r.root, "provider-capture-"+uuid.NewString()+".json"), data, 0o600); writeErr != nil {
				return out, writeErr
			}
		}
	}
	return out, err
}
