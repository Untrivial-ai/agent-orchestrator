package vmbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
	"github.com/coder/websocket"
)

func TestRealViewerResizeAndEditing(t *testing.T) {
	if os.Getenv("AO_TEST_REAL_BROWSER") != "1" {
		t.Skip("requires the worker image's Chromium and browser command binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	chromium := NewChromium(ChromiumOptions{BinaryPath: "/usr/bin/chromium", UserDataDir: filepath.Join(root, "profile")})
	engine := NewEngine(chromium, nil, EngineOptions{BinaryPath: "/usr/local/lib/ao/agent-browser", Root: root, SessionID: "regression"})
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = engine.Close(cleanup)
	}()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<title>Editing fixture</title><input aria-label="Shared value" style="position:absolute;left:20px;top:80px;width:240px;height:40px" oninput="document.querySelector('output').textContent=this.value"><output style="position:absolute;top:150px"></output>`)
	}))
	defer fixture.Close()
	viewer := NewViewerController(ViewerControllerOptions{Chromium: chromium, Engine: engine,
		Logger: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	token, verifier, err := browsercontract.NewAuthority().Issue("regression")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceOptions{SessionID: "regression", CapabilityVerifier: verifier, Engine: engine, Viewer: viewer, Arbiter: viewer.opts.Arbiter})
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	if chromium.CurrentStatus().Running {
		t.Fatal("Chromium started without browser intent")
	}
	header := http.Header{browsercontract.CapabilityHeader: []string{token}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+ViewerStreamRoute, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(browserstream.MaxFrameBytes + 512)
	var epoch, sequence uint64
	var lastFrame browserstream.Frame
	var lastState browserstream.Control
	wait := func(match func(browserstream.Control) bool, frameWidth, frameHeight int, minimum uint64) browserstream.Control {
		t.Helper()
		for {
			kind, payload, err := conn.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if kind == websocket.MessageBinary {
				frame, err := browserstream.DecodeFrame(payload)
				if err != nil {
					t.Fatal(err)
				}
				lastFrame = frame
				if frameWidth > 0 && int(frame.Width) == frameWidth && int(frame.Height) == frameHeight && frame.Sequence >= minimum {
					return browserstream.Control{}
				}
				continue
			}
			var control browserstream.Control
			if err := json.Unmarshal(payload, &control); err != nil {
				t.Fatal(err)
			}
			if control.Type == "hello" {
				epoch = control.StreamEpoch
			}
			if control.Type == "state" {
				lastState = control
			}
			if control.Type == "input_rejected" {
				t.Fatalf("input rejected: %s", control.Code)
			}
			if match != nil && match(control) {
				return control
			}
		}
	}
	wait(func(c browserstream.Control) bool { return c.Type == "attached" }, 0, 0, 0)
	send := func(control browserstream.Control) browserstream.Control {
		t.Helper()
		sequence++
		control.Version, control.StreamEpoch, control.InputSeq = browserstream.Version, epoch, sequence
		payload, _ := json.Marshal(control)
		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
			t.Fatal(err)
		}
		return wait(func(c browserstream.Control) bool { return c.Type == "input_ack" && c.InputSeq == sequence }, 0, 0, 0)
	}
	for _, size := range [][2]int{{800, 600}, {1000, 800}, {800, 600}} {
		ack := send(browserstream.Control{Type: "viewport", Width: size[0], Height: size[1]})
		if int(lastFrame.Width) != size[0] || int(lastFrame.Height) != size[1] || lastFrame.Sequence < ack.MinFrameSeq {
			wait(nil, size[0], size[1], ack.MinFrameSeq)
		}
	}
	send(browserstream.Control{Type: "navigate", Operation: "open", URL: fixture.URL})
	loaded := func(c browserstream.Control) bool {
		return c.Type == "state" && c.Title == "Editing fixture" && !c.IsLoading
	}
	if !loaded(lastState) {
		wait(loaded, 0, 0, 0)
	}
	send(browserstream.Control{Type: "input", Kind: "pointerDown", X: 80, Y: 100, Button: "left", Buttons: 1, ClickCount: 1})
	send(browserstream.Control{Type: "input", Kind: "pointerUp", X: 80, Y: 100, Button: "left", ClickCount: 1})
	send(browserstream.Control{Type: "input", Kind: "text", Text: "old value"})
	send(browserstream.Control{Type: "input", Kind: "keyDown", Key: "a", CodeValue: "KeyA", Modifiers: 2})
	send(browserstream.Control{Type: "input", Kind: "keyUp", Key: "a", CodeValue: "KeyA", Modifiers: 2})
	send(browserstream.Control{Type: "input", Kind: "text", Text: "replacement"})
	expectValue := func(want, absent string) {
		t.Helper()
		result, err := engine.Execute(ctx, "snapshot", nil)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		if !strings.Contains(string(encoded), want) || strings.Contains(string(encoded), absent) {
			t.Fatalf("remote editing: want %q without %q, got %s", want, absent, encoded)
		}
	}
	expectValue("replacement", "old value")
	for _, shortcut := range []struct{ key, code, want, absent string }{
		{"z", "KeyZ", "old value", "replacement"},
		{"y", "KeyY", "replacement", "old value"},
	} {
		send(browserstream.Control{Type: "input", Kind: "keyDown", Key: shortcut.key, CodeValue: shortcut.code, Modifiers: 2})
		send(browserstream.Control{Type: "input", Kind: "keyUp", Key: shortcut.key, CodeValue: shortcut.code, Modifiers: 2})
		expectValue(shortcut.want, shortcut.absent)
	}
}
