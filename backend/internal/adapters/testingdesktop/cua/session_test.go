package cua

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type providerSession struct {
	ended          bool
	last           time.Time
	serial         int
	capture, token string
}

// Named sessions in pinned Driver 0.34 remain ended after idle cleanup.
// Only start_session can revive them, and their old capture/AX state is gone.
type sessionProvider struct {
	now                      time.Time
	sessions                 map[string]*providerSession
	starts, revivals, inputs int
	stopped                  bool
}

func strictSessionProvider(t *testing.T, f *fixture) *sessionProvider {
	t.Helper()
	p := &sessionProvider{now: f.adapter.now(), sessions: make(map[string]*providerSession)}
	p.sessions[f.adapter.bindings[f.target.ID].session] = &providerSession{last: p.now}
	f.adapter.now = func() time.Time { return p.now }
	provider := f.runner.hook
	f.runner.hook = func(executable string, args []string) (Output, error) {
		if len(args) == 7 && args[4] == "stop" {
			if args[5] != "--expected-pid" || args[6] != "99" {
				t.Fatal("stop lost owned Driver identity", args)
			}
			p.stopped = true
			return Output{}, os.Remove(f.adapter.pidFile())
		}
		if len(args) != 5 || args[2] != "call" {
			return provider(executable, args)
		}
		var input map[string]any
		if err := json.Unmarshal([]byte(args[4]), &input); err != nil {
			t.Fatal(err)
		}
		label, _ := input["session"].(string)
		if label == "" {
			t.Fatal("missing owned session label", args)
		}
		s := p.sessions[label]
		if s != nil && p.now.Sub(s.last) >= 300*time.Second {
			s.ended, s.capture, s.token = true, "", ""
		}
		switch args[3] {
		case "start_session":
			if len(input) != 1 {
				t.Fatal("start_session received unsupported target or policy arguments", input)
			}
			p.starts++
			revived := s != nil && s.ended
			if s == nil {
				s = &providerSession{}
				p.sessions[label] = s
			}
			if revived {
				p.revivals++
				s.capture, s.token = "", ""
			}
			s.ended, s.last = false, p.now
			return jsonOutput(map[string]any{"active": true, "revived": revived}), nil
		case "end_session":
			if s == nil || len(input) != 1 {
				t.Fatal("end_session did not name only an owned episode", input)
			}
			s.ended, s.capture, s.token = true, "", ""
			return jsonOutput(map[string]any{"active": false}), nil
		}
		if s == nil {
			// Ordinary calls can create a new named run, but cannot revive one.
			s = &providerSession{last: p.now}
			p.sessions[label] = s
		}
		if s.ended {
			return Output{Stderr: []byte("session has ended; tool call '" + args[3] + "' was rejected. Call start_session with session '" + label + "' to start it again, or use a new session label.")}, errors.New("exit status 1")
		}
		pID, _ := input["pid"].(float64)
		windowID := 456
		if pID == 124 {
			windowID = 777
		}
		if args[3] == "list_windows" {
			if input["window_id"] != nil {
				t.Fatal("list_windows received window_id", input)
			}
			s.last = p.now
			return jsonOutput(map[string]any{"windows": []window{{PID: int(pID), ID: windowID, Layer: 0, Bounds: f.bounds, OnScreen: true}}}), nil
		}
		if args[3] == "get_window_state" {
			out, err := provider(executable, args)
			if err != nil {
				return out, err
			}
			var state captureState
			if err := json.Unmarshal(out.Stdout, &state); err != nil {
				t.Fatal(err)
			}
			s.serial++
			s.capture = fmt.Sprintf("%s-capture-%d", label, s.serial)
			s.token = fmt.Sprintf("%s-token-%d", label, s.serial)
			state.PID, state.WindowID, state.Capture = int(pID), windowID, s.capture
			for i := range state.Elements {
				state.Elements[i].Token = s.token
			}
			s.last = p.now
			return jsonOutput(state), nil
		}
		if args[3] == "click" || args[3] == "type_text" || args[3] == "press_key" {
			if input["capture_id"] != nil && input["capture_id"] != s.capture || input["element_token"] != nil && input["element_token"] != s.token {
				t.Fatal("input adopted retired provider state", input)
			}
			p.inputs++
		}
		s.last = p.now
		return provider(executable, args)
	}
	f.adapter.started = func(_ context.Context, pid int) (time.Time, error) {
		if pid == 99 && p.stopped {
			return time.Time{}, errors.New("owned Driver stopped")
		}
		return f.born, nil
	}
	return p
}

