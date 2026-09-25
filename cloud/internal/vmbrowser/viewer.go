package vmbrowser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
	"github.com/coder/websocket"
)

const (
	defaultViewerWidth  = 1280
	defaultViewerHeight = 720
	minViewerWidth      = 320
	minViewerHeight     = 240
	maxViewerWidth      = 1440
	maxViewerHeight     = 900
	viewerControlBuffer = 64
	viewerInputHistory  = 128
)

type chromiumStarter interface {
	EnsureRunning(context.Context) (Endpoint, error)
}

type ViewerControllerOptions struct {
	Chromium chromiumStarter
	Engine   EngineLike
	Arbiter  *ControlArbiter
	DialCDP  cdpDialer
	Logger   *slog.Logger
}

// ViewerController owns the second, restricted CDP session used for the Cloud
// viewer. It permits one loopback viewer connection and never exposes raw CDP.
type ViewerController struct {
	opts    ViewerControllerOptions
	mu      sync.Mutex
	active  bool
	session *viewerSession
	epoch   atomic.Uint64
}

type viewerSession struct {
	ctx               context.Context
	cancel            context.CancelFunc
	cdp               cdpConnection
	control           chan browserstream.Control
	frames            *browserstream.Latest
	writeMu           sync.Mutex
	opMu              sync.Mutex
	targetMu          sync.Mutex
	devtools          *viewerDevTools
	cdpURL            string
	sessionID         string
	targetID          string
	width             int
	height            int
	sequence          uint64
	epoch             uint64
	started           time.Time
	rateMu            sync.Mutex
	rateStart         time.Time
	rateCount         int
	loading           bool
	dialogOpen        bool
	dialogType        string
	dialogText        string
	dialogPrompt      string
	quality           int
	fps               int
	captureWidth      int
	captureHeight     int
	lastFrame         time.Time
	viewportChangedAt time.Time
	deferredFrame     *deferredViewerFrame
	deferredFlush     bool
	saturatedSince    time.Time
	stableSince       time.Time
	attachStarted     time.Time
	chromiumReady     time.Duration
	framesCaptured    atomic.Uint64
	framesReplaced    atomic.Uint64
	framesSent        atomic.Uint64
	bytesSent         atomic.Uint64
	inputRejected     atomic.Uint64
	// Chromium targets and browser command tabs use different identifier namespaces.
	engineTabIDs map[string]string
	lastInputSeq uint64
	inputResults map[uint64][]browserstream.Control
	inputOrder   []uint64
}

type deferredViewerFrame struct {
	jpeg       []byte
	capturedAt time.Time
	targetID   string
	width      int
	height     int
}

func NewViewerController(opts ViewerControllerOptions) *ViewerController {
	if opts.Arbiter == nil {
		opts.Arbiter = NewControlArbiter(nil)
	}
	if opts.DialCDP == nil {
		opts.DialCDP = dialWebsocketCDP
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	controller := &ViewerController{opts: opts}
	controller.epoch.Store(uint64(time.Now().UnixMicro()))
	return controller
}

func (v *ViewerController) Attached() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.active
}

func (v *ViewerController) Serve(ctx context.Context, conn *websocket.Conn) error {
	attachStarted := time.Now()
	v.mu.Lock()
	if v.active {
		v.mu.Unlock()
		return errors.New("a browser viewer is already attached")
	}
	v.active = true
	v.mu.Unlock()
	defer func() {
		v.opts.Arbiter.ReleaseUser()
		v.mu.Lock()
		v.active = false
		v.session = nil
		v.mu.Unlock()
	}()

	endpoint, err := v.opts.Chromium.EnsureRunning(ctx)
	if err != nil {
		return fmt.Errorf("start Chromium for viewer: %w", err)
	}
	cdp, err := v.opts.DialCDP(ctx, endpoint.WebSocketURL)
	if err != nil {
		return err
	}
	defer cdp.Close()

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	epoch := v.epoch.Add(1)
	state := &viewerSession{
		ctx: sessionCtx, cancel: cancel, cdp: cdp,
		control: make(chan browserstream.Control, viewerControlBuffer),
		frames:  browserstream.NewLatest(), width: defaultViewerWidth,
		height: defaultViewerHeight, epoch: epoch, started: time.Now(),
		quality: 70, fps: 15, captureWidth: maxViewerWidth, captureHeight: maxViewerHeight,
		attachStarted: attachStarted, chromiumReady: time.Since(attachStarted),
		cdpURL: endpoint.WebSocketURL,
	}
	defer state.frames.Close()
	defer v.cleanupDevTools(state)
	defer func() {
		v.opts.Logger.Info("browser viewer session closed",
			"duration_ms", time.Since(state.attachStarted).Milliseconds(),
			"chromium_ready_ms", state.chromiumReady.Milliseconds(),
			"frames_captured", state.framesCaptured.Load(),
			"frames_replaced", state.framesReplaced.Load(),
			"frames_sent", state.framesSent.Load(),
			"bytes_sent", state.bytesSent.Load(),
			"input_rejected", state.inputRejected.Load(),
		)
	}()
	v.mu.Lock()
	v.session = state
	v.mu.Unlock()

	if err := v.attachInitialTarget(state); err != nil {
		return err
	}
	conn.SetReadLimit(browserstream.MaxControlBytes)
	writeErr := make(chan error, 2)
	go func() { writeErr <- v.writeControls(conn, state) }()
	go func() { writeErr <- v.writeFrames(conn, state) }()
	go v.readCDPEvents(state)

	v.enqueue(state, browserstream.Control{
		Type: "hello", Version: browserstream.Version, StreamEpoch: epoch,
	})
	v.enqueue(state, browserstream.Control{
		Type: "attached", Version: browserstream.Version, StreamEpoch: epoch,
		TargetID: state.targetID, Width: state.width, Height: state.height, Running: true,
	})
	v.publishState(state)

	readErr := make(chan error, 1)
	go func() { readErr <- v.readViewerControls(conn, state) }()
	select {
	case err := <-readErr:
		cancel()
		return err
	case err := <-writeErr:
		cancel()
		return err
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	}
}

