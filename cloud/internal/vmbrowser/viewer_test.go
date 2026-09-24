package vmbrowser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
	"github.com/coder/websocket"
)

type viewerTestChromium struct{}

func (viewerTestChromium) EnsureRunning(context.Context) (Endpoint, error) {
	return Endpoint{WebSocketURL: "ws://loopback.invalid/devtools/browser/test"}, nil
}

type viewerTestEngine struct {
	mu      sync.Mutex
	text    string
	clicked bool
}

func (e *viewerTestEngine) Execute(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	snapshot := e.text
	if e.clicked {
		snapshot = "clicked " + snapshot
	}
	return map[string]any{"action": action, "snapshot": snapshot}, nil
}

func (*viewerTestEngine) Screenshot(context.Context) (string, int, int, error) {
	return "", 0, 0, nil
}

func (*viewerTestEngine) Close(context.Context) error { return nil }

type viewerTabEngine struct {
	actions     []string
	active      string
	tabs        []string
	selectedArg string
	closedArg   string
	cdp         *viewerTestCDP
	targets     map[string]string
	urls        map[string]string
	titles      map[string]string
}

func (e *viewerTabEngine) Execute(_ context.Context, action string, args map[string]any) (map[string]any, error) {
	e.actions = append(e.actions, action)
	switch action {
	case "tab-select":
		e.active, _ = args["tabId"].(string)
		e.selectedArg = e.active
	case "tab-new":
		e.active = "tab-3"
		e.tabs = append(e.tabs, e.active)
		e.targets[e.active] = "cdp-3"
		e.urls[e.active], _ = args["url"].(string)
		e.titles[e.active] = "Third"
		e.cdp.targets = append(e.cdp.targets, viewerTestTarget{
			id: "cdp-3", url: e.urls[e.active], title: e.titles[e.active],
		})
		return map[string]any{"tabId": e.active}, nil
	case "tab-close":
		closing, _ := args["tabId"].(string)
		if closing == "" {
			closing = e.active
		}
		e.closedArg = closing
		remaining := e.tabs[:0]
		for _, tab := range e.tabs {
			if tab != closing {
				remaining = append(remaining, tab)
			}
		}
		e.tabs = remaining
		if len(e.tabs) > 0 {
			e.active = e.tabs[0]
		} else {
			e.active = ""
		}
		closedTarget := e.targets[closing]
		remainingTargets := e.cdp.targets[:0]
		for _, target := range e.cdp.targets {
			if target.id != closedTarget {
				remainingTargets = append(remainingTargets, target)
			}
		}
		e.cdp.targets = remainingTargets
	case "tabs":
		tabs := make([]any, 0, len(e.tabs))
		for _, tab := range e.tabs {
			tabs = append(tabs, map[string]any{
				"tabId": tab, "active": tab == e.active,
				"url": e.urls[tab], "title": e.titles[tab],
			})
		}
		return map[string]any{"tabs": tabs}, nil
	}
	return map[string]any{}, nil
}

func (*viewerTabEngine) Screenshot(context.Context) (string, int, int, error) {
	return "", 0, 0, nil
}

func (*viewerTabEngine) Close(context.Context) error { return nil }

type viewerTestCDP struct {
	events  chan cdpEvent
	engine  *viewerTestEngine
	mu      sync.Mutex
	calls   []string
	targets []viewerTestTarget
}

type viewerTestTarget struct {
	id    string
	title string
	url   string
}

