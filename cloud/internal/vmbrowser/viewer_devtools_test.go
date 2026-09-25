package vmbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
)

type inspectorCall struct {
	method, session string
	args            map[string]any
}

type inspectorCDP struct {
	viewerTestCDP
	lock          sync.Mutex
	recorded      []inspectorCall
	fail          string
	inspector     bool
	lateOpen      bool
	closing       bool
	closePolls    int
	beforeHistory func()
}

func (c *inspectorCDP) Call(ctx context.Context, session, method string, params any, result any) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	args, _ := params.(map[string]any)
	c.recorded = append(c.recorded, inspectorCall{method: method, session: session, args: args})
	if c.fail == method {
		c.fail = ""
		if method == "Target.openDevTools" && c.lateOpen {
			c.inspector = true
		}
		return errors.New("injected inspector failure")
	}
	var response any
	switch method {
	case "Target.openDevTools":
		c.inspector = true
		response = map[string]any{"targetId": "inspector"}
	case "Runtime.evaluate":
		response = map[string]any{"result": map[string]any{"value": true}}
	case "Target.getDevToolsTarget":
		if c.inspector {
			response = map[string]any{"targetId": "inspector"}
		}
	case "Target.closeTarget":
		if args["targetId"] == "inspector" {
			c.closing = true
			if c.closePolls == 0 {
				c.inspector = false
			}
		}
	case "Target.attachToTarget":
		response = map[string]any{"sessionId": "session-" + args["targetId"].(string)}
	case "Target.getTargets":
		if c.closing && c.closePolls > 0 {
			c.closePolls--
			if c.closePolls == 0 {
				c.inspector = false
			}
		}
		targets := []map[string]any{{"targetId": "page", "type": "page", "url": "https://example.test/"}}
		if c.inspector {
			targets = append(targets, map[string]any{"targetId": "inspector", "type": "other", "url": bundledDevToolsURL})
		}
		response = map[string]any{"targetInfos": targets}
	case "Page.getNavigationHistory":
		if c.beforeHistory != nil {
			c.beforeHistory()
		}
		response = map[string]any{"currentIndex": 0, "entries": []any{map[string]any{"id": 1}}}
	default:
		return c.viewerTestCDP.Call(ctx, session, method, params, result)
	}
	if result != nil && response != nil {
		encoded, _ := json.Marshal(response)
		return json.Unmarshal(encoded, result)
	}
	return nil
}

func newInspectorTest(t *testing.T) (*ViewerController, *viewerSession, *inspectorCDP) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cdp := &inspectorCDP{viewerTestCDP: viewerTestCDP{engine: &viewerTestEngine{}}}
	state := &viewerSession{ctx: ctx, cancel: cancel, cdp: cdp, targetID: "page", sessionID: "session-page",
		control: make(chan browserstream.Control, 1024), frames: browserstream.NewLatest(),
		width: 800, height: 600, captureWidth: 1440, captureHeight: 900, quality: 70, fps: 15,
		epoch: 1, started: time.Now()}
	viewer := NewViewerController(ViewerControllerOptions{})
	viewer.session = state
	t.Cleanup(func() { viewer.cleanupDevTools(state); state.frames.Close() })
	return viewer, state, cdp
}

func TestViewerDevToolsLifecycle(t *testing.T) {
	viewer, state, cdp := newInspectorTest(t)
	for range 2 {
		if _, err := viewer.DevTools(true); err != nil {
			t.Fatal(err)
		}
	}
	if state.targetID != "page" || state.sessionID != "session-page" || state.displayTargetLocked() != "inspector" {
		t.Fatal("inspector changed the inspected page identity")
	}
	tabs, err := viewer.listTargets(state)
	if err != nil || len(tabs) != 1 || tabs[0].ID != "page" {
		t.Fatalf("tabs=%v err=%v", tabs, err)
	}
	for _, target := range []string{"", "page", "arbitrary-target"} {
		if err := viewer.dispatchInput(state, browserstream.Control{Kind: "text", TargetID: target, Text: "wrong"}); err == nil {
			t.Fatalf("accepted stale target %q", target)
		}
	}
	if err := viewer.dispatchInput(state, browserstream.Control{Kind: "text", TargetID: "inspector", Text: "console"}); err != nil {
		t.Fatal(err)
	}
	if call := cdp.recorded[len(cdp.recorded)-1]; call.session != "session-inspector" {
		t.Fatalf("input sent to %s", call.session)
	}
	if err := viewer.resize(state, 1000, 800); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := viewer.DevTools(false); err != nil {
			t.Fatal(err)
		}
	}
	if cdp.inspector || state.devtools != nil || state.displayTargetLocked() != "page" {
		t.Fatal("inspector survived close")
	}
	if err := viewer.dispatchInput(state, browserstream.Control{Kind: "text", TargetID: "inspector"}); err == nil {
		t.Fatal("accepted input for closed inspector")
	}
	opens := 0
	for _, call := range cdp.recorded {
		if call.method == "Target.openDevTools" {
			opens++
		}
	}
	if opens != 1 {
		t.Fatalf("duplicate open created %d inspectors", opens)
	}
}

