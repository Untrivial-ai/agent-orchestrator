package cua

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestLockedScreenRefusesBeforeWindowAction(t *testing.T) {
	for _, action := range []string{"bind", "screenshot", "click", "type", "key", "record"} {
		t.Run(action, func(t *testing.T) {
			f := newFixture(t)
			frame := f.screenshot(t)
			f.locked = true
			before := len(f.runner.calls)
			var err error
			switch action {
			case "bind":
				target := f.target
				target.ID = "locked-attempt"
				_, err = f.adapter.BindWindow(context.Background(), target)
			case "screenshot":
				_, err = f.adapter.Screenshot(context.Background(), f.target)
			case "click":
				_, err = f.adapter.Click(context.Background(), f.target, frame, domain.TestClickRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100})
			case "type":
				_, err = f.adapter.Type(context.Background(), f.target, frame, domain.TestTypeRequest{ScreenshotID: frame.ScreenshotID, X: 100, Y: 100, Text: "blocked"})
			case "key":
				_, err = f.adapter.Key(context.Background(), f.target, frame, domain.TestKeyRequest{ScreenshotID: frame.ScreenshotID, Keys: []string{"Return"}})
			case "record":
				_, err = f.adapter.StartRecording(context.Background(), f.target, filepath.Join(shortRoot(t), "evidence"))
			}
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "screen_locked") || !strings.Contains(err.Error(), "IOConsoleLocked=true") {
				t.Fatalf("lock reason lost: %v", err)
			}
			for _, call := range f.runner.calls[before:] {
				if call.executable == "/usr/bin/open" || len(call.args) == 5 && (call.args[3] == "bring_to_front" || call.args[3] == "get_window_state" || call.args[3] == "click" || call.args[3] == "type_text" || call.args[3] == "press_key") {
					t.Fatal("locked screen continued to window action", call)
				}
			}
		})
	}
}

func TestScreenLockMustBeProved(t *testing.T) {
	for _, plist := range []string{"<plist><dict/></plist>", "<plist><dict><key>IOConsoleLocked</key><string>false</string></dict></plist>", "<plist><dict><key>IOConsoleLocked</key><false/>"} {
		f := newFixture(t)
		f.runner.hook = func(string, []string) (Output, error) { return Output{Stdout: []byte(plist)}, nil }
		if err := f.adapter.checkScreenUnlocked(context.Background()); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "screen_lock_unknown") {
			t.Fatalf("unproved lock state accepted: %v", err)
		}
	}
}