func (c *viewerTestCDP) Call(_ context.Context, _ string, method string, params any, result any) error {
	c.mu.Lock()
	c.calls = append(c.calls, method)
	c.mu.Unlock()
	if method == "Input.insertText" {
		payload, _ := json.Marshal(params)
		var input struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(payload, &input)
		c.engine.mu.Lock()
		c.engine.text += input.Text
		c.engine.mu.Unlock()
	}
	if method == "Input.dispatchMouseEvent" {
		payload, _ := json.Marshal(params)
		var input struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(payload, &input)
		if input.Type == "mousePressed" {
			c.engine.mu.Lock()
			c.engine.clicked = true
			c.engine.mu.Unlock()
		}
	}
	var response any
	switch method {
	case "Target.getTargets":
		targets := c.targets
		if targets == nil {
			targets = []viewerTestTarget{{id: "tab-1", title: "Test", url: "https://example.test/"}}
		}
		targetInfos := make([]map[string]any, 0, len(targets))
		for _, target := range targets {
			targetInfos = append(targetInfos, map[string]any{
				"targetId": target.id, "type": "page", "title": target.title, "url": target.url,
			})
		}
		response = map[string]any{"targetInfos": targetInfos}
	case "Target.attachToTarget":
		response = map[string]any{"sessionId": "cdp-session-1"}
	case "Page.getNavigationHistory":
		response = map[string]any{"currentIndex": 0, "entries": []map[string]any{{"id": 1}}}
	}
	if result != nil && response != nil {
		encoded, _ := json.Marshal(response)
		_ = json.Unmarshal(encoded, result)
	}
	return nil
}

func (c *viewerTestCDP) Events() <-chan cdpEvent { return c.events }
func (*viewerTestCDP) Close() error              { return nil }

func (c *viewerTestCDP) called(method string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, call := range c.calls {
		if call == method {
			return true
		}
	}
	return false
}

