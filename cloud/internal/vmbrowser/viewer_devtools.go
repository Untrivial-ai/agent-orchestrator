package vmbrowser

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const bundledDevToolsURL = "devtools://devtools/bundled/devtools_app.html?targetType=tab"

type viewerDevTools struct {
	targetID  string
	sessionID string
}

func (s *viewerSession) displaySessionLocked() string {
	if s.devtools != nil {
		return s.devtools.sessionID
	}
	return s.sessionID
}

func (s *viewerSession) displayTargetLocked() string {
	if s.devtools != nil {
		return s.devtools.targetID
	}
	return s.targetID
}

func (s *viewerSession) resetDisplayLocked() {
	s.lastFrame = time.Time{}
	s.deferredFrame = nil
	s.viewportChangedAt = time.Time{}
}

func (v *ViewerController) DevTools(open bool) (map[string]any, error) {
	v.mu.Lock()
	state := v.session
	v.mu.Unlock()
	if state == nil {
		if !open {
			return map[string]any{"open": false}, nil
		}
		return nil, &CommandError{Code: "BROWSER_VIEWER_REQUIRED", Message: "Open the session browser viewer before opening DevTools."}
	}
	operation := "close"
	if open {
		operation = "open"
	}
	if err := v.devtoolsOperation(state, operation); err != nil {
		return nil, err
	}
	return map[string]any{"open": open}, nil
}

func (v *ViewerController) devtoolsOperation(state *viewerSession, operation string) error {
	if operation != "open" && operation != "close" {
		return errors.New("unsupported DevTools operation")
	}
	state.targetMu.Lock()
	defer state.targetMu.Unlock()
	state.opMu.Lock()
	if err := state.ctx.Err(); err != nil {
		state.opMu.Unlock()
		return err
	}
	if (operation == "open") == (state.devtools != nil) {
		state.opMu.Unlock()
		v.publishState(state)
		return nil
	}
	var err error
	if operation == "open" {
		err = v.openDevToolsLocked(state)
	} else {
		err = v.closeDevToolsLocked(state.ctx, state)
	}
	state.opMu.Unlock()
	v.publishState(state)
	state.opMu.Lock()
	if err == nil {
		err = v.applyViewportLocked(state)
	}
	if err == nil {
		err = v.startScreencastLocked(state)
	}
	if err != nil && operation == "open" && state.devtools != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		closeErr := v.closeDevToolsLocked(cleanup, state)
		cancel()
		if closeErr != nil {
			v.opts.Logger.Debug("roll back browser inspector", "error", closeErr)
		}
		resumeErr := v.applyViewportLocked(state)
		if resumeErr == nil {
			resumeErr = v.startScreencastLocked(state)
		}
		if resumeErr != nil {
			v.opts.Logger.Debug("resume page after inspector failure", "error", resumeErr)
		}
	}
	state.opMu.Unlock()
	if err != nil {
		v.publishState(state)
		v.opts.Logger.Debug("change browser inspector", "error", err)
		return &CommandError{Code: "BROWSER_DEVTOOLS_UNAVAILABLE", Message: "DevTools could not start or close. Retry, or restart the session with an updated worker image."}
	}
	return nil
}