func (v *ViewerController) attachInitialTarget(state *viewerSession) error {
	if err := state.cdp.Call(state.ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		return err
	}
	tabs, err := v.listTargets(state)
	if err != nil {
		return err
	}
	targetID := ""
	for _, tab := range tabs {
		if tab.Active {
			targetID = tab.ID
			break
		}
	}
	if targetID == "" && len(tabs) > 0 {
		targetID = tabs[0].ID
	}
	if targetID == "" {
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := state.cdp.Call(state.ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
			return err
		}
		targetID = created.TargetID
	}
	return v.switchTarget(state, targetID)
}

func (v *ViewerController) switchTarget(state *viewerSession, targetID string) error {
	state.targetMu.Lock()
	defer state.targetMu.Unlock()
	state.opMu.Lock()
	if targetID == "" {
		state.opMu.Unlock()
		return errors.New("browser viewer target is required")
	}
	if err := v.closeDevToolsLocked(state.ctx, state); err != nil {
		state.opMu.Unlock()
		return err
	}
	if state.sessionID != "" {
		_ = state.cdp.Call(state.ctx, state.sessionID, "Page.stopScreencast", nil, nil)
		_ = state.cdp.Call(state.ctx, "", "Target.detachFromTarget", map[string]any{"sessionId": state.sessionID}, nil)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := state.cdp.Call(state.ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": targetID, "flatten": true,
	}, &attached); err != nil {
		state.opMu.Unlock()
		return err
	}
	if attached.SessionID == "" {
		state.opMu.Unlock()
		return errors.New("Chromium returned no target session")
	}
	state.targetID = targetID
	state.sessionID = attached.SessionID
	state.resetDisplayLocked()
	state.saturatedSince = time.Time{}
	state.stableSince = time.Time{}
	state.dialogOpen = false
	state.dialogType = ""
	state.dialogText = ""
	state.dialogPrompt = ""
	if err := state.cdp.Call(state.ctx, "", "Target.activateTarget", map[string]any{"targetId": targetID}, nil); err != nil {
		state.opMu.Unlock()
		return err
	}
	if err := state.cdp.Call(state.ctx, state.sessionID, "Page.enable", nil, nil); err != nil {
		state.opMu.Unlock()
		return err
	}
	if err := v.applyViewportLocked(state); err != nil {
		state.opMu.Unlock()
		return err
	}
	newSessionID := state.sessionID
	state.opMu.Unlock()

	// State is queued before screencast starts so a new-target frame is never
	// presented under the previous tab metadata.
	v.publishState(state)
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if state.sessionID != newSessionID || state.targetID != targetID {
		return nil
	}
	return v.startScreencastLocked(state)
}

func (v *ViewerController) startScreencastLocked(state *viewerSession) error {
	return state.cdp.Call(state.ctx, state.displaySessionLocked(), "Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": state.quality,
		"maxWidth":  min(state.width, state.captureWidth),
		"maxHeight": min(state.height, state.captureHeight), "everyNthFrame": 1,
	}, nil)
}

func (v *ViewerController) applyViewportLocked(state *viewerSession) error {
	return state.cdp.Call(state.ctx, state.displaySessionLocked(), "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": state.width, "height": state.height, "deviceScaleFactor": 1,
		"mobile": false,
	}, nil)
}

func (v *ViewerController) readCDPEvents(state *viewerSession) {
	defer state.cancel()
	for event := range state.cdp.Events() {
		if state.ctx.Err() != nil {
			return
		}
		state.opMu.Lock()
		pageEvent := event.SessionID == "" || event.SessionID == state.sessionID
		state.opMu.Unlock()
		if !pageEvent && event.Method != "Page.screencastFrame" && strings.HasPrefix(event.Method, "Page.") {
			continue
		}
		switch event.Method {
		case "Page.screencastFrame":
			v.acceptScreencastFrame(state, event)
		case "Target.targetCreated":
			v.handleTargetCreated(state, event)
		case "Target.targetDestroyed":
			v.handleTargetDestroyed(state, event)
		case "Target.targetInfoChanged":
			v.publishState(state)
		case "Page.frameStartedLoading":
			state.opMu.Lock()
			state.loading = true
			state.opMu.Unlock()
			v.publishState(state)
		case "Page.loadEventFired", "Page.frameStoppedLoading":
			state.opMu.Lock()
			state.loading = false
			state.opMu.Unlock()
			v.publishState(state)
		case "Page.javascriptDialogOpening":
			var dialog struct {
				Type          string `json:"type"`
				Message       string `json:"message"`
				DefaultPrompt string `json:"defaultPrompt"`
			}
			if json.Unmarshal(event.Params, &dialog) == nil {
				state.opMu.Lock()
				state.dialogOpen = true
				state.dialogType = dialog.Type
				state.dialogText = dialog.Message
				state.dialogPrompt = dialog.DefaultPrompt
				state.opMu.Unlock()
				v.publishState(state)
			}
		case "Page.javascriptDialogClosed":
			state.opMu.Lock()
			state.dialogOpen = false
			state.dialogType = ""
			state.dialogText = ""
			state.dialogPrompt = ""
			state.opMu.Unlock()
			v.publishState(state)
		case "Inspector.targetCrashed":
			v.enqueue(state, browserstream.Control{
				Type: "error", Version: browserstream.Version, StreamEpoch: state.epoch,
				Code: "BROWSER_RESTARTING", Message: "The browser is restarting.",
			})
			v.opts.Arbiter.Reset()
			state.cancel()
		}
	}
}

