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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	chromium := NewChromium(ChromiumOptions{BinaryPath: "/usr/bin/chromium", UserDataDir: filepath.Join(root, "profile")})
	engine := NewEngine(chromium, nil, EngineOptions{BinaryPath: "/usr/local/lib/ao/agent-browser", Root: root, SessionID: "regression"})
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = engine.Close(cleanup)
	}()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devtools-network" {
			_, _ = fmt.Fprint(w, "network fixture")
			return
		}
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
		if control.Type == "input" {
			viewer.mu.Lock()
			state := viewer.session
			viewer.mu.Unlock()
			state.opMu.Lock()
			control.TargetID = state.displayTargetLocked()
			state.opMu.Unlock()
		}
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
	t.Run("DevTools", func(t *testing.T) {
		viewer.mu.Lock()
		state := viewer.session
		viewer.mu.Unlock()
		state.opMu.Lock()
		pageID, pageSession := state.targetID, state.sessionID
		state.opMu.Unlock()
		send(browserstream.Control{Type: "devtools", Operation: "open"})
		state.opMu.Lock()
		if state.devtools == nil {
			state.opMu.Unlock()
			t.Fatal("inspector not opened")
		}
		inspectorID, inspectorSession := state.devtools.targetID, state.devtools.sessionID
		state.opMu.Unlock()
		evaluate := func(session, expression string, result any) {
			t.Helper()
			var response struct {
				Result struct {
					Value json.RawMessage `json:"value"`
				} `json:"result"`
				ExceptionDetails json.RawMessage `json:"exceptionDetails"`
			}
			if err := state.cdp.Call(ctx, session, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.ExceptionDetails) > 0 {
				t.Fatalf("inspector evaluation: %s", response.ExceptionDetails)
			}
			if err := json.Unmarshal(response.Result.Value, result); err != nil {
				t.Fatalf("decode evaluation: %v (%s)", err, response.Result.Value)
			}
		}
		type axNode struct {
			Role struct {
				Value string `json:"value"`
			} `json:"role"`
			Name struct {
				Value string `json:"value"`
			} `json:"name"`
			BackendID int `json:"backendDOMNodeId"`
		}
		inspectorNodes := func() []axNode {
			t.Helper()
			var result struct {
				Nodes []axNode `json:"nodes"`
			}
			if err := state.cdp.Call(ctx, inspectorSession, "Accessibility.getFullAXTree", nil, &result); err != nil {
				t.Fatal(err)
			}
			return result.Nodes
		}
		clickRole := func(role, name string) {
			t.Helper()
			var nodeID int
			deadline := time.Now().Add(8 * time.Second)
			for nodeID == 0 && time.Now().Before(deadline) {
				for _, node := range inspectorNodes() {
					if node.Role.Value == role && node.Name.Value == name {
						nodeID = node.BackendID
						break
					}
				}
				if nodeID == 0 {
					time.Sleep(50 * time.Millisecond)
				}
			}
			if nodeID == 0 {
				t.Fatalf("%s %s missing", role, name)
			}
			var box struct {
				Model struct {
					Content []float64 `json:"content"`
				} `json:"model"`
			}
			if err := state.cdp.Call(ctx, inspectorSession, "DOM.getBoxModel", map[string]any{"backendNodeId": nodeID}, &box); err != nil {
				t.Fatal(err)
			}
			if len(box.Model.Content) != 8 {
				t.Fatal("panel has no bounds")
			}
			x, y := (box.Model.Content[0]+box.Model.Content[4])/2, (box.Model.Content[1]+box.Model.Content[5])/2
			send(browserstream.Control{Type: "input", Kind: "pointerDown", X: x, Y: y, Button: "left", Buttons: 1, ClickCount: 1})
			send(browserstream.Control{Type: "input", Kind: "pointerUp", X: x, Y: y, Button: "left", ClickCount: 1})
		}
		clickPanel := func(name string) { clickRole("tab", name) }
		inspectorContains := func(want string) {
			t.Helper()
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				for _, node := range inspectorNodes() {
					if strings.Contains(node.Name.Value, want) {
						return
					}
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatalf("inspector missing %q", want)
		}
		clickPanel("Console")
		clickRole("textbox", "Console prompt")
		send(browserstream.Control{Type: "input", Kind: "text", Text: `document.querySelector('input').value='devtools-console'; document.querySelector('input').dispatchEvent(new Event('input')); inspect(document.querySelector('input'));`})
		send(browserstream.Control{Type: "input", Kind: "keyDown", Key: "Enter", CodeValue: "Enter"})
		send(browserstream.Control{Type: "input", Kind: "keyUp", Key: "Enter", CodeValue: "Enter"})
		var value string
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			evaluate(pageSession, `document.querySelector('input').value`, &value)
			if value == "devtools-console" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if value != "devtools-console" {
			for _, node := range inspectorNodes() {
				if node.Name.Value != "" {
					t.Logf("%s: %s", node.Role.Value, node.Name.Value)
				}
			}
			t.Fatalf("Console keystrokes did not reach inspected page: %q", value)
		}
		expectValue("devtools-console", "replacement")
		clickPanel("Elements")
		inspectorContains("Shared value")
		clickPanel("Network")
		var networkBody string
		evaluate(pageSession, `fetch('/devtools-network').then(r => r.text())`, &networkBody)
		if networkBody != "network fixture" {
			t.Fatal("network fixture did not respond")
		}
		inspectorContains("devtools-network")
		ack := send(browserstream.Control{Type: "viewport", Width: 1000, Height: 800})
		if lastFrame.TargetID != inspectorID || lastFrame.Sequence < ack.MinFrameSeq {
			wait(nil, 1000, 800, ack.MinFrameSeq)
		}
		if lastFrame.TargetID != inspectorID {
			t.Fatal("viewer did not stream inspector frame")
		}
		tabs, err := viewer.listTargets(state)
		if err != nil || len(tabs) != 1 || tabs[0].ID != pageID {
			t.Fatalf("inspector changed tabs: %v, %v", tabs, err)
		}
		send(browserstream.Control{Type: "devtools", Operation: "close"})
		send(browserstream.Control{Type: "devtools", Operation: "open"})
		_ = conn.CloseNow()
		deadline = time.Now().Add(5 * time.Second)
		for viewer.Attached() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if viewer.Attached() {
			t.Fatal("viewer did not detach")
		}
		endpoint, err := chromium.EnsureRunning(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cdp, err := dialWebsocketCDP(ctx, endpoint.WebSocketURL)
		if err != nil {
			t.Fatal(err)
		}
		defer cdp.Close()
		var targets struct {
			TargetInfos []struct {
				URL string `json:"url"`
			} `json:"targetInfos"`
		}
		// Chromium acknowledges close before publishing target destruction.
		deadline = time.Now().Add(5 * time.Second)
		for {
			if err := cdp.Call(ctx, "", "Target.getTargets", nil, &targets); err != nil {
				t.Fatal(err)
			}
			inspectorRemaining := false
			for _, target := range targets.TargetInfos {
				inspectorRemaining = inspectorRemaining || strings.HasPrefix(target.URL, "devtools:")
			}
			if !inspectorRemaining {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("disconnect leaked native inspector")
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	t.Run("ReconnectSelectedTab", func(t *testing.T) {
		connect := func() browserstream.Control {
			t.Helper()
			var err error
			conn, _, err = websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+ViewerStreamRoute, &websocket.DialOptions{HTTPHeader: header})
			if err != nil {
				t.Fatal(err)
			}
			conn.SetReadLimit(browserstream.MaxFrameBytes + 512)
			connection := conn
			t.Cleanup(func() { _ = connection.CloseNow() })
			wait(func(c browserstream.Control) bool { return c.Type == "attached" }, 0, 0, 0)
			return wait(func(c browserstream.Control) bool { return c.Type == "state" }, 0, 0, 0)
		}
		disconnect := func() {
			t.Helper()
			_ = conn.CloseNow()
			deadline := time.Now().Add(5 * time.Second)
			for viewer.Attached() && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if viewer.Attached() {
				t.Fatal("viewer did not detach")
			}
		}
		if _, err := engine.Execute(ctx, "tab-new", map[string]any{"url": fixture.URL + "/second"}); err != nil {
			t.Fatal(err)
		}
		tabs, err := viewer.engineTabs(ctx)
		if err != nil || len(tabs) != 2 {
			t.Fatalf("tabs=%v err=%v", tabs, err)
		}
		for _, tab := range tabs {
			if _, err := engine.Execute(ctx, "tab-select", map[string]any{"tabId": tab.id}); err != nil {
				t.Fatal(err)
			}
			state := connect()
			if state.URL != tab.url {
				t.Fatalf("reconnected to %q, want selected tab %q", state.URL, tab.url)
			}
			selected, err := viewer.engineActiveTabID(ctx)
			if err != nil || selected != tab.id {
				t.Fatalf("viewer changed engine tab: %s err=%v", selected, err)
			}
			disconnect()
		}
		connect()
		send(browserstream.Control{Type: "tab", Operation: "new", URL: fixture.URL + "/second"})
		viewer.mu.Lock()
		current := viewer.session
		viewer.mu.Unlock()
		current.opMu.Lock()
		wantTarget := current.targetID
		current.opMu.Unlock()
		disconnect()
		if state := connect(); state.ActiveTabID != wantTarget {
			t.Fatalf("identical-page reconnect target=%q want=%q", state.ActiveTabID, wantTarget)
		}
		disconnect()
	})
}