func (v *ViewerController) openDevToolsLocked(state *viewerSession) (err error) {
	ctx, cancel := context.WithTimeout(state.ctx, 8*time.Second)
	defer cancel()
	var opened struct {
		TargetID string `json:"targetId"`
	}
	defer func() {
		if err != nil {
			if closeErr := v.cleanupInspectorTarget(state, opened.TargetID); closeErr != nil {
				v.opts.Logger.Debug("clean up partial browser inspector", "error", closeErr)
			}
		}
	}()
	if err = state.cdp.Call(ctx, "", "Target.openDevTools", map[string]any{"targetId": state.targetID}, &opened); err != nil {
		return err
	}
	if opened.TargetID == "" || opened.TargetID == state.targetID {
		return errors.New("Chromium returned no inspector target")
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err = state.cdp.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": opened.TargetID, "flatten": true}, &attached); err != nil {
		return err
	}
	if attached.SessionID == "" {
		return errors.New("Chromium returned no inspector session")
	}
	if err = state.cdp.Call(ctx, attached.SessionID, "Page.enable", nil, nil); err != nil {
		return err
	}
	if err = waitDevToolsReady(ctx, state.cdp, attached.SessionID); err != nil {
		return err
	}
	// The native inspector keeps its inspected-page binding across reloads.
	// Omitting can_dock keeps the streamed surface and input coordinates aligned.
	var navigation struct {
		ErrorText string `json:"errorText"`
	}
	if err = state.cdp.Call(ctx, attached.SessionID, "Page.navigate", map[string]any{"url": bundledDevToolsURL}, &navigation); err != nil {
		return err
	}
	if navigation.ErrorText != "" {
		return fmt.Errorf("load bundled inspector: %s", navigation.ErrorText)
	}
	if err = waitDevToolsReady(ctx, state.cdp, attached.SessionID); err != nil {
		return err
	}
	if err = state.cdp.Call(ctx, state.sessionID, "Page.stopScreencast", nil, nil); err != nil {
		return err
	}
	state.devtools = &viewerDevTools{targetID: opened.TargetID, sessionID: attached.SessionID}
	state.resetDisplayLocked()
	return nil
}

func waitDevToolsReady(ctx context.Context, cdp cdpConnection, sessionID string) error {
	// Navigating before the native frontend initializes can leave its embedder
	// binding disconnected. This fixed predicate never evaluates viewer input.
	for {
		var ready struct {
			Result struct {
				Value bool `json:"value"`
			} `json:"result"`
		}
		if err := cdp.Call(ctx, sessionID, "Runtime.evaluate", map[string]any{
			"expression":    `document.readyState === "complete" && !!document.querySelector(".root-view")`,
			"returnByValue": true,
		}, &ready); err != nil {
			return err
		}
		if ready.Result.Value {
			return nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (v *ViewerController) closeDevToolsLocked(ctx context.Context, state *viewerSession) error {
	if state.devtools == nil {
		return nil
	}
	targetID := state.devtools.targetID
	if err := state.cdp.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": targetID}, nil); err != nil {
		// Closing the inspected page can destroy its inspector before this call.
		var targets struct {
			TargetInfos []struct {
				TargetID string `json:"targetId"`
			} `json:"targetInfos"`
		}
		if checkErr := state.cdp.Call(ctx, "", "Target.getTargets", nil, &targets); checkErr != nil {
			return err
		}
		for _, target := range targets.TargetInfos {
			if target.TargetID == targetID {
				return err
			}
		}
	}
	state.devtools = nil
	state.resetDisplayLocked()
	return nil
}

func (v *ViewerController) cleanupDevTools(state *viewerSession) {
	state.cancel()
	state.targetMu.Lock()
	defer state.targetMu.Unlock()
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if state.devtools == nil {
		return
	}
	if err := v.cleanupInspectorTarget(state, state.devtools.targetID); err != nil {
		v.opts.Logger.Debug("clean up browser inspector", "error", err)
		return
	}
	state.devtools = nil
}

func (v *ViewerController) cleanupInspectorTarget(state *viewerSession, targetID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	closeTarget := func(cdp cdpConnection) error {
		if targetID == "" {
			// A canceled open can have created the native target before its reply.
			var opened struct {
				TargetID string `json:"targetId"`
			}
			if err := cdp.Call(ctx, "", "Target.getDevToolsTarget", map[string]any{"targetId": state.targetID}, &opened); err != nil {
				return err
			}
			targetID = opened.TargetID
		}
		if targetID == "" || targetID == state.targetID {
			return nil
		}
		return cdp.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": targetID}, nil)
	}
	err := closeTarget(state.cdp)
	if err == nil || state.cdpURL == "" {
		return err
	}
	// Canceling an in-flight WebSocket write can close the original connection.
	// Reconnect only to the existing browser, never start one during cleanup.
	cdp, dialErr := v.opts.DialCDP(ctx, state.cdpURL)
	if dialErr != nil {
		return fmt.Errorf("reconnect inspector cleanup: %w", dialErr)
	}
	defer cdp.Close()
	return closeTarget(cdp)
}