func (v *ViewerController) handleTargetCreated(state *viewerSession, event cdpEvent) {
	var created struct {
		TargetInfo struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			OpenerID string `json:"openerId"`
		} `json:"targetInfo"`
	}
	if json.Unmarshal(event.Params, &created) != nil {
		return
	}
	state.opMu.Lock()
	activeTarget := state.targetID
	state.opMu.Unlock()
	if created.TargetInfo.Type == "page" && created.TargetInfo.TargetID != "" && created.TargetInfo.OpenerID == activeTarget {
		if v.opts.Engine != nil && v.opts.Arbiter.Owner() == ControlUser {
			engineTabID, err := v.engineTabIDForTarget(state, created.TargetInfo.TargetID, false)
			if err == nil {
				_, err = v.opts.Engine.Execute(state.ctx, "tab-select", map[string]any{"tabId": engineTabID})
			}
			if err != nil {
				v.opts.Logger.Debug("sync browser engine to user popup", "error", err)
			}
		}
		if err := v.switchTarget(state, created.TargetInfo.TargetID); err != nil {
			v.opts.Logger.Debug("switch browser viewer to popup", "error", err)
		}
		return
	}
	v.publishState(state)
}

func (v *ViewerController) handleTargetDestroyed(state *viewerSession, event cdpEvent) {
	var destroyed struct {
		TargetID string `json:"targetId"`
	}
	if json.Unmarshal(event.Params, &destroyed) != nil {
		return
	}
	state.opMu.Lock()
	wasActive := destroyed.TargetID != "" && destroyed.TargetID == state.targetID
	wasInspector := state.devtools != nil && destroyed.TargetID == state.devtools.targetID
	state.opMu.Unlock()
	if wasInspector {
		if err := v.devtoolsOperation(state, "close"); err != nil {
			v.opts.Logger.Debug("recover viewer after inspector close", "error", err)
		}
		return
	}
	if !wasActive {
		v.publishState(state)
		return
	}
	tabs, err := v.listTargets(state)
	if err != nil {
		return
	}
	if len(tabs) == 0 {
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := state.cdp.Call(state.ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
			return
		}
		tabs = append(tabs, browserstream.Tab{ID: created.TargetID})
	}
	if err := v.switchTarget(state, tabs[0].ID); err != nil {
		v.opts.Logger.Debug("recover browser viewer after tab close", "error", err)
	}
}

func (v *ViewerController) acceptScreencastFrame(state *viewerSession, event cdpEvent) {
	state.opMu.Lock()
	activeSession := state.displaySessionLocked()
	state.opMu.Unlock()
	if event.SessionID != "" && event.SessionID != activeSession {
		return
	}
	var params struct {
		Data      string `json:"data"`
		SessionID int    `json:"sessionId"`
		Metadata  struct {
			Width     float64 `json:"deviceWidth"`
			Height    float64 `json:"deviceHeight"`
			Timestamp float64 `json:"timestamp"`
		} `json:"metadata"`
	}
	if json.Unmarshal(event.Params, &params) != nil || params.Data == "" || params.SessionID <= 0 {
		return
	}
	// Ack before any relay work. The latest-frame slot bounds memory if every
	// downstream viewer is slower than Chromium.
	if err := state.cdp.Call(state.ctx, activeSession, "Page.screencastFrameAck", map[string]any{
		"sessionId": params.SessionID,
	}, nil); err != nil {
		return
	}
	jpeg, err := base64.StdEncoding.DecodeString(params.Data)
	if err != nil || len(jpeg) == 0 {
		return
	}
	if len(jpeg) > browserstream.MaxFrameBytes {
		v.adaptViewer(state, true, time.Now())
		return
	}
	now := time.Now()
	state.opMu.Lock()
	if state.displaySessionLocked() != activeSession || (!state.viewportChangedAt.IsZero() &&
		(params.Metadata.Width != float64(state.width) || params.Metadata.Height != float64(state.height) ||
			params.Metadata.Timestamp < float64(state.viewportChangedAt.UnixMicro())/1e6)) {
		state.opMu.Unlock()
		return
	}
	pending := &deferredViewerFrame{
		jpeg: append([]byte(nil), jpeg...), capturedAt: now,
		targetID: state.displayTargetLocked(), width: state.width, height: state.height,
	}
	if state.deferredFlush {
		state.deferredFrame = pending
		state.opMu.Unlock()
		return
	}
	if !state.lastFrame.IsZero() {
		remaining := time.Second/time.Duration(state.fps) - now.Sub(state.lastFrame)
		if remaining > 0 {
			state.deferredFrame = pending
			state.deferredFlush = true
			state.opMu.Unlock()
			time.AfterFunc(remaining, func() { v.flushDeferredFrame(state) })
			return
		}
	}
	state.lastFrame = now
	state.sequence++
	frame := browserstream.Frame{
		StreamEpoch: state.epoch, Sequence: state.sequence,
		Width: uint16(state.width), Height: uint16(state.height),
		CapturedMS: uint64(time.Since(state.started).Milliseconds()), TargetID: state.displayTargetLocked(), JPEG: jpeg,
	}
	quality, fps := state.quality, state.fps
	state.opMu.Unlock()
	v.publishViewerFrame(state, frame, quality, fps, now)
}

func (v *ViewerController) flushDeferredFrame(state *viewerSession) {
	now := time.Now()
	state.opMu.Lock()
	pending := state.deferredFrame
	state.deferredFrame = nil
	state.deferredFlush = false
	if pending == nil || state.ctx.Err() != nil || pending.targetID != state.displayTargetLocked() {
		state.opMu.Unlock()
		return
	}
	state.lastFrame = now
	state.sequence++
	frame := browserstream.Frame{
		StreamEpoch: state.epoch, Sequence: state.sequence,
		Width: uint16(pending.width), Height: uint16(pending.height),
		CapturedMS: uint64(pending.capturedAt.Sub(state.started).Milliseconds()),
		TargetID:   pending.targetID, JPEG: pending.jpeg,
	}
	quality, fps := state.quality, state.fps
	state.opMu.Unlock()
	v.publishViewerFrame(state, frame, quality, fps, now)
}

func (v *ViewerController) publishViewerFrame(
	state *viewerSession,
	frame browserstream.Frame,
	quality int,
	fps int,
	now time.Time,
) {
	encoded, err := browserstream.EncodeFrame(frame)
	if err == nil {
		replaced := state.frames.Put(encoded)
		captured := state.framesCaptured.Add(1)
		if replaced {
			state.framesReplaced.Add(1)
		}
		if captured == 1 {
			v.opts.Logger.Info("browser viewer first frame",
				"attach_to_frame_ms", time.Since(state.attachStarted).Milliseconds(),
				"chromium_ready_ms", state.chromiumReady.Milliseconds(),
				"frame_bytes", len(frame.JPEG), "width", frame.Width, "height", frame.Height,
				"quality", quality, "fps", fps,
			)
		}
		v.adaptViewer(state, replaced, now)
	}
}