func TestSessionRevivesBeforeFreshCaptureAfterIdle(t *testing.T) {
	f := newFixture(t)
	p := strictSessionProvider(t, f)
	f.adapter.cfg.DeliveryMode = Foreground
	f.elements = []capturedElement{{Role: "AXTextField", Token: "field", Frame: &pixelBounds{X: 100, Y: 100, Width: 400, Height: 200}}}
	old := f.screenshot(t)
	if result, err := f.adapter.Type(context.Background(), f.target, old, domain.TestTypeRequest{ScreenshotID: old.ScreenshotID, X: 200, Y: 150, Text: "hello"}); err != nil || !result.Delivered {
		t.Fatal(result, err)
	}
	b := f.adapter.bindings[f.target.ID]
	oldCapture := p.sessions[b.session].capture
	if b.lastTyped == nil {
		t.Fatal("typing setup did not retain focus")
	}
	// This is the recorded bad04 sequence: long idle, then list_windows fails
	// before get_window_state. No window or permission diagnosis is implied.
	p.now = p.now.Add(9 * time.Minute)
	if _, err := f.adapter.liveWindow(context.Background(), b); !errors.Is(err, ErrProvider) || !strings.Contains(err.Error(), "call list_windows") || !strings.Contains(err.Error(), "session has ended") {
		t.Fatal("strict provider did not reproduce the retained cause", err)
	}
	starts := p.starts
	fresh := f.screenshot(t)
	if p.starts != starts+1 || p.revivals != 1 || b.session == "" || b.lastTyped != nil || b.receipt.keyToken != "" || b.receipt.providerID == oldCapture {
		t.Fatal("fresh capture adopted the ended episode", p, b)
	}
	before := len(f.runner.calls)
	if _, err := f.adapter.Click(context.Background(), f.target, old, domain.TestClickRequest{ScreenshotID: old.ScreenshotID, X: 10, Y: 10}); !errors.Is(err, ErrRefused) || len(f.runner.calls) != before {
		t.Fatal("old screenshot was accepted after revival", err)
	}
	inputs := p.inputs
	if result, err := f.adapter.Click(context.Background(), f.target, fresh, domain.TestClickRequest{ScreenshotID: fresh.ScreenshotID, X: 10, Y: 10}); err != nil || !result.Delivered || p.inputs != inputs+1 || p.starts != starts+1 {
		t.Fatal("fresh input failed or revived/replayed its session", result, err)
	}
	if _, err := f.adapter.Click(context.Background(), f.target, fresh, domain.TestClickRequest{ScreenshotID: fresh.ScreenshotID, X: 10, Y: 10}); !errors.Is(err, ErrRefused) || p.inputs != inputs+1 {
		t.Fatal("fresh input was not consumed once", err)
	}
}

func TestSessionInputNeverRevivesEndedReceipt(t *testing.T) {
	f := newFixture(t)
	p := strictSessionProvider(t, f)
	frame := f.screenshot(t)
	b := f.adapter.bindings[f.target.ID]
	p.sessions[b.session].ended = true
	starts := p.starts
	if _, err := f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 10, Y: 10}); !errors.Is(err, ErrProvider) || !strings.Contains(err.Error(), "call list_windows") || p.starts != starts || p.inputs != 0 {
		t.Fatal("input silently revived or replayed an ended session", err)
	}
}

func TestSessionActiveObservationKeepsTypingFocus(t *testing.T) {
	f := newFixture(t)
	p := strictSessionProvider(t, f)
	f.adapter.cfg.DeliveryMode = Foreground
	f.elements = []capturedElement{{Role: "AXTextField", Token: "field", Frame: &pixelBounds{X: 100, Y: 100, Width: 400, Height: 200}}}
	frame := f.screenshot(t)
	if _, err := f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: 200, Y: 150, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	frame = f.screenshot(t)
	b := f.adapter.bindings[f.target.ID]
	if p.revivals != 0 || b.lastTyped == nil || b.receipt.keyToken != p.sessions[b.session].token {
		t.Fatal("active session lost fresh typing focus", b)
	}
	if _, err := f.adapter.Key(context.Background(), f.target, frame, domain.TestKeyRequest{ScreenshotID: frame.ScreenshotID, Keys: []string{"Return"}}); err != nil {
		t.Fatal("active foreground type/key contract changed", err)
	}
}