func TestViewerLoopbackStreamsFrameAndSharesInputWithAgentEngine(t *testing.T) {
	engine := &viewerTestEngine{}
	cdp := &viewerTestCDP{events: make(chan cdpEvent, 8), engine: engine}
	viewer := NewViewerController(ViewerControllerOptions{
		Chromium: viewerTestChromium{},
		Engine:   engine,
		DialCDP:  func(context.Context, string) (cdpConnection, error) { return cdp, nil },
	})
	authority := browsercontract.NewAuthority()
	capability, verifier, err := authority.Issue("session-1")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceOptions{
		SessionID: "session-1", CapabilityVerifier: verifier,
		Engine: engine, Viewer: viewer, Arbiter: viewer.opts.Arbiter,
	})
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	streamURL := "ws" + strings.TrimPrefix(server.URL, "http") + ViewerStreamRoute
	header := http.Header{}
	header.Set(browsercontract.CapabilityHeader, capability)
	connection, _, err := websocket.Dial(ctx, streamURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial viewer: %v", err)
	}
	defer connection.CloseNow()

	for received := 0; received < 3; {
		kind, payload, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var control browserstream.Control
		if json.Unmarshal(payload, &control) == nil && (control.Type == "hello" || control.Type == "attached" || control.Type == "state") {
			received++
		}
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xd9}
	frameParams, _ := json.Marshal(map[string]any{
		"data": base64.StdEncoding.EncodeToString(jpeg), "sessionId": 1,
	})
	cdp.events <- cdpEvent{Method: "Page.screencastFrame", SessionID: "cdp-session-1", Params: frameParams}
	for {
		kind, payload, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageBinary {
			continue
		}
		frame, err := browserstream.DecodeFrame(payload)
		if err != nil || !bytes.Equal(frame.JPEG, jpeg) || frame.TargetID != "tab-1" {
			t.Fatalf("frame=%+v err=%v", frame, err)
		}
		break
	}

	click, _ := json.Marshal(browserstream.Control{
		Type: "input", Version: browserstream.Version, InputSeq: 1,
		Kind: "pointerDown", X: 20, Y: 30, Button: "left", Buttons: 1,
	})
	if err := connection.Write(ctx, websocket.MessageText, click); err != nil {
		t.Fatal(err)
	}
	clickAck := waitViewerInputAck(t, ctx, connection, 1)
	if clickAck.MinFrameSeq != 2 {
		t.Fatalf("click min frame = %d, want 2", clickAck.MinFrameSeq)
	}
	input, _ := json.Marshal(browserstream.Control{
		Type: "input", Version: browserstream.Version, InputSeq: 2,
		Kind: "text", Text: "shared",
	})
	if err := connection.Write(ctx, websocket.MessageText, input); err != nil {
		t.Fatal(err)
	}
	waitViewerInputAck(t, ctx, connection, 2)
	if !cdp.called("Input.dispatchMouseEvent") {
		t.Fatal("viewer click was not dispatched to Chromium")
	}
	if !cdp.called("Input.insertText") {
		t.Fatal("viewer text was not dispatched to Chromium")
	}

	commandBody := []byte(`{"sessionId":"session-1","action":"snapshot"}`)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+browsercontract.RouteCommands, bytes.NewReader(commandBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(browsercontract.CapabilityHeader, capability)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var command struct {
		Result map[string]any `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&command); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || command.Result["text"] != "clicked shared" {
		t.Fatalf("snapshot status=%d result=%v", response.StatusCode, command.Result)
	}
}

func waitViewerInputAck(t *testing.T, ctx context.Context, connection *websocket.Conn, inputSeq uint64) browserstream.Control {
	t.Helper()
	for {
		kind, payload, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var control browserstream.Control
		if json.Unmarshal(payload, &control) == nil && control.Type == "input_ack" && control.InputSeq == inputSeq {
			return control
		}
	}
}

func TestViewerAdaptationStepsDownAndRecoversConservatively(t *testing.T) {
	cdp := &viewerTestCDP{events: make(chan cdpEvent, 1), engine: &viewerTestEngine{}}
	viewer := NewViewerController(ViewerControllerOptions{DialCDP: func(context.Context, string) (cdpConnection, error) {
		return cdp, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &viewerSession{
		ctx: ctx, cancel: cancel, cdp: cdp, control: make(chan browserstream.Control, 16),
		frames: browserstream.NewLatest(), sessionID: "cdp-session-1", targetID: "tab-1",
		width: 1440, height: 900, epoch: 1, quality: 70, fps: 15,
		captureWidth: 1440, captureHeight: 900,
	}
	defer state.frames.Close()
	base := time.Now()
	viewer.adaptViewer(state, true, base)
	viewer.adaptViewer(state, true, base.Add(3*time.Second))
	viewer.adaptViewer(state, true, base.Add(6*time.Second))
	viewer.adaptViewer(state, true, base.Add(9*time.Second))
	viewer.adaptViewer(state, true, base.Add(12*time.Second))
	if state.fps != 6 || state.quality != 55 || state.captureWidth != 1280 || state.captureHeight != 720 {
		t.Fatalf("step-down state = fps %d quality %d dimensions %dx%d", state.fps, state.quality, state.captureWidth, state.captureHeight)
	}
	viewer.adaptViewer(state, false, base.Add(13*time.Second))
	viewer.adaptViewer(state, false, base.Add(23*time.Second))
	if state.captureWidth != 1440 || state.captureHeight != 900 || state.quality != 55 || state.fps != 6 {
		t.Fatalf("first recovery step = fps %d quality %d dimensions %dx%d", state.fps, state.quality, state.captureWidth, state.captureHeight)
	}
}

func TestViewerThrottleFlushesNewestDeferredFrame(t *testing.T) {
	cdp := &viewerTestCDP{events: make(chan cdpEvent, 1), engine: &viewerTestEngine{}}
	viewer := NewViewerController(ViewerControllerOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	state := &viewerSession{
		ctx: ctx, cancel: cancel, cdp: cdp,
		control: make(chan browserstream.Control, 16), frames: browserstream.NewLatest(),
		sessionID: "cdp-session-1", targetID: "tab-1", width: 800, height: 600,
		epoch: 1, started: started, attachStarted: started,
		quality: 70, fps: 15, captureWidth: 1440, captureHeight: 900,
	}
	defer state.frames.Close()
	event := func(jpeg []byte, session int) cdpEvent {
		params, _ := json.Marshal(map[string]any{
			"data": base64.StdEncoding.EncodeToString(jpeg), "sessionId": session,
		})
		return cdpEvent{Method: "Page.screencastFrame", SessionID: "cdp-session-1", Params: params}
	}
	firstJPEG := []byte{0xff, 0xd8, 1, 0xff, 0xd9}
	viewer.acceptScreencastFrame(state, event(firstJPEG, 1))
	firstPayload, ok := state.frames.Next(ctx)
	if !ok {
		t.Fatal("initial frame was not published")
	}
	first, err := browserstream.DecodeFrame(firstPayload)
	if err != nil || !bytes.Equal(first.JPEG, firstJPEG) {
		t.Fatalf("first frame=%+v err=%v", first, err)
	}

	viewer.acceptScreencastFrame(state, event([]byte{0xff, 0xd8, 2, 0xff, 0xd9}, 2))
	lastJPEG := []byte{0xff, 0xd8, 3, 0xff, 0xd9}
	viewer.acceptScreencastFrame(state, event(lastJPEG, 3))
	lastPayload, ok := state.frames.Next(ctx)
	if !ok {
		t.Fatal("deferred frame was never flushed")
	}
	last, err := browserstream.DecodeFrame(lastPayload)
	if err != nil || last.Sequence != 2 || !bytes.Equal(last.JPEG, lastJPEG) {
		t.Fatalf("deferred frame=%+v err=%v", last, err)
	}
}

func TestViewerTabOperationsSynchronizeSharedEngine(t *testing.T) {
	cdp := &viewerTestCDP{
		events: make(chan cdpEvent, 8), engine: &viewerTestEngine{},
		targets: []viewerTestTarget{
			{id: "cdp-1", title: "First", url: "https://one.example.test/"},
			{id: "cdp-2", title: "Second", url: "https://two.example.test/"},
		},
	}
	engine := &viewerTabEngine{
		active: "tab-1", tabs: []string{"tab-1", "tab-2"}, cdp: cdp,
		targets: map[string]string{"tab-1": "cdp-1", "tab-2": "cdp-2"},
		urls: map[string]string{
			"tab-1": "https://one.example.test/", "tab-2": "https://two.example.test/",
		},
		titles: map[string]string{"tab-1": "First", "tab-2": "Second"},
	}
	viewer := NewViewerController(ViewerControllerOptions{Engine: engine})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &viewerSession{
		ctx: ctx, cancel: cancel, cdp: cdp,
		control: make(chan browserstream.Control, 32), frames: browserstream.NewLatest(),
		sessionID: "cdp-session-1", targetID: "cdp-1", width: 1280, height: 720,
		quality: 70, fps: 15, captureWidth: 1440, captureHeight: 900,
	}

	if err := viewer.tabOperation(state, browserstream.Control{Operation: "select", TabID: "cdp-2"}); err != nil {
		t.Fatal(err)
	}
	if state.targetID != "cdp-2" || engine.active != "tab-2" || engine.selectedArg != "tab-2" {
		t.Fatalf("selected viewer=%q engine=%q", state.targetID, engine.active)
	}
	if err := viewer.tabOperation(state, browserstream.Control{Operation: "new", URL: "https://example.test/new"}); err != nil {
		t.Fatal(err)
	}
	if state.targetID != "cdp-3" || engine.active != "tab-3" {
		t.Fatalf("new viewer=%q engine=%q", state.targetID, engine.active)
	}
	if err := viewer.tabOperation(state, browserstream.Control{Operation: "close"}); err != nil {
		t.Fatal(err)
	}
	if state.targetID != "cdp-1" || engine.active != "tab-1" || engine.closedArg != "tab-3" {
		t.Fatalf("closed viewer=%q engine=%q", state.targetID, engine.active)
	}
	if got := strings.Join(engine.actions, ","); got != "tabs,tab-select,tab-new,tabs,tab-close,tabs,tabs" {
		t.Fatalf("engine actions = %q", got)
	}
}

func TestViewerPopupSynchronizesSharedEngine(t *testing.T) {
	cdp := &viewerTestCDP{
		events: make(chan cdpEvent, 1), engine: &viewerTestEngine{},
		targets: []viewerTestTarget{
			{id: "cdp-1", title: "First", url: "https://one.example.test/"},
			{id: "popup-cdp", title: "Popup", url: "https://popup.example.test/"},
		},
	}
	engine := &viewerTabEngine{
		active: "tab-1", tabs: []string{"tab-1", "tab-2"}, cdp: cdp,
		targets: map[string]string{"tab-1": "cdp-1", "tab-2": "popup-cdp"},
		urls: map[string]string{
			"tab-1": "https://one.example.test/", "tab-2": "https://popup.example.test/",
		},
		titles: map[string]string{"tab-1": "First", "tab-2": "Popup"},
	}
	viewer := NewViewerController(ViewerControllerOptions{Engine: engine})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &viewerSession{
		ctx: ctx, cancel: cancel, cdp: cdp,
		control: make(chan browserstream.Control, 32), frames: browserstream.NewLatest(),
		sessionID: "cdp-session-1", targetID: "cdp-1", width: 1280, height: 720,
		quality: 70, fps: 15, captureWidth: 1440, captureHeight: 900,
	}
	defer state.frames.Close()
	if !viewer.opts.Arbiter.TryUser(true) {
		t.Fatal("user control was not acquired")
	}
	params, _ := json.Marshal(map[string]any{"targetInfo": map[string]any{
		"targetId": "popup-cdp", "type": "page", "openerId": "cdp-1",
	}})
	viewer.handleTargetCreated(state, cdpEvent{Method: "Target.targetCreated", Params: params})
	if state.targetID != "popup-cdp" || engine.active != "tab-2" || engine.selectedArg != "tab-2" {
		t.Fatalf("popup viewer=%q engine=%q", state.targetID, engine.active)
	}
	if got := strings.Join(engine.actions, ","); got != "tabs,tab-select" {
		t.Fatalf("popup engine actions = %v", engine.actions)
	}
}

func TestViewerDialogIsAllowlistedAndCrashResetsControl(t *testing.T) {
	engine := &viewerTestEngine{}
	cdp := &viewerTestCDP{events: make(chan cdpEvent, 2), engine: engine}
	viewer := NewViewerController(ViewerControllerOptions{Engine: engine})
	ctx, cancel := context.WithCancel(context.Background())
	state := &viewerSession{
		ctx: ctx, cancel: cancel, cdp: cdp,
		control: make(chan browserstream.Control, 32), frames: browserstream.NewLatest(),
		sessionID: "cdp-session-1", targetID: "tab-1", width: 1280, height: 720,
		quality: 70, fps: 15, captureWidth: 1440, captureHeight: 900,
	}
	defer state.frames.Close()

	if err := viewer.dialogOperation(state, browserstream.Control{Operation: "accept", Text: "approved"}); err != nil {
		t.Fatal(err)
	}
	if !cdp.called("Page.handleJavaScriptDialog") {
		t.Fatal("dialog decision was not dispatched through the allowlisted CDP method")
	}
	if err := viewer.dialogOperation(state, browserstream.Control{Operation: "accept", Text: strings.Repeat("x", (8<<10)+1)}); err == nil {
		t.Fatal("oversized dialog text was accepted")
	}

	if !viewer.opts.Arbiter.TryUser(true) {
		t.Fatal("user control was not acquired")
	}
	done := make(chan struct{})
	go func() {
		viewer.readCDPEvents(state)
		close(done)
	}()
	cdp.events <- cdpEvent{Method: "Inspector.targetCrashed"}
	close(cdp.events)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("crash event did not stop the viewer session")
	}
	if viewer.opts.Arbiter.Owner() != ControlIdle {
		t.Fatalf("owner after crash = %q", viewer.opts.Arbiter.Owner())
	}
	select {
	case <-state.ctx.Done():
	default:
		t.Fatal("viewer context remained active after browser crash")
	}
	foundRestart := false
	for len(state.control) > 0 {
		control := <-state.control
		if control.Type == "error" && control.Code == "BROWSER_RESTARTING" {
			foundRestart = true
		}
	}
	if !foundRestart {
		t.Fatal("browser crash did not publish a restart signal")
	}
}