func (v *ViewerController) adaptViewer(state *viewerSession, saturated bool, now time.Time) {
	state.opMu.Lock()
	changed := false
	if saturated {
		state.stableSince = time.Time{}
		if state.saturatedSince.IsZero() {
			state.saturatedSince = now
		}
		if now.Sub(state.saturatedSince) >= 3*time.Second {
			switch {
			case state.fps > 10:
				state.fps = 10
				changed = true
			case state.fps > 6:
				state.fps = 6
				changed = true
			case state.quality > 55:
				state.quality = 55
				changed = true
			case state.captureWidth > 1280 || state.captureHeight > 720:
				state.captureWidth, state.captureHeight = 1280, 720
				changed = true
			}
			state.saturatedSince = now
		}
	} else {
		state.saturatedSince = time.Time{}
		if state.stableSince.IsZero() {
			state.stableSince = now
		}
		if now.Sub(state.stableSince) >= 10*time.Second {
			switch {
			case state.captureWidth < maxViewerWidth || state.captureHeight < maxViewerHeight:
				state.captureWidth, state.captureHeight = maxViewerWidth, maxViewerHeight
				changed = true
			case state.quality < 70:
				state.quality = 70
				changed = true
			case state.fps < 10:
				state.fps = 10
				changed = true
			case state.fps < 15:
				state.fps = 15
				changed = true
			}
			state.stableSince = now
		}
	}
	if !changed || state.sessionID == "" {
		state.opMu.Unlock()
		return
	}
	_ = state.cdp.Call(state.ctx, state.displaySessionLocked(), "Page.stopScreencast", nil, nil)
	_ = v.startScreencastLocked(state)
	v.opts.Logger.Info("browser viewer adapted",
		"quality", state.quality, "fps", state.fps,
		"capture_width", state.captureWidth, "capture_height", state.captureHeight,
	)
	state.opMu.Unlock()
	v.publishState(state)
}

func (v *ViewerController) readViewerControls(conn *websocket.Conn, state *viewerSession) error {
	for {
		kind, payload, err := conn.Read(state.ctx)
		if err != nil {
			return err
		}
		if kind != websocket.MessageText || len(payload) == 0 || len(payload) > browserstream.MaxControlBytes {
			return errors.New("invalid browser viewer control frame")
		}
		var control browserstream.Control
		if json.Unmarshal(payload, &control) != nil || control.Version != browserstream.Version {
			return errors.New("invalid browser viewer control message")
		}
		v.handleControl(state, control)
	}
}

func (v *ViewerController) handleControl(state *viewerSession, control browserstream.Control) {
	if control.Type == "ping" {
		v.enqueue(state, browserstream.Control{Type: "pong", Version: browserstream.Version, StreamEpoch: state.epoch})
		return
	}
	if control.Type == "detach" {
		state.cancel()
		return
	}
	reject := func(code string) {
		state.inputRejected.Add(1)
		v.enqueue(state, browserstream.Control{
			Type: "input_rejected", Version: browserstream.Version, StreamEpoch: state.epoch,
			InputSeq: control.InputSeq, Code: code, Owner: string(v.opts.Arbiter.Owner()),
		})
	}
	if control.StreamEpoch != state.epoch {
		reject("BROWSER_STALE_EPOCH")
		return
	}
	if control.InputSeq == 0 || control.InputSeq > (1<<53)-1 {
		reject("BROWSER_INVALID_INPUT_SEQUENCE")
		return
	}
	if result, ok := state.inputResults[control.InputSeq]; ok {
		for _, response := range result {
			v.enqueue(state, response)
		}
		return
	}
	if control.InputSeq <= state.lastInputSeq {
		reject("BROWSER_STALE_INPUT_SEQUENCE")
		return
	}
	state.lastInputSeq = control.InputSeq
	var responses []browserstream.Control
	respond := func(response browserstream.Control) {
		responses = append(responses, response)
		v.enqueue(state, response)
	}
	defer func() {
		if state.inputResults == nil {
			state.inputResults = make(map[uint64][]browserstream.Control)
		}
		if len(state.inputOrder) == viewerInputHistory {
			delete(state.inputResults, state.inputOrder[0])
			state.inputOrder = state.inputOrder[1:]
		}
		state.inputOrder = append(state.inputOrder, control.InputSeq)
		state.inputResults[control.InputSeq] = responses
	}()
	if !allowViewerInput(state, time.Now()) {
		respond(browserstream.Control{
			Type: "input_rejected", Version: browserstream.Version, StreamEpoch: state.epoch,
			InputSeq: control.InputSeq, Code: "BROWSER_INPUT_RATE_EXCEEDED",
		})
		return
	}
	active := control.Type != "input" || control.Kind != "pointerMove" || control.Buttons != 0
	if !v.opts.Arbiter.TryUser(active) {
		respond(browserstream.Control{
			Type: "input_rejected", Version: browserstream.Version, StreamEpoch: state.epoch,
			InputSeq: control.InputSeq, Code: "BROWSER_AGENT_CONTROL_ACTIVE", Owner: string(ControlAgent),
		})
		return
	}
	minFrame := uint64(0)
	if control.InputSeq > 0 {
		state.opMu.Lock()
		minFrame = state.sequence + 1
		state.opMu.Unlock()
	}
	var err error
	switch control.Type {
	case "viewport":
		err = v.resize(state, control.Width, control.Height)
		if err == nil {
			respond(browserstream.Control{
				Type: "viewport_ack", Version: browserstream.Version, StreamEpoch: state.epoch,
				Width: control.Width, Height: control.Height, InputSeq: control.InputSeq, MinFrameSeq: minFrame,
			})
		}
	case "input":
		err = v.dispatchInput(state, control)
	case "navigate":
		err = v.navigate(state, control)
	case "tab":
		err = v.tabOperation(state, control)
	case "dialog":
		err = v.dialogOperation(state, control)
	case "devtools":
		err = v.devtoolsOperation(state, control.Operation)
	default:
		err = errors.New("unsupported browser viewer control type")
	}
	if err != nil {
		state.inputRejected.Add(1)
		code, message := "BROWSER_INPUT_REJECTED", "Browser input could not be applied."
		var commandErr *CommandError
		if errors.As(err, &commandErr) {
			code, message = commandErr.Code, commandErr.Message
		}
		respond(browserstream.Control{
			Type: "input_rejected", Version: browserstream.Version, StreamEpoch: state.epoch,
			InputSeq: control.InputSeq, Code: code, Message: message,
		})
		v.opts.Logger.Debug("browser viewer input rejected", "error", err, "type", control.Type, "kind", control.Kind)
		return
	}
	if control.InputSeq > 0 {
		respond(browserstream.Control{
			Type: "input_ack", Version: browserstream.Version, StreamEpoch: state.epoch,
			InputSeq: control.InputSeq, MinFrameSeq: minFrame, Accepted: true,
		})
	}
	v.enqueue(state, browserstream.Control{
		Type: "control_owner", Version: browserstream.Version,
		StreamEpoch: state.epoch, Owner: string(v.opts.Arbiter.Owner()),
	})
	go func(epoch uint64) {
		timer := time.NewTimer(defaultUserControlLease + 25*time.Millisecond)
		defer timer.Stop()
		select {
		case <-state.ctx.Done():
		case <-timer.C:
			v.enqueue(state, browserstream.Control{
				Type: "control_owner", Version: browserstream.Version,
				StreamEpoch: epoch, Owner: string(v.opts.Arbiter.Owner()),
			})
		}
	}(state.epoch)
}

