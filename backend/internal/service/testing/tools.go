package testing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ToolNames lists the exact native testing tools mounted by HTTP and MCP.
var ToolNames = []string{"screenshot", "observe", "click", "type", "key", "read_target_logs", "target_daemon_query", "submit_report"}

// IsTool reports whether name belongs to the native testing profile.
func IsTool(name string) bool {
	for _, n := range ToolNames {
		if n == name {
			return true
		}
	}
	return false
}
func validOutcome(out domain.TestOutcome) bool {
	switch out {
	case domain.TestOutcomeReproduced, domain.TestOutcomeNotReproduced, domain.TestOutcomeNeedsInformation, domain.TestOutcomeEnvironmentBlocked, domain.TestOutcomePartial, domain.TestOutcomeCancelled:
		return true
	}
	return false
}
func validKey(key string) bool {
	if len(key) == 1 && ((key[0] >= 'a' && key[0] <= 'z') || (key[0] >= 'A' && key[0] <= 'Z') || (key[0] >= '0' && key[0] <= '9')) {
		return true
	}
	switch key {
	case "Enter", "Tab", "Escape", "Backspace", "Delete", "Space", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown", "Control", "Shift", "Alt", "Meta":
		return true
	}
	return false
}
func decodeInput(raw json.RawMessage, name string) (any, json.RawMessage, error) {
	var input any
	var required []string
	switch name {
	case "screenshot":
		input = &domain.TestScreenshotRequest{}
	case "observe":
		input = &domain.TestObserveRequest{}
	case "click":
		input = &domain.TestClickRequest{}
		required = []string{"screenshotId"}
	case "type":
		input = &domain.TestTypeRequest{}
		required = []string{"screenshotId", "text"}
	case "key":
		input = &domain.TestKeyRequest{}
		required = []string{"screenshotId", "keys"}
	case "read_target_logs":
		input = &domain.TestReadLogsRequest{}
	case "target_daemon_query":
		input = &domain.TestDaemonQueryRequest{}
		required = []string{"resource"}
	case "submit_report":
		input = &domain.TestSubmitReportRequest{}
		required = []string{"outcome", "markdown"}
	default:
		return nil, nil, invalid("Unknown testing tool")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, nil, invalid("Tool input must be a JSON object")
	}
	for _, field := range required {
		value, ok := fields[field]
		if !ok || bytes.Equal(value, []byte("null")) {
			return nil, nil, invalid("Missing required tool input")
		}
	}
	for _, value := range fields {
		if bytes.Equal(value, []byte("null")) {
			return nil, nil, invalid("Null tool input is not allowed")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(input) != nil {
		return nil, nil, invalid("Invalid testing tool input")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, nil, invalid("Trailing tool input")
	}
	if name == "click" || name == "type" {
		_, element := fields["elementId"]
		_, x := fields["x"]
		_, y := fields["y"]
		if (element && (x || y)) || (!element && (!x || !y)) {
			return nil, nil, invalid("Supply elementId or both x and y, never both addressing paths")
		}
		if element {
			var id string
			if json.Unmarshal(fields["elementId"], &id) != nil || strings.TrimSpace(id) == "" || len(id) > 128 {
				return nil, nil, invalid("Invalid element ID")
			}
		}
	}
	switch v := input.(type) {
	case *domain.TestClickRequest:
		if v.ScreenshotID == "" || v.X < 0 || v.Y < 0 {
			return nil, nil, invalid("Screenshot and nonnegative coordinates are required")
		}
		if _, provided := fields["button"]; !provided {
			v.Button = domain.TestMouseButtonLeft
		}
		if v.Button != domain.TestMouseButtonLeft && v.Button != domain.TestMouseButtonRight && v.Button != domain.TestMouseButtonMiddle {
			return nil, nil, invalid("Invalid mouse button")
		}
	case *domain.TestTypeRequest:
		if v.ScreenshotID == "" || v.X < 0 || v.Y < 0 || !utf8.ValidString(v.Text) || utf8.RuneCountInString(v.Text) > 16384 {
			return nil, nil, invalid("Invalid type input")
		}
	case *domain.TestKeyRequest:
		if v.ScreenshotID == "" || len(v.Keys) < 1 || len(v.Keys) > 4 {
			return nil, nil, invalid("Invalid key input")
		}
		for _, key := range v.Keys {
			if !validKey(key) {
				return nil, nil, invalid("Key is not allowed")
			}
		}
	case *domain.TestReadLogsRequest:
		if _, provided := fields["maxBytes"]; !provided {
			v.MaxBytes = 65536
		}
		if v.MaxBytes < 1 || v.MaxBytes > 262144 || len(v.Cursor) > 256 {
			return nil, nil, invalid("Invalid log bounds")
		}
	case *domain.TestDaemonQueryRequest:
		switch v.Resource {
		case domain.TestDaemonProjects, domain.TestDaemonSessions:
			if v.SessionID != "" {
				return nil, nil, invalid("Session ID is only allowed for a session resource")
			}
		case domain.TestDaemonReviews, domain.TestDaemonConversation:
			if !domain.ValidTestSessionID(v.SessionID) {
				return nil, nil, invalid("Invalid target session ID")
			}
		default:
			return nil, nil, invalid("Invalid daemon resource")
		}
	case *domain.TestSubmitReportRequest:
		if !validOutcome(v.Outcome) || len(v.Markdown) > 65536 || !utf8.ValidString(v.Markdown) {
			return nil, nil, invalid("Invalid report outcome or markdown over 64 KiB")
		}
	}
	canonical, err := json.Marshal(input)
	return input, canonical, err
}

// authorize resolves the session's current durable binding on every admission.
// An explicit old attempt is fenced even though the session credential survives.
func (s *Service) authorize(ctx context.Context, id domain.TestAttemptID, session domain.SessionID, token string) (*attemptState, capability, error) {
	hash := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.caps[session]
	link, bound, err := s.deps.Store.GetTestToolBinding(ctx, session)
	if err != nil {
		return nil, capability{}, err
	}
	if !ok && token != "" && (s.closed || s.attempts[id] == nil) && bound && link.AttemptID == id {
		return nil, capability{}, WorkerNotRunning()
	}
	if !ok || token == "" || !bound || link.ProfileID != domain.TestToolProfileNativeV1 || subtle.ConstantTimeCompare(grant.hash[:], hash[:]) != 1 {
		return nil, capability{}, apierr.Forbidden("INVALID_TEST_CAPABILITY", "Testing capability is missing, revoked or does not own this session")
	}
	if link.AttemptID != id {
		return nil, capability{}, targetChanged()
	}
	st := s.attempts[id]
	if s.closed || st == nil || st.ctx.Err() != nil || grant.ctx.Err() != nil || st.record.Phase != domain.TestAttemptActive || st.record.CancelledAt != nil || !s.deps.Clock.Now().Before(st.record.Deadline) {
		return nil, capability{}, inactive()
	}
	r, found, err := s.deps.Store.GetTestAttempt(ctx, id)
	if err != nil {
		return nil, capability{}, err
	}
	if !found || r.Phase != domain.TestAttemptActive || r.CancelledAt != nil || !s.deps.Clock.Now().Before(r.Deadline) {
		return nil, capability{}, inactive()
	}
	if !sameTarget(r.Target, st.record.Target) || r.LeaseGeneration != st.record.Target.Generation {
		return nil, capability{}, targetChanged()
	}
	grant.link, grant.target = link, r.Target
	return st, grant, nil
}

// ExecuteCurrent never accepts a target supplied by the worker or MCP process.
func (s *Service) ExecuteCurrent(ctx context.Context, session domain.SessionID, token, requestID, name string, raw json.RawMessage) (ToolResult, error) {
	if err := s.configured(); err != nil {
		return ToolResult{}, err
	}
	link, found, err := s.LookupBinding(ctx, session)
	if err != nil {
		return ToolResult{}, err
	}
	if !found {
		return ToolResult{}, apierr.Forbidden("INVALID_TEST_CAPABILITY", "Session has no testing binding")
	}
	return s.Execute(ctx, link.AttemptID, session, token, requestID, name, raw)
}

// Execute validates ownership and journals a tool before dispatch.
func (s *Service) Execute(ctx context.Context, id domain.TestAttemptID, session domain.SessionID, token, requestID, name string, raw json.RawMessage) (result ToolResult, err error) {
	if err := s.configured(); err != nil {
		return result, err
	}
	if requestID == "" || len(requestID) > 128 || len(raw) > 256*1024 {
		return result, invalid("Request ID and bounded tool input are required")
	}
	input, canonical, err := decodeInput(raw, name)
	if err != nil {
		return result, err
	}
	st, grant, err := s.authorize(ctx, id, session, token)
	if err != nil {
		return result, err
	}
	if strings.Contains(string(raw), token) || strings.Contains(string(canonical), token) || strings.Contains(requestID, token) {
		return result, invalid("Capability secrets cannot be written as tool data")
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopAttempt := context.AfterFunc(st.ctx, cancel)
	defer stopAttempt()
	stopCap := context.AfterFunc(grant.ctx, cancel)
	defer stopCap()
	s.mu.Lock()
	if st.seen[requestID] {
		s.mu.Unlock()
		return result, apierr.Conflict("DUPLICATE_TEST_REQUEST", "Request ID was already admitted; inspect evidence instead of repeating input", nil)
	}
	st.seen[requestID] = true
	s.mu.Unlock()
	select {
	case <-callCtx.Done():
		return result, callCtx.Err()
	case <-st.gate:
	}
	defer func() { st.gate <- struct{}{} }()
	if _, _, err = s.authorize(callCtx, id, session, token); err != nil {
		return result, err
	}
	if err = s.deps.Target.Probe(callCtx, grant.target); err != nil {
		return result, targetChanged()
	}
	record := domain.TestActionRecord{AttemptID: id, WindowID: grant.target.WindowID, LaunchID: grant.target.LaunchID, RequestID: requestID, Tool: name, Input: canonical, State: "dispatching", At: s.deps.Clock.Now().UTC()}
	if name == "click" || name == "type" || name == "key" {
		record.ConfiguredDeliveryMode = s.deliveryMode()
		record.DeliveryMode = record.ConfiguredDeliveryMode
		if policy, ok := s.deps.Desktop.(ports.TestingDesktopPolicy); ok {
			record.DeliveryMode = policy.InputDeliveryMode(name)
		}
	}
	if err = s.deps.Evidence.AppendAction(callCtx, record); err != nil {
		return result, apierr.Internal("TEST_EVIDENCE_WRITE_FAILED", "Cannot save action journal; tool was not dispatched")
	}
	defer func() {
		journalCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		record.At = s.deps.Clock.Now().UTC()
		record.State = "completed"
		record.Detail = "Tool completed"
		if result.Action != nil {
			record.InputPath = result.Action.InputPath
			record.RequestedInputPath = result.Action.RequestedInputPath
		}
		if result.ObservationStatus == "failed" {
			record.State = "failed"
			record.Detail = "Input delivered; post-action observation failed: " + result.ObservationError
		}
		if err != nil {
			record.State = "failed"
			record.Detail = "Tool failed or was cancelled; partial action may have been delivered"
			var failure *apierr.Error
			if errors.As(err, &failure) && failure.Code == "TEST_INPUT_REFUSED" {
				record.State = "refused"
				record.Detail = "Input refused before dispatch; nothing was sent"
			}
		}
		if e := s.deps.Evidence.AppendAction(journalCtx, record); e != nil {
			err = apierr.Internal("TEST_EVIDENCE_WRITE_FAILED", "Cannot save action completion; partial action may have been delivered")
			if result.Action != nil && result.Action.Delivered {
				result.ObservationStatus, result.ObservationError = "failed", "TEST_EVIDENCE_WRITE_FAILED"
				err = nil // Keep the delivery fact visible in the HTTP/MCP result.
			}
		}
	}()
	if record.ConfiguredDeliveryMode == "foreground" && record.DeliveryMode != "foreground" {
		return result, apierr.Conflict("TEST_INPUT_REFUSED", "Testing input must use foreground; background input is unsupported", nil)
	}
	if _, _, err = s.authorize(callCtx, id, session, token); err != nil {
		return result, err
	}
	if err = s.deps.Target.Probe(callCtx, grant.target); err != nil {
		return result, targetChanged()
	}
	if err := callCtx.Err(); err != nil {
		return result, err
	}
	result, err = s.dispatch(callCtx, st, grant.target, input, requestID)
	if err != nil {
		return result, err
	}
	if callCtx.Err() != nil && name != "submit_report" && result.Action == nil {
		return result, callCtx.Err()
	}
	return result, nil
}
func (s *Service) frame(st *attemptState, target domain.TestTargetIdentity, id string, x, y *int) (domain.TestDesktopFrame, error) {
	s.mu.Lock()
	f, ok := st.frames[id]
	s.mu.Unlock()
	if !ok || !sameTarget(f.Target, target) || f.Width < 1 || f.Height < 1 || f.Bounds.Width <= 0 || f.Bounds.Height <= 0 || s.deps.Clock.Now().Sub(f.CapturedAt) > 2*time.Minute || f.CapturedAt.After(s.deps.Clock.Now()) {
		return f, invalid("Screenshot is foreign, stale or has invalid geometry; capture again")
	}
	if x != nil && (*x < 0 || *y < 0 || *x >= f.Width || *y >= f.Height) {
		return f, invalid("Coordinates are outside the screenshot")
	}
	return f, nil
}
func (s *Service) save(ctx context.Context, st *attemptState, result *ToolResult, kind, mime string, data []byte, frame *domain.TestDesktopFrame) (domain.TestEvidenceReceipt, error) {
	receipt, err := s.deps.Evidence.Write(ctx, st.record.ID, ports.TestingEvidenceArtifact{Kind: kind, MIMEType: mime, Frame: frame}, bytes.NewReader(data))
	if err != nil {
		return receipt, fmt.Errorf("%w: %w", apierr.Internal("TEST_EVIDENCE_WRITE_FAILED", "Cannot save tool evidence; partial action may have been delivered"), err)
	}
	result.Evidence = append(result.Evidence, receipt)
	return receipt, nil
}

func (s *Service) consumeFrame(st *attemptState, id string) {
	s.mu.Lock()
	delete(st.frames, id)
	s.mu.Unlock()
}

// capture saves the exact pixels and bounded AX together. Each new provider
// capture invalidates prior input receipts, even if capture or storage fails.
func (s *Service) capture(ctx context.Context, st *attemptState, target domain.TestTargetIdentity, result *ToolResult) error {
	s.mu.Lock()
	clear(st.frames)
	s.mu.Unlock()
	shot, err := s.deps.Desktop.Screenshot(ctx, target)
	if err != nil {
		return fmt.Errorf("%w: %w", apierr.Unavailable("TEST_SCREENSHOT_FAILED", "Bound target observation failed; do not repeat input"), err)
	}
	evidenceShot := &shot
	if shot.Original != nil {
		evidenceShot = shot.Original
	}
	for _, capture := range []*domain.TestScreenshot{&shot, evidenceShot} {
		if !sameTarget(capture.Frame.Target, target) || capture.Frame.Width < 1 || capture.Frame.Height < 1 || capture.Frame.Bounds.Width <= 0 || capture.Frame.Bounds.Height <= 0 || capture.MIMEType != "image/png" || len(capture.Data) == 0 {
			return targetChanged()
		}
		geometry, e := png.DecodeConfig(bytes.NewReader(capture.Data))
		if e != nil || geometry.Width != capture.Frame.Width || geometry.Height != capture.Frame.Height {
			return targetChanged()
		}
	}
	if shot.Elements == nil {
		shot.Elements = []domain.TestElement{}
	}
	ids := map[string]bool{}
	if len(shot.Elements) > 300 {
		return targetChanged()
	}
	for _, element := range shot.Elements {
		f := element.Frame
		if element.ElementID == "" || len(element.ElementID) > 128 || ids[element.ElementID] ||
			utf8.RuneCountInString(element.Role) > 64 || utf8.RuneCountInString(element.Label) > 256 || utf8.RuneCountInString(element.Value) > 256 ||
			math.IsNaN(f.X) || math.IsNaN(f.Y) || !(f.Width > 0) || !(f.Height > 0) || f.X < 0 || f.Y < 0 ||
			f.X+f.Width > float64(shot.Frame.Width) || f.Y+f.Height > float64(shot.Frame.Height) {
			return targetChanged()
		}
		ids[element.ElementID] = true
	}
	metadata := evidenceShot.Frame
	metadata.Target, metadata.CaptureHandle = domain.TestTargetIdentity{}, ""
	receipt, err := s.save(ctx, st, result, "screenshot", evidenceShot.MIMEType, evidenceShot.Data, &metadata)
	if err != nil {
		return err
	}
	shot.Frame.ScreenshotID = receipt.ID
	observation, err := json.Marshal(struct {
		Frame     domain.TestDesktopFrame `json:"frame"`
		Elements  []domain.TestElement    `json:"elements"`
		Truncated bool                    `json:"truncated"`
	}{shot.Frame, shot.Elements, shot.Truncated})
	if err != nil {
		return err
	}
	if _, err = s.save(ctx, st, result, "observation", "application/json", observation, nil); err != nil {
		return err
	}
	s.mu.Lock()
	st.frames[receipt.ID] = shot.Frame
	s.mu.Unlock()
	shot.InputReady = true
	result.Screenshot = &shot
	return nil
}

// observationDigest excludes ephemeral element IDs and capture timestamps.
// AX changes matter even when the pixels are identical.
func observationDigest(shot *domain.TestScreenshot) [32]byte {
	elements := append([]domain.TestElement(nil), shot.Elements...)
	for i := range elements {
		elements[i].ElementID = ""
	}
	metadata, _ := json.Marshal(struct {
		Bounds        domain.TestWindowBounds
		Width, Height int
		Elements      []domain.TestElement
		Truncated     bool
	}{shot.Frame.Bounds, shot.Frame.Width, shot.Frame.Height, elements, shot.Truncated})
	hash := sha256.New()
	_, _ = hash.Write(metadata)
	pixels := shot.Data
	if shot.Original != nil {
		pixels = shot.Original.Data
	}
	_, _ = hash.Write(pixels)
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func (s *Service) settle(ctx context.Context, st *attemptState, target domain.TestTargetIdentity, result *ToolResult) error {
	settleCtx, cancel := context.WithTimeout(ctx, s.deps.PostActionCaptureTimeout)
	defer cancel()
	var previous [32]byte
	havePrevious := false
	for {
		last := result.Screenshot
		if err := s.capture(settleCtx, st, target, result); err != nil {
			if havePrevious && ctx.Err() == nil && errors.Is(settleCtx.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded) {
				// A newer capture may have invalidated this saved receipt. Retain
				// its visual evidence without allowing another input with its IDs.
				retained := *last
				retained.InputReady = false
				result.Screenshot = &retained
				result.ObservationStatus, result.ObservationError = "unsettled", "TEST_SETTLE_TIMEOUT"
				return nil
			}
			return err
		}
		current := observationDigest(result.Screenshot)
		if havePrevious && current == previous {
			result.ObservationStatus = "settled"
			return nil
		}
		previous, havePrevious = current, true
		// Sampling spacing avoids calling two immediate captures a settled UI.
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-timer.C:
		case <-settleCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			result.ObservationStatus = "unsettled"
			return nil
		}
	}
}

func (s *Service) dispatch(ctx context.Context, st *attemptState, target domain.TestTargetIdentity, input any, requestID string) (result ToolResult, err error) {
	result.Evidence = []domain.TestEvidenceReceipt{}
	switch v := input.(type) {
	case *domain.TestScreenshotRequest, *domain.TestObserveRequest:
		if err := s.capture(ctx, st, target, &result); err != nil {
			return result, err
		}
		result.ObservationStatus = "captured"
	case *domain.TestClickRequest:
		var x, y *int
		if v.ElementID == "" {
			x, y = &v.X, &v.Y
		}
		f, e := s.frame(st, target, v.ScreenshotID, x, y)
		if e != nil {
			return result, e
		}
		s.consumeFrame(st, v.ScreenshotID)
		action, e := s.deps.Desktop.Click(ctx, target, f, *v)
		if e != nil {
			return result, inputFailure(e)
		}
		result.Action = &action
	case *domain.TestTypeRequest:
		var x, y *int
		if v.ElementID == "" {
			x, y = &v.X, &v.Y
		}
		f, e := s.frame(st, target, v.ScreenshotID, x, y)
		if e != nil {
			return result, e
		}
		s.consumeFrame(st, v.ScreenshotID)
		action, e := s.deps.Desktop.Type(ctx, target, f, *v)
		if e != nil {
			return result, inputFailure(e)
		}
		result.Action = &action
	case *domain.TestKeyRequest:
		f, e := s.frame(st, target, v.ScreenshotID, nil, nil)
		if e != nil {
			return result, e
		}
		s.consumeFrame(st, v.ScreenshotID)
		action, e := s.deps.Desktop.Key(ctx, target, f, *v)
		if e != nil {
			return result, inputFailure(e)
		}
		result.Action = &action
	case *domain.TestReadLogsRequest:
		logs, e := s.deps.Target.ReadLogs(ctx, target, *v)
		if e != nil {
			return result, apierr.Unavailable("TEST_LOGS_FAILED", "Target logs failed")
		}
		if len(logs.Text) > v.MaxBytes {
			return result, apierr.Internal("TEST_LOGS_OVERSIZED", "Provider returned oversized logs")
		}
		if _, e = s.save(ctx, st, &result, "logs", "text/plain", []byte(logs.Text), nil); e != nil {
			return result, e
		}
		result.Logs = &logs
	case *domain.TestDaemonQueryRequest:
		query, e := s.deps.Target.QueryDaemon(ctx, target, *v)
		if e != nil {
			return result, apierr.Unavailable("TEST_QUERY_FAILED", "Target daemon query failed")
		}
		if !json.Valid(query.Data) || query.Resource != v.Resource {
			return result, apierr.Internal("TEST_QUERY_INVALID", "Provider returned invalid daemon query")
		}
		if _, e = s.save(ctx, st, &result, "daemon_query", "application/json", query.Data, nil); e != nil {
			return result, e
		}
		result.Query = &query
	case *domain.TestSubmitReportRequest:
		receipt, e := s.save(ctx, st, &result, "report", "text/markdown", []byte(v.Markdown), nil)
		if e != nil {
			return result, e
		}
		if e := s.deps.Store.SetTestRunReport(ctx, st.record.RunID, receipt.ID); e != nil {
			return result, e
		}
		if _, e = s.finish(ctx, st.record.ID, v.Outcome, v.Outcome == domain.TestOutcomeCancelled); e != nil {
			return result, e
		}
		result.Report = &domain.TestSubmitReportResult{Outcome: v.Outcome, EvidenceID: receipt.ID}
	}
	if result.Action != nil {
		data, _ := json.Marshal(result.Action)
		if _, err := s.save(ctx, st, &result, "delivery", "application/json", data, nil); err != nil {
			result.ObservationStatus, result.ObservationError = "failed", "TEST_EVIDENCE_WRITE_FAILED"
			s.deps.Log.Warn("testing post-action evidence failed", "attemptID", st.record.ID, "requestID", requestID, "cause", workerLaunchCause(err))
		} else if !result.Action.Delivered {
			result.ObservationStatus, result.ObservationError = "failed", "TEST_INPUT_NOT_DELIVERED"
		} else if err := s.settle(ctx, st, target, &result); err != nil {
			result.Screenshot = nil
			result.ObservationStatus = "failed"
			result.ObservationError = "TEST_SCREENSHOT_FAILED"
			var failure *apierr.Error
			if errors.As(err, &failure) {
				result.ObservationError = failure.Code
			}
			s.deps.Log.Warn("testing post-action observation failed", "attemptID", st.record.ID, "requestID", requestID, "cause", workerLaunchCause(err))
		}
	}
	return result, nil
}

func inputFailure(err error) error {
	if errors.Is(err, ports.ErrTestingInputRefused) {
		return fmt.Errorf("%w: %w", apierr.Conflict("TEST_INPUT_REFUSED", "Target input refused before dispatch; nothing was sent", nil), err)
	}
	return fmt.Errorf("%w: %w", apierr.Unavailable("TEST_INPUT_FAILED", "Target input failed; delivery is unverified"), err)
}