func TestViewerInspectorCloseWaitsForDestruction(t *testing.T) {
	viewer, state, cdp := newInspectorTest(t)
	if _, err := viewer.DevTools(true); err != nil {
		t.Fatal(err)
	}
	cdp.closePolls = 3
	if _, err := viewer.DevTools(false); err != nil {
		t.Fatal(err)
	}
	if cdp.inspector || cdp.closePolls != 0 || state.devtools != nil {
		t.Fatal("close returned before inspector destruction")
	}
	if _, err := viewer.DevTools(true); err != nil {
		t.Fatal(err)
	}
	cdp.closePolls = 100
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := viewer.closeDevToolsLocked(ctx, state); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled close=%v", err)
	}
	if state.devtools == nil {
		t.Fatal("uncertain close discarded cleanup state")
	}
	cdp.closePolls = 0
}

func TestViewerDevToolsFailures(t *testing.T) {
	for _, method := range []string{"Target.openDevTools", "Target.attachToTarget", "Page.enable", "Runtime.evaluate", "Page.navigate", "Page.stopScreencast", "Emulation.setDeviceMetricsOverride", "Page.startScreencast"} {
		t.Run(method, func(t *testing.T) {
			viewer, state, cdp := newInspectorTest(t)
			cdp.fail = method
			_, err := viewer.DevTools(true)
			var commandErr *CommandError
			if !errors.As(err, &commandErr) || commandErr.Code != "BROWSER_DEVTOOLS_UNAVAILABLE" {
				t.Fatalf("error=%v", err)
			}
			if cdp.inspector || state.devtools != nil {
				t.Fatal("partial inspector leaked")
			}
			if _, err := viewer.DevTools(true); err != nil {
				t.Fatalf("retry: %v", err)
			}
		})
	}
}