func (v *ViewerController) resize(state *viewerSession, width, height int) error {
	if width < minViewerWidth || width > maxViewerWidth || height < minViewerHeight || height > maxViewerHeight {
		return errors.New("browser viewport is outside the allowed range")
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	state.viewportChangedAt = time.Now()
	state.deferredFrame = nil
	state.width, state.height = width, height
	if err := v.applyViewportLocked(state); err != nil {
		return err
	}
	if err := state.cdp.Call(state.ctx, state.displaySessionLocked(), "Page.stopScreencast", nil, nil); err != nil {
		return err
	}
	return v.startScreencastLocked(state)
}

func (v *ViewerController) dispatchInput(state *viewerSession, input browserstream.Control) error {
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if (input.TargetID != "" && input.TargetID != state.displayTargetLocked()) || (state.devtools != nil && input.TargetID == "") {
		return errors.New("browser input targets a stale surface")
	}
	params := map[string]any{"modifiers": input.Modifiers}
	method := ""
	switch input.Kind {
	case "pointerMove", "pointerDown", "pointerUp", "doubleClick":
		if input.X < 0 || input.Y < 0 || input.X > float64(state.width) || input.Y > float64(state.height) {
			return errors.New("pointer coordinates are outside the viewport")
		}
		method = "Input.dispatchMouseEvent"
		params["type"] = map[string]string{
			"pointerMove": "mouseMoved", "pointerDown": "mousePressed",
			"pointerUp": "mouseReleased", "doubleClick": "mousePressed",
		}[input.Kind]
		params["x"], params["y"] = input.X, input.Y
		params["button"] = normalizeMouseButton(input.Button)
		params["buttons"] = input.Buttons
		if input.Kind == "doubleClick" {
			params["clickCount"] = 2
		} else if input.ClickCount > 0 {
			params["clickCount"] = input.ClickCount
		}
	case "wheel":
		method = "Input.dispatchMouseEvent"
		params["type"], params["x"], params["y"] = "mouseWheel", input.X, input.Y
		params["deltaX"], params["deltaY"] = finite(input.DeltaX), finite(input.DeltaY)
	case "keyDown", "keyUp":
		if len(input.Key) > 128 || len(input.CodeValue) > 128 || len(input.Text) > 8<<10 {
			return errors.New("keyboard input exceeds its limit")
		}
		method = "Input.dispatchKeyEvent"
		params["type"], params["key"], params["code"] = input.Kind, input.Key, input.CodeValue
		params["windowsVirtualKeyCode"] = virtualKeyCode(input.Key)
		if input.Text != "" {
			params["text"] = input.Text
		} else if input.Kind == "keyDown" {
			params["type"] = "rawKeyDown"
		}
	case "text", "compositionCommit":
		if len(input.Text) > 8<<10 {
			return errors.New("text input exceeds its limit")
		}
		method = "Input.insertText"
		params = map[string]any{"text": input.Text}
	case "compositionStart", "compositionUpdate", "compositionCancel":
		if len(input.Text) > 8<<10 {
			return errors.New("composition input exceeds its limit")
		}
		method = "Input.imeSetComposition"
		if input.Kind == "compositionCancel" {
			input.Text = ""
		}
		params = map[string]any{"text": input.Text, "selectionStart": len([]rune(input.Text)), "selectionEnd": len([]rune(input.Text))}
	default:
		return errors.New("unsupported browser input kind")
	}
	if input.Kind != "doubleClick" {
		return state.cdp.Call(state.ctx, state.displaySessionLocked(), method, params, nil)
	}
	if err := state.cdp.Call(state.ctx, state.displaySessionLocked(), method, params, nil); err != nil {
		return err
	}
	params["type"] = "mouseReleased"
	return state.cdp.Call(state.ctx, state.displaySessionLocked(), method, params, nil)
}

func virtualKeyCode(key string) int {
	if len(key) == 1 {
		value := key[0]
		if value >= 'a' && value <= 'z' {
			value -= 'a' - 'A'
		}
		if value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == ' ' {
			return int(value)
		}
	}
	return map[string]int{
		"Backspace": 8, "Tab": 9, "Enter": 13, "Shift": 16, "Control": 17, "Alt": 18,
		"Escape": 27, "PageUp": 33, "PageDown": 34, "End": 35, "Home": 36,
		"ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40,
		"Insert": 45, "Delete": 46, "Meta": 91,
	}[key]
}

func (v *ViewerController) navigate(state *viewerSession, control browserstream.Control) error {
	state.opMu.Lock()
	defer state.opMu.Unlock()
	switch control.Operation {
	case "open":
		normalized, err := NormalizeAgentBrowserURL(control.URL)
		if err != nil {
			return err
		}
		return state.cdp.Call(state.ctx, state.sessionID, "Page.navigate", map[string]any{"url": normalized}, nil)
	case "reload":
		return state.cdp.Call(state.ctx, state.sessionID, "Page.reload", nil, nil)
	case "back", "forward":
		var history struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				ID int `json:"id"`
			} `json:"entries"`
		}
		if err := state.cdp.Call(state.ctx, state.sessionID, "Page.getNavigationHistory", nil, &history); err != nil {
			return err
		}
		index := history.CurrentIndex - 1
		if control.Operation == "forward" {
			index = history.CurrentIndex + 1
		}
		if index < 0 || index >= len(history.Entries) {
			return errors.New("browser navigation history has no matching entry")
		}
		return state.cdp.Call(state.ctx, state.sessionID, "Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[index].ID}, nil)
	default:
		return errors.New("unsupported browser navigation operation")
	}
}