func TestSessionReleaseAndNextTargetKeepSeparateEpisodes(t *testing.T) {
	f := newFixture(t)
	p := strictSessionProvider(t, f)
	old := f.screenshot(t)
	label := f.adapter.bindings[f.target.ID].session
	if err := f.adapter.Release(context.Background(), f.target); err != nil || !p.sessions[label].ended {
		t.Fatal("Release did not end its owned label", err)
	}
	starts := p.starts
	if _, err := f.adapter.Screenshot(context.Background(), f.target); !errors.Is(err, ErrRefused) || p.starts != starts {
		t.Fatal("released binding was revived", err)
	}
	next := f.target
	next.ID, next.LaunchID, next.Generation, next.ElectronPID, next.WindowID = "next-target", "next-launch", 2, 124, ""
	var err error
	next, err = f.adapter.BindWindow(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	b := f.adapter.bindings[next.ID]
	if b.session == label || b.receipt != nil || b.lastTyped != nil || next.WindowID != "777" || !p.sessions[label].ended {
		t.Fatal("next target adopted the prior episode", b, next)
	}
	shot, err := f.adapter.Screenshot(context.Background(), next)
	if err != nil || !sameTarget(shot.Frame.Target, next) {
		t.Fatal("next owned target did not capture its exact window", shot, err)
	}
	if _, err := f.adapter.Click(context.Background(), next, old, domain.TestClickRequest{ScreenshotID: old.ScreenshotID, X: 10, Y: 10}); !errors.Is(err, ErrRefused) || p.inputs != 0 {
		t.Fatal("prior target's receipt was adopted", err)
	}
	if err := f.adapter.Close(context.Background()); err != nil || !p.stopped || !p.sessions[b.session].ended {
		t.Fatal("Close did not revoke and stop only the owned episode", err)
	}
	starts = p.starts
	if _, err := f.adapter.Screenshot(context.Background(), next); !errors.Is(err, ErrRefused) || p.starts != starts {
		t.Fatal("closed adapter revived a session", err)
	}
}

func TestSessionStartFailureDoesNotCaptureOrKeepReceipts(t *testing.T) {
	for _, failure := range []string{"provider", "cancelled", "inactive"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.screenshot(t)
			b := f.adapter.bindings[f.target.ID]
			b.lastTyped = &typedFocus{point: pixel{10, 10}, bounds: f.bounds}
			cause := errors.New("provider start failure")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := f.runner.hook
			f.runner.hook = func(executable string, args []string) (Output, error) {
				if args[3] != "start_session" {
					t.Fatal("session failure continued into capture or input", args)
					return provider(executable, args)
				}
				if failure == "inactive" {
					return jsonOutput(map[string]any{"active": false}), nil
				}
				if failure == "cancelled" {
					cancel()
				}
				return Output{Stderr: []byte("cannot activate owned session")}, cause
			}
			before := len(f.runner.calls)
			_, err := f.adapter.Screenshot(ctx, f.target)
			if !errors.Is(err, ErrProvider) || !strings.Contains(err.Error(), "start_session") || b.receipt != nil || b.lastTyped != nil || len(f.runner.calls) != before+1 {
				t.Fatal("session failure lost cause, retained receipt, or continued dispatch", err, b)
			}
			if failure != "inactive" && !errors.Is(err, cause) || failure == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("provider or cancellation chain was lost", err)
			}
		})
	}
}

func TestSessionRecordingCanStartAfterIdle(t *testing.T) {
	f := newFixture(t)
	p := strictSessionProvider(t, f)
	prepareRecording(t, f)
	p.now = p.now.Add(9 * time.Minute)
	start := startFakeRecording(t, f)
	if p.revivals != 1 || start.RecorderPID != 42 {
		t.Fatal("recording validation did not start its owned session", p, start)
	}
	if _, err := f.adapter.StopRecording(context.Background(), f.target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(start.Path)); err != nil {
		t.Fatal(err)
	}
}