func TestViewerDevToolsCanceledOpenCleanup(t *testing.T) {
	viewer, state, cdp := newInspectorTest(t)
	cdp.fail, cdp.lateOpen = "Target.openDevTools", true
	if _, err := viewer.DevTools(true); err == nil {
		t.Fatal("canceled open succeeded")
	}
	if cdp.inspector || state.devtools != nil {
		t.Fatal("lost open reply leaked inspector")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitDevToolsReady(ctx, &viewerTestCDP{}, "inspector"); !errors.Is(err, context.Canceled) {
		t.Fatalf("readiness cancellation: %v", err)
	}
}

func TestViewerDevToolsCleanupReconnectsAfterCanceledWrite(t *testing.T) {
	for _, partialOpen := range []bool{false, true} {
		t.Run(fmt.Sprint(partialOpen), func(t *testing.T) {
			viewer, state, cdp := newInspectorTest(t)
			state.cdpURL = "ws://127.0.0.1:9222/devtools/browser/test"
			recovery := &inspectorCDP{inspector: true}
			viewer.opts.DialCDP = func(ctx context.Context, endpoint string) (cdpConnection, error) {
				if ctx.Err() != nil || endpoint != state.cdpURL {
					t.Fatal("cleanup inherited cancellation or changed browser")
				}
				if _, bounded := ctx.Deadline(); !bounded {
					t.Fatal("cleanup has no deadline")
				}
				return recovery, nil
			}
			if partialOpen {
				cdp.fail = "Target.getDevToolsTarget"
				if err := viewer.cleanupInspectorTarget(state, ""); err != nil {
					t.Fatal(err)
				}
			} else {
				state.devtools = &viewerDevTools{targetID: "inspector", sessionID: "session-inspector"}
				cdp.fail = "Target.closeTarget"
				viewer.cleanupDevTools(state)
				if state.devtools != nil {
					t.Fatal("inspector state survived cleanup")
				}
			}
			if recovery.inspector {
				t.Fatal("inspector survived cleanup reconnect")
			}
		})
	}
}

func TestViewerNativeInspectorClosure(t *testing.T) {
	viewer, state, cdp := newInspectorTest(t)
	if _, err := viewer.DevTools(true); err != nil {
		t.Fatal(err)
	}
	cdp.inspector = false
	viewer.handleTargetDestroyed(state, cdpEvent{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"inspector"}`)})
	if state.devtools != nil || state.displayTargetLocked() != "page" {
		t.Fatal("native close did not restore page")
	}
}

func TestViewerDoesNotPublishReplacedSurfaceState(t *testing.T) {
	viewer, state, cdp := newInspectorTest(t)
	cdp.beforeHistory = func() {
		state.opMu.Lock()
		state.devtools = &viewerDevTools{targetID: "inspector", sessionID: "session-inspector"}
		state.opMu.Unlock()
	}
	viewer.publishState(state)
	if len(state.control) != 0 {
		t.Fatal("published page state after display switched to inspector")
	}
	cdp.beforeHistory = nil
	viewer.publishState(state)
	control := <-state.control
	if !control.DevToolsOpen || control.TargetID != "inspector" || control.ActiveTabID != "page" {
		t.Fatalf("state=%+v", control)
	}
}

func TestViewerDevToolsSwitchAndDisconnect(t *testing.T) {
	viewer, state, cdp := newInspectorTest(t)
	if _, err := viewer.DevTools(true); err != nil {
		t.Fatal(err)
	}
	if err := viewer.switchTarget(state, "next-page"); err != nil {
		t.Fatal(err)
	}
	if state.devtools != nil || cdp.inspector || state.targetID != "next-page" {
		t.Fatal("tab switch kept old inspector")
	}
	if _, err := viewer.DevTools(true); err != nil {
		t.Fatal(err)
	}
	viewer.cleanupDevTools(state)
	if cdp.inspector || state.devtools != nil || state.ctx.Err() == nil {
		t.Fatal("disconnect leaked inspector")
	}
	if _, err := viewer.DevTools(true); err == nil {
		t.Fatal("closed stream reopened inspector")
	}
}

func TestViewerDevToolsOwnershipAndAcknowledgements(t *testing.T) {
	viewer, state, cdp := newInspectorTest(t)
	release, err := viewer.opts.Arbiter.AcquireAgent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	control := browserstream.Control{Type: "devtools", Operation: "open", StreamEpoch: 1, InputSeq: 1}
	viewer.handleControl(state, control)
	if response := <-state.control; response.Code != "BROWSER_AGENT_CONTROL_ACTIVE" {
		t.Fatalf("response=%+v", response)
	}
	if cdp.inspector {
		t.Fatal("opened during agent ownership")
	}
	release()
	control.InputSeq++
	viewer.handleControl(state, control)
	if !cdp.inspector {
		t.Fatal("operator could not open inspector")
	}
	var acknowledged bool
	for len(state.control) > 0 {
		r := <-state.control
		acknowledged = acknowledged || r.Type == "input_ack" && r.InputSeq == control.InputSeq
	}
	if !acknowledged {
		t.Fatal("open not acknowledged")
	}
	viewer.handleControl(state, control)
	if response := <-state.control; response.Type != "input_ack" {
		t.Fatalf("duplicate response=%+v", response)
	}
	if err := viewer.devtoolsOperation(state, "arbitrary"); err == nil {
		t.Fatal("accepted unknown inspector operation")
	}
}

func TestViewerDevToolsConcurrentSwitch(t *testing.T) {
	viewer, state, cdp := newInspectorTest(t)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if _, err := viewer.DevTools(true); err != nil {
				t.Error(err)
			}
		})
		group.Go(func() {
			if err := viewer.switchTarget(state, "page"); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	viewer.cleanupDevTools(state)
	if cdp.inspector {
		t.Fatal("concurrent switch leaked inspector")
	}
}

func TestServiceDevToolsRequiresViewerAndRoutesCommands(t *testing.T) {
	service, capability := newTestService(t, &fakeEngine{})
	status, body := doBrowserRequest(t, service.Handler(), http.MethodPost, "/api/v1/browser/commands", capability,
		`{"sessionId":"sess-1","action":"devtools-open"}`)
	if status != http.StatusUnprocessableEntity || envelopeField(t, body, "code") != "BROWSER_VIEWER_REQUIRED" {
		t.Fatalf("status=%d body=%s", status, body)
	}
	viewer, _, cdp := newInspectorTest(t)
	service.opts.Viewer = viewer
	for _, action := range []string{"devtools-open", "devtools-close"} {
		_, err := service.dispatch(t.Context(), action, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cdp.inspector != (action == "devtools-open") {
			t.Fatal("command was not routed to viewer")
		}
	}
	viewer.session = nil
	if _, err := viewer.DevTools(true); err == nil {
		t.Fatal("opened without a viewer")
	}
	if _, err := viewer.DevTools(false); err != nil {
		t.Fatal(err)
	}
}