func (v *ViewerController) tabOperation(state *viewerSession, control browserstream.Control) error {
	switch control.Operation {
	case "select":
		if control.TabID == "" {
			return errors.New("tab id is required")
		}
		if v.opts.Engine != nil {
			engineTabID, err := v.engineTabIDForTarget(state, control.TabID, true)
			if err != nil {
				return err
			}
			if _, err := v.opts.Engine.Execute(state.ctx, "tab-select", map[string]any{"tabId": engineTabID}); err != nil {
				return err
			}
		} else if err := state.cdp.Call(state.ctx, "", "Target.activateTarget", map[string]any{"targetId": control.TabID}, nil); err != nil {
			return err
		}
		if err := v.switchTarget(state, control.TabID); err != nil {
			return err
		}
	case "new":
		targetURL := "about:blank"
		if strings.TrimSpace(control.URL) != "" {
			normalized, err := NormalizeAgentBrowserURL(control.URL)
			if err != nil {
				return err
			}
			targetURL = normalized
		}
		targetID := ""
		if v.opts.Engine != nil {
			before, err := v.listTargets(state)
			if err != nil {
				return err
			}
			args := map[string]any{}
			if targetURL != "about:blank" {
				args["url"] = targetURL
			}
			result, err := v.opts.Engine.Execute(state.ctx, "tab-new", args)
			if err != nil {
				return err
			}
			engineTabID := stringField(result["tabId"])
			if engineTabID == "" {
				engineTabID, err = v.engineActiveTabID(state.ctx)
				if err != nil {
					return err
				}
			}
			after, err := v.listTargets(state)
			if err != nil {
				return err
			}
			targetID = createdTargetID(before, after, targetURL)
			if targetID != "" && engineTabID != "" {
				v.rememberEngineTabID(state, targetID, engineTabID)
			}
			if targetID == "" && engineTabID != "" {
				targetID, err = v.targetIDForEngineTab(state, engineTabID)
				if err != nil {
					return err
				}
			}
		} else {
			var created struct {
				TargetID string `json:"targetId"`
			}
			if err := state.cdp.Call(state.ctx, "", "Target.createTarget", map[string]any{"url": targetURL}, &created); err != nil {
				return err
			}
			targetID = created.TargetID
		}
		if targetID == "" {
			return errors.New("browser engine did not report an active tab")
		}
		if err := v.switchTarget(state, targetID); err != nil {
			return err
		}
	case "close":
		targetID := control.TabID
		if targetID == "" {
			targetID = state.targetID
		}
		if v.opts.Engine != nil {
			engineTabID, err := v.engineTabIDForTarget(state, targetID, true)
			if err != nil {
				return err
			}
			if _, err := v.opts.Engine.Execute(state.ctx, "tab-close", map[string]any{"tabId": engineTabID}); err != nil {
				return err
			}
			v.forgetEngineTarget(state, targetID)
			activeID, err := v.engineActiveTabID(state.ctx)
			if err != nil {
				return err
			}
			if activeID != "" {
				activeTargetID, err := v.targetIDForEngineTab(state, activeID)
				if err != nil {
					return err
				}
				if err := v.switchTarget(state, activeTargetID); err != nil {
					return err
				}
			}
		} else {
			if err := state.cdp.Call(state.ctx, "", "Target.closeTarget", map[string]any{"targetId": targetID}, nil); err != nil {
				return err
			}
			tabs, err := v.listTargets(state)
			if err != nil {
				return err
			}
			if len(tabs) > 0 {
				if err := v.switchTarget(state, tabs[0].ID); err != nil {
					return err
				}
			}
		}
	default:
		return errors.New("unsupported browser tab operation")
	}
	v.publishState(state)
	return nil
}

type engineTabState struct {
	id     string
	url    string
	title  string
	active bool
}

func (v *ViewerController) engineTabs(ctx context.Context) ([]engineTabState, error) {
	data, err := v.opts.Engine.Execute(ctx, "tabs", nil)
	if err != nil {
		return nil, err
	}
	rawTabs, _ := data["tabs"].([]any)
	tabs := make([]engineTabState, 0, len(rawTabs))
	for _, raw := range rawTabs {
		tab, _ := raw.(map[string]any)
		id := stringField(tab["tabId"])
		if id == "" {
			continue
		}
		active, _ := tab["active"].(bool)
		tabs = append(tabs, engineTabState{
			id: id, url: SanitizeBrowserURL(stringField(tab["url"])),
			title: SanitizeBrowserTitle(stringField(tab["title"])), active: active,
		})
	}
	return tabs, nil
}

func (v *ViewerController) engineTabIDForTarget(state *viewerSession, targetID string, alignActive bool) (string, error) {
	engineTabs, err := v.engineTabs(state.ctx)
	if err != nil {
		return "", err
	}
	targets, err := v.listTargets(state)
	if err != nil {
		return "", err
	}
	v.reconcileEngineTabIDs(state, engineTabs, targets, alignActive)
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if engineTabID := state.engineTabIDs[targetID]; engineTabID != "" {
		return engineTabID, nil
	}
	return "", errors.New("browser engine tab could not be matched to the viewer target")
}

func (v *ViewerController) targetIDForEngineTab(state *viewerSession, engineTabID string) (string, error) {
	engineTabs, err := v.engineTabs(state.ctx)
	if err != nil {
		return "", err
	}
	targets, err := v.listTargets(state)
	if err != nil {
		return "", err
	}
	v.reconcileEngineTabIDs(state, engineTabs, targets, false)
	state.opMu.Lock()
	defer state.opMu.Unlock()
	for targetID, candidate := range state.engineTabIDs {
		if candidate == engineTabID {
			return targetID, nil
		}
	}
	return "", errors.New("browser viewer target could not be matched to the engine tab")
}

func (v *ViewerController) reconcileEngineTabIDs(state *viewerSession, engineTabs []engineTabState, targets []browserstream.Tab, alignActive bool) {
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if state.engineTabIDs == nil {
		state.engineTabIDs = map[string]string{}
	}
	targetExists := make(map[string]bool, len(targets))
	engineExists := make(map[string]bool, len(engineTabs))
	for _, target := range targets {
		targetExists[target.ID] = true
	}
	for _, tab := range engineTabs {
		engineExists[tab.id] = true
	}
	for targetID, engineTabID := range state.engineTabIDs {
		if !targetExists[targetID] || !engineExists[engineTabID] {
			delete(state.engineTabIDs, targetID)
		}
	}
	bind := func(targetID, engineTabID string) {
		if targetID == "" || engineTabID == "" {
			return
		}
		for existingTarget, existingEngineTab := range state.engineTabIDs {
			if existingTarget == targetID || existingEngineTab == engineTabID {
				delete(state.engineTabIDs, existingTarget)
			}
		}
		state.engineTabIDs[targetID] = engineTabID
	}
	if alignActive && targetExists[state.targetID] {
		for _, tab := range engineTabs {
			if tab.active {
				bind(state.targetID, tab.id)
				break
			}
		}
	}
	usedEngineTabs := map[string]bool{}
	for _, engineTabID := range state.engineTabIDs {
		usedEngineTabs[engineTabID] = true
	}
	for _, target := range targets {
		if state.engineTabIDs[target.ID] != "" {
			continue
		}
		matches := make([]string, 0, 1)
		for _, tab := range engineTabs {
			if usedEngineTabs[tab.id] || tab.url != target.URL || tab.title != target.Title {
				continue
			}
			matches = append(matches, tab.id)
		}
		if len(matches) == 1 {
			bind(target.ID, matches[0])
			usedEngineTabs[matches[0]] = true
		}
	}
	unmappedTargets := make([]string, 0, len(targets))
	unmappedEngineTabs := make([]string, 0, len(engineTabs))
	for _, target := range targets {
		if state.engineTabIDs[target.ID] == "" {
			unmappedTargets = append(unmappedTargets, target.ID)
		}
	}
	for _, tab := range engineTabs {
		if !usedEngineTabs[tab.id] {
			unmappedEngineTabs = append(unmappedEngineTabs, tab.id)
		}
	}
	if len(unmappedTargets) == len(unmappedEngineTabs) {
		for index := range unmappedTargets {
			bind(unmappedTargets[index], unmappedEngineTabs[index])
		}
	}
}

func (v *ViewerController) rememberEngineTabID(state *viewerSession, targetID, engineTabID string) {
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if state.engineTabIDs == nil {
		state.engineTabIDs = map[string]string{}
	}
	for existingTarget, existingEngineTab := range state.engineTabIDs {
		if existingTarget == targetID || existingEngineTab == engineTabID {
			delete(state.engineTabIDs, existingTarget)
		}
	}
	state.engineTabIDs[targetID] = engineTabID
}

func (v *ViewerController) forgetEngineTarget(state *viewerSession, targetID string) {
	state.opMu.Lock()
	defer state.opMu.Unlock()
	delete(state.engineTabIDs, targetID)
}

func createdTargetID(before, after []browserstream.Tab, targetURL string) string {
	existing := make(map[string]bool, len(before))
	for _, tab := range before {
		existing[tab.ID] = true
	}
	candidates := make([]browserstream.Tab, 0, 1)
	for _, tab := range after {
		if !existing[tab.ID] {
			candidates = append(candidates, tab)
		}
	}
	if len(candidates) == 1 {
		return candidates[0].ID
	}
	for _, tab := range candidates {
		if tab.URL == targetURL {
			return tab.ID
		}
	}
	return ""
}

func (v *ViewerController) engineActiveTabID(ctx context.Context) (string, error) {
	tabs, err := v.engineTabs(ctx)
	if err != nil {
		return "", err
	}
	for _, tab := range tabs {
		if tab.active {
			return tab.id, nil
		}
	}
	return "", nil
}

func (v *ViewerController) dialogOperation(state *viewerSession, control browserstream.Control) error {
	accept := false
	switch control.Operation {
	case "accept":
		accept = true
	case "dismiss":
	default:
		return errors.New("unsupported browser dialog operation")
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	params := map[string]any{"accept": accept}
	if accept && control.Text != "" {
		if len(control.Text) > 8<<10 {
			return errors.New("dialog text exceeds its limit")
		}
		params["promptText"] = control.Text
	}
	return state.cdp.Call(state.ctx, state.sessionID, "Page.handleJavaScriptDialog", params, nil)
}

func (v *ViewerController) listTargets(state *viewerSession) ([]browserstream.Tab, error) {
	state.opMu.Lock()
	activeTarget := state.targetID
	state.opMu.Unlock()
	var response struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := state.cdp.Call(state.ctx, "", "Target.getTargets", nil, &response); err != nil {
		return nil, err
	}
	tabs := make([]browserstream.Tab, 0, len(response.TargetInfos))
	for _, target := range response.TargetInfos {
		if target.Type != "page" || target.TargetID == "" || strings.HasPrefix(target.URL, "devtools:") {
			continue
		}
		tabs = append(tabs, browserstream.Tab{
			ID: target.TargetID, URL: SanitizeBrowserURL(target.URL),
			Title: SanitizeBrowserTitle(target.Title), Active: target.TargetID == activeTarget,
		})
	}
	return tabs, nil
}

func (v *ViewerController) publishState(state *viewerSession) {
	tabs, err := v.listTargets(state)
	if err != nil {
		return
	}
	state.opMu.Lock()
	active := state.targetID
	displayTarget := state.displayTargetLocked()
	devtoolsOpen := state.devtools != nil
	sessionID := state.sessionID
	width, height := state.width, state.height
	loading := state.loading
	quality, fps := state.quality, state.fps
	dialogOpen := state.dialogOpen
	dialogType, dialogText, dialogPrompt := state.dialogType, state.dialogText, state.dialogPrompt
	state.opMu.Unlock()
	currentURL, currentTitle := "", ""
	for index := range tabs {
		tabs[index].Active = tabs[index].ID == active
		if tabs[index].Active {
			currentURL, currentTitle = tabs[index].URL, tabs[index].Title
		}
	}
	canGoBack, canGoForward := false, false
	var history struct {
		CurrentIndex int        `json:"currentIndex"`
		Entries      []struct{} `json:"entries"`
	}
	if sessionID != "" && state.cdp.Call(state.ctx, sessionID, "Page.getNavigationHistory", nil, &history) == nil {
		canGoBack = history.CurrentIndex > 0
		canGoForward = history.CurrentIndex >= 0 && history.CurrentIndex+1 < len(history.Entries)
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if state.targetID != active || state.sessionID != sessionID || state.displayTargetLocked() != displayTarget {
		return
	}
	v.enqueue(state, browserstream.Control{
		Type: "state", Version: browserstream.Version, StreamEpoch: state.epoch,
		TargetID: displayTarget, ActiveTabID: active, URL: currentURL, Title: currentTitle,
		DevToolsOpen: devtoolsOpen, DevToolsSupported: true,
		Width: width, Height: height, Tabs: tabs, Owner: string(v.opts.Arbiter.Owner()),
		CanGoBack: canGoBack, CanGoForward: canGoForward, IsLoading: loading,
		DialogOpen: dialogOpen, DialogType: dialogType, DialogText: dialogText, DialogPrompt: dialogPrompt,
		Quality: quality, FPS: fps,
	})
}

func (v *ViewerController) writeControls(conn *websocket.Conn, state *viewerSession) error {
	for {
		select {
		case <-state.ctx.Done():
			return state.ctx.Err()
		case control := <-state.control:
			payload, err := json.Marshal(control)
			if err != nil {
				return err
			}
			state.writeMu.Lock()
			err = conn.Write(state.ctx, websocket.MessageText, payload)
			state.writeMu.Unlock()
			if err != nil {
				return err
			}
		}
	}
}

func (v *ViewerController) writeFrames(conn *websocket.Conn, state *viewerSession) error {
	for {
		frame, ok := state.frames.Next(state.ctx)
		if !ok {
			return state.ctx.Err()
		}
		state.writeMu.Lock()
		err := conn.Write(state.ctx, websocket.MessageBinary, frame)
		state.writeMu.Unlock()
		if err != nil {
			return err
		}
		state.framesSent.Add(1)
		state.bytesSent.Add(uint64(len(frame)))
	}
}

func (v *ViewerController) enqueue(state *viewerSession, control browserstream.Control) {
	select {
	case state.control <- control:
	case <-state.ctx.Done():
	default:
		state.cancel()
	}
}

func (v *ViewerController) AgentActionStarted(action string) {
	v.mu.Lock()
	state := v.session
	v.mu.Unlock()
	if state == nil {
		return
	}
	state.opMu.Lock()
	minFrame := state.sequence + 1
	state.opMu.Unlock()
	v.enqueue(state, browserstream.Control{
		Type: "agent_action", Version: browserstream.Version, StreamEpoch: state.epoch,
		Kind: action, Owner: string(ControlAgent), MinFrameSeq: minFrame, Running: true,
	})
}

func (v *ViewerController) AgentActionFinished(action string, args map[string]any, result map[string]any) {
	v.mu.Lock()
	state := v.session
	v.mu.Unlock()
	if state == nil {
		return
	}
	engineTabID := ""
	switch action {
	case "tab-select":
		engineTabID, _ = args["tabId"].(string)
	case "tab-new":
		engineTabID, _ = result["id"].(string)
	case "tab-close":
		engineTabID, _ = result["activeTabId"].(string)
	}
	targetID := ""
	if engineTabID != "" {
		var err error
		targetID, err = v.targetIDForEngineTab(state, engineTabID)
		if err != nil {
			v.opts.Logger.Debug("match browser viewer target after agent action", "error", err, "action", action)
		}
	}
	state.opMu.Lock()
	activeTarget := state.targetID
	state.opMu.Unlock()
	if targetID != "" && targetID != activeTarget {
		if err := v.switchTarget(state, targetID); err != nil {
			v.opts.Logger.Debug("sync browser viewer target after agent action", "error", err, "action", action)
		}
	}
	v.publishState(state)
	v.enqueue(state, browserstream.Control{
		Type: "agent_action", Version: browserstream.Version, StreamEpoch: state.epoch,
		Kind: action, Owner: string(v.opts.Arbiter.Owner()), Running: false,
	})
}

func normalizeMouseButton(button string) string {
	switch button {
	case "left", "middle", "right", "back", "forward":
		return button
	default:
		return "none"
	}
}

func finite(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func allowViewerInput(state *viewerSession, now time.Time) bool {
	state.rateMu.Lock()
	defer state.rateMu.Unlock()
	if state.rateStart.IsZero() || now.Sub(state.rateStart) >= time.Second {
		state.rateStart = now
		state.rateCount = 0
	}
	if state.rateCount >= 120 {
		return false
	}
	state.rateCount++
	return true
}
