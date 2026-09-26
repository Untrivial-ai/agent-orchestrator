package vmbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
)

// EngineLike is the service's engine seam: satisfied by *Engine, faked in
// tests and worker wiring tests.
type EngineLike interface {
	Execute(ctx context.Context, action string, args map[string]any) (map[string]any, error)
	Screenshot(ctx context.Context) (data string, width, height int, err error)
	// Close stops the supervised browser; used by idle shutdown.
	Close(ctx context.Context) error
}

// ServiceOptions configures the loopback browser service.
type ServiceOptions struct {
	SessionID          string
	CapabilityVerifier string
	Engine             EngineLike
	Viewer             *ViewerController
	Arbiter            *ControlArbiter
	Logger             *slog.Logger
	authority          *browsercontract.Authority
}

// Service exposes the desktop browser contract on the VM loopback:
// POST /api/v1/browser/commands and GET /api/v1/browser/status.
type Service struct {
	opts                    ServiceOptions
	authority               *browsercontract.Authority
	lastActivityNanoseconds atomic.Int64
	viewerConnections       atomic.Int64
}

// NewService creates the service for exactly one session.
func NewService(opts ServiceOptions) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.authority == nil {
		opts.authority = browsercontract.NewAuthority()
	}
	return &Service{opts: opts, authority: opts.authority}
}

// Handler returns the loopback HTTP handler.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+browsercontract.RouteCommands, s.handleCommand)
	mux.HandleFunc("GET "+browsercontract.RouteStatus, s.handleStatus)
	mux.HandleFunc("GET "+ViewerStreamRoute, s.handleViewerStream)
	return mux
}

const ViewerStreamRoute = "/api/v1/browser/viewer-stream"

// Connected reports whether the backing engine has served or could serve.
func (s *Service) Connected() (bool, time.Time) {
	nanos := s.lastActivityNanoseconds.Load()
	if nanos == 0 {
		return false, time.Time{}
	}
	return true, time.Unix(0, nanos).UTC()
}

// LastActivity exposes the last served-command time for idle shutdown.
func (s *Service) LastActivity() time.Time {
	nanos := s.lastActivityNanoseconds.Load()
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos).UTC()
}

func (s *Service) ViewerAttached() bool {
	return s.viewerConnections.Load() > 0
}

func (s *Service) touchActivity() {
	s.lastActivityNanoseconds.Store(time.Now().UnixNano())
}

type commandRequest struct {
	SessionID string         `json:"sessionId"`
	Action    string         `json:"action"`
	Args      map[string]any `json:"args,omitempty"`
}

type commandResponse struct {
	RequestID string         `json:"requestId"`
	SessionID string         `json:"sessionId"`
	Action    string         `json:"action"`
	Result    map[string]any `json:"result"`
}

func (s *Service) handleCommand(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.NewString()
	var in commandRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", requestID)
		return
	}
	if strings.TrimSpace(in.SessionID) == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "SESSION_ID_REQUIRED", "sessionId is required", requestID)
		return
	}
	if !s.authorized(in.SessionID, r.Header.Get(browsercontract.CapabilityHeader)) {
		s.writeError(w, http.StatusForbidden, "forbidden", "BROWSER_CAPABILITY_INVALID", "Browser capability is invalid", requestID)
		return
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if !browsercontract.Supported(action) {
		s.writeError(w, http.StatusBadRequest, "bad_request", "BROWSER_ACTION_UNSUPPORTED", "Unsupported browser action", requestID)
		return
	}
	if s.opts.Engine == nil {
		s.writeError(w, http.StatusServiceUnavailable, "unavailable", "BROWSER_RUNTIME_UNAVAILABLE", "Browser service is not available", requestID)
		return
	}
	s.touchActivity()
	mutating := browserActionMutates(action, in.Args)
	var release func()
	if mutating && s.opts.Arbiter != nil {
		var err error
		release, err = s.opts.Arbiter.AcquireAgent(r.Context())
		if err != nil {
			if errors.Is(err, ErrUserControlActive) {
				err = &CommandError{Code: "BROWSER_USER_CONTROL_ACTIVE", Message: "The user is controlling the browser"}
			}
			s.writeCommandError(w, r, err, requestID)
			return
		}
		if s.opts.Viewer != nil {
			s.opts.Viewer.AgentActionStarted(action)
		}
	}
	result, err := s.dispatch(r.Context(), action, in.Args)
	if release != nil {
		release()
		if s.opts.Viewer != nil {
			s.opts.Viewer.AgentActionFinished(action, in.Args, result)
		}
	}
	if err != nil {
		s.writeCommandError(w, r, err, requestID)
		return
	}
	writeJSONResponse(w, http.StatusOK, commandResponse{
		RequestID: requestID,
		SessionID: in.SessionID,
		Action:    action,
		Result:    result,
	})
}

func (s *Service) handleViewerStream(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.NewString()
	if !s.authorized(s.opts.SessionID, r.Header.Get(browsercontract.CapabilityHeader)) {
		s.writeError(w, http.StatusForbidden, "forbidden", "BROWSER_CAPABILITY_INVALID", "Browser capability is invalid", requestID)
		return
	}
	if s.opts.Viewer == nil {
		s.writeError(w, http.StatusServiceUnavailable, "unavailable", "BROWSER_VIEWER_UNAVAILABLE", "Browser viewer is not available", requestID)
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled, InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	s.viewerConnections.Add(1)
	s.touchActivity()
	defer s.viewerConnections.Add(-1)
	if err := s.opts.Viewer.Serve(r.Context(), connection); err != nil && r.Context().Err() == nil {
		_ = connection.Close(websocket.StatusPolicyViolation, "browser viewer closed")
	}
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.NewString()
	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "SESSION_ID_REQUIRED", "sessionId is required", requestID)
		return
	}
	if !s.authorized(sessionID, r.Header.Get(browsercontract.CapabilityHeader)) {
		s.writeError(w, http.StatusForbidden, "forbidden", "BROWSER_CAPABILITY_INVALID", "Browser capability is invalid", requestID)
		return
	}
	connected, connectedAt := s.Connected()
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"sessionId":   sessionID,
		"connected":   connected,
		"connectedAt": connectedAt,
		"transport":   "vm-chromium",
	})
}

func (s *Service) authorized(sessionID, capability string) bool {
	if sessionID != s.opts.SessionID {
		return false
	}
	return s.authority.Valid(sessionID, strings.TrimSpace(capability), s.opts.CapabilityVerifier)
}

func (s *Service) writeCommandError(w http.ResponseWriter, _ *http.Request, err error, requestID string) {
	var commandErr *CommandError
	if !asCommandError(err, &commandErr) {
		s.writeError(w, http.StatusUnprocessableEntity, "unprocessable", "BROWSER_COMMAND_FAILED", err.Error(), requestID)
		return
	}
	status := http.StatusUnprocessableEntity
	typeName := "unprocessable"
	switch commandErr.Code {
	case "INVALID_ARGUMENT", "URL_REQUIRED", "REFERENCE_REQUIRED", "TAB_ID_REQUIRED",
		"INVALID_URL", "BROWSER_URL_FORBIDDEN", "AGENT_BROWSER_COMMAND_BLOCKED",
		"BROWSER_ACTION_UNSUPPORTED_CLOUD":
		status = http.StatusBadRequest
		typeName = "bad_request"
	case "STALE_REFERENCE", "TAB_NOT_FOUND":
		status = http.StatusConflict
		typeName = "conflict"
	case "BROWSER_USER_CONTROL_ACTIVE":
		status = http.StatusConflict
		typeName = "conflict"
	case "BROWSER_TARGET_UNAVAILABLE", "BROWSER_AUTOMATION_UNAVAILABLE", "AGENT_BROWSER_NOT_INSTALLED",
		"AGENT_BROWSER_START_FAILED", "AGENT_BROWSER_OUTPUT_TOO_LARGE", "BROWSER_AUTOMATION_INVALID_OUTPUT":
		status = http.StatusServiceUnavailable
		typeName = "unavailable"
	}
	s.writeError(w, status, typeName, commandErr.Code, commandErr.Message, requestID)
}

func browserActionMutates(action string, args map[string]any) bool {
	switch action {
	case "snapshot", "get", "tabs", "wait", "console", "errors", "screenshot":
		return false
	case "dialog":
		operation, _ := args["operation"].(string)
		return !strings.EqualFold(strings.TrimSpace(operation), "status")
	default:
		return true
	}
}

func (s *Service) writeError(w http.ResponseWriter, status int, typeName, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":     typeName,
		"code":      code,
		"message":   message,
		"requestId": requestID,
	})
}

func writeJSONResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// cloudUnsupported lists allowlisted actions the Stage 1 cloud engine does
// not implement (network capture is spec Stage 3; unhighlight is an
// Electron overlay feature).
var cloudUnsupported = map[string]struct{}{
	"network-start": {}, "network-status": {}, "network-list": {}, "network-stop": {},
	"network-clear": {}, "unhighlight": {},
}

var actVerbs = map[string]struct{}{
	"click": {}, "dblclick": {}, "focus": {}, "hover": {},
	"fill": {}, "type": {}, "check": {}, "uncheck": {},
}

func (s *Service) dispatch(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	if _, unsupported := cloudUnsupported[action]; unsupported {
		return nil, &CommandError{
			Code:    "BROWSER_ACTION_UNSUPPORTED_CLOUD",
			Message: "This browser action is not available in cloud sessions yet",
		}
	}
	switch action {
	case "devtools-open", "devtools-close":
		if s.opts.Viewer == nil {
			return nil, &CommandError{Code: "BROWSER_VIEWER_REQUIRED", Message: "Open the session browser viewer before opening DevTools."}
		}
		return s.opts.Viewer.DevTools(action == "devtools-open")
	case "open":
		raw, err := requireString(args, "url", "URL_REQUIRED", "url is required", false)
		if err != nil {
			return nil, err
		}
		normalized, err := NormalizeAgentBrowserURL(raw)
		if err != nil {
			return nil, err
		}
		data, err := s.opts.Engine.Execute(ctx, action, map[string]any{"url": normalized})
		if err != nil {
			return nil, err
		}
		return s.navStateResult(data), nil
	case "snapshot":
		snapshotArgs := map[string]any{}
		if interactive, ok := args["interactive"].(bool); ok && interactive {
			snapshotArgs["interactive"] = true
		}
		data, err := s.opts.Engine.Execute(ctx, action, snapshotArgs)
		if err != nil {
			return nil, err
		}
		text, _ := data["snapshot"].(string)
		result := map[string]any{
			"text":                     text,
			"refs":                     data["refs"],
			"untrustedExternalContent": true,
		}
		if boundary, ok := data["_boundary"]; ok {
			result["_boundary"] = boundary
		}
		return result, nil
	case "act":
		return s.actComposite(ctx, args)
	case "tabs":
		data, err := s.opts.Engine.Execute(ctx, action, nil)
		if err != nil {
			return nil, err
		}
		return s.tabsResult(data), nil
	case "tab-new":
		engineArgs := map[string]any{}
		if raw, present := optionalTrimmed(args, "url"); present {
			normalized, err := NormalizeAgentBrowserURL(raw)
			if err != nil {
				return nil, err
			}
			engineArgs["url"] = normalized
		}
		if _, err := s.opts.Engine.Execute(ctx, action, engineArgs); err != nil {
			return nil, err
		}
		return s.activeTabResult(ctx)
	case "tab-select":
		tabID, err := requireString(args, "tabId", "TAB_ID_REQUIRED", "tabId is required", false)
		if err != nil {
			return nil, err
		}
		if _, err := s.opts.Engine.Execute(ctx, action, map[string]any{"tabId": tabID}); err != nil {
			return nil, err
		}
		return s.activeTabResult(ctx)
	case "tab-close":
		engineArgs := map[string]any{}
		closed := ""
		if tabID, present := optionalTrimmed(args, "tabId"); present {
			engineArgs["tabId"] = tabID
			closed = tabID
		}
		if _, err := s.opts.Engine.Execute(ctx, action, engineArgs); err != nil {
			return nil, err
		}
		data, err := s.opts.Engine.Execute(ctx, "tabs", nil)
		if err != nil {
			return nil, err
		}
		tabs := s.tabsResult(data)
		if closed == "" {
			closed = stringField(tabs["activeTabId"])
		}
		result := map[string]any{
			"closedTabId":              closed,
			"activeTabId":              tabs["activeTabId"],
			"tabs":                     tabs["tabs"],
			"untrustedExternalContent": true,
		}
		return result, nil
	case "get":
		property, err := requireString(args, "property", "INVALID_ARGUMENT", "property is required", false)
		if err != nil {
			return nil, err
		}
		property = strings.ToLower(property)
		ref, hasRef := optionalTrimmed(args, "ref")
		switch property {
		case "url", "title", "text", "value", "checked":
		default:
			return nil, invalidArgument("Unsupported browser property: " + property)
		}
		if (property == "url" || property == "title") && hasRef {
			return nil, invalidArgument(property + " does not accept an element ref")
		}
		if (property == "value" || property == "checked") && !hasRef {
			return nil, &CommandError{Code: "REFERENCE_REQUIRED", Message: property + " requires an element ref"}
		}
		engineArgs := map[string]any{"property": property}
		if hasRef {
			engineArgs["ref"] = ref
		}
		data, err := s.opts.Engine.Execute(ctx, action, engineArgs)
		if err != nil {
			return nil, err
		}
		value, ok := data[property]
		if !ok {
			value = data["value"]
		}
		if property == "url" || property == "title" {
			if text, isString := value.(string); isString {
				if property == "url" {
					value = SanitizeBrowserURL(text)
				} else {
					value = SanitizeBrowserTitle(text)
				}
				data[property] = value
			}
		}
		data["value"] = value
		return data, nil
	case "console", "errors":
		data, err := s.opts.Engine.Execute(ctx, action, nil)
		if err != nil {
			return nil, err
		}
		return NormalizeNativeMessages(data, action), nil
	case "screenshot":
		data, width, height, err := s.opts.Engine.Screenshot(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"data":                     data,
			"width":                    width,
			"height":                   height,
			"untrustedExternalContent": true,
		}, nil
	default:
		// Host-equivalent pre-validation for the passthrough verbs, then a
		// straight engine round trip.
		if err := validatePassthrough(action, args); err != nil {
			return nil, err
		}
		return s.opts.Engine.Execute(ctx, action, args)
	}
}

// validatePassthrough mirrors the Electron host's stringArg/numberArg checks
// so missing arguments surface the same codes before the argv layer.
func validatePassthrough(action string, args map[string]any) error {
	switch action {
	case "click", "dblclick", "focus", "hover", "highlight", "scrollintoview", "check", "uncheck":
		_, err := requireString(args, "ref", "REFERENCE_REQUIRED", "ref is required", false)
		return err
	case "fill", "type":
		if _, err := requireString(args, "ref", "REFERENCE_REQUIRED", "ref is required", false); err != nil {
			return err
		}
		_, err := requireString(args, "text", "INVALID_ARGUMENT", "text is required", true)
		return err
	case "press":
		_, err := requireString(args, "key", "INVALID_ARGUMENT", "key is required", false)
		return err
	case "drag":
		if _, err := requireString(args, "ref", "REFERENCE_REQUIRED", "ref is required", false); err != nil {
			return err
		}
		_, err := requireString(args, "targetRef", "REFERENCE_REQUIRED", "target ref is required", false)
		return err
	case "select":
		if _, err := requireString(args, "ref", "REFERENCE_REQUIRED", "ref is required", false); err != nil {
			return err
		}
		_, err := requireString(args, "value", "INVALID_ARGUMENT", "value is required", true)
		return err
	case "scroll":
		direction, err := requireString(args, "direction", "INVALID_ARGUMENT", "direction is required", false)
		if err != nil {
			return err
		}
		switch strings.ToLower(direction) {
		case "up", "down", "left", "right":
		default:
			return invalidArgument("direction must be up, down, left, or right")
		}
		if raw, present := args["amount"]; present && raw != nil {
			if _, ok := raw.(float64); !ok {
				return invalidArgument("Numeric argument must be between 1 and 5000")
			}
		}
		return nil
	case "frame":
		_, err := requireString(args, "target", "INVALID_ARGUMENT", "frame target is required", false)
		return err
	case "dialog":
		operation, err := requireString(args, "operation", "INVALID_ARGUMENT", "dialog operation is required", false)
		if err != nil {
			return err
		}
		switch strings.ToLower(operation) {
		case "accept", "dismiss", "status":
			return nil
		default:
			return invalidArgument("dialog operation must be accept, dismiss, or status")
		}
	default:
		return nil
	}
}

func (s *Service) actComposite(ctx context.Context, args map[string]any) (map[string]any, error) {
	instruction, err := requireString(args, "instruction", "INVALID_ARGUMENT", "instruction is required", false)
	if err != nil {
		return nil, err
	}
	verb := "click"
	if raw, present := optionalTrimmed(args, "action"); present {
		verb = raw
	}
	if _, ok := actVerbs[verb]; !ok {
		return nil, invalidArgument("Unsupported act verb: " + verb)
	}
	value := ""
	hasValue := false
	if verb == "fill" || verb == "type" {
		raw, present := args["value"].(string)
		if !present {
			return nil, invalidArgument("value is required")
		}
		value = raw
		hasValue = true
	}
	nth := 0
	hasNth := false
	if raw, ok := args["nth"].(float64); ok && !math.IsNaN(raw) {
		nth = int(math.Trunc(raw))
		hasNth = true
	}

	snapshotOnce := func() (string, any, error) {
		data, err := s.opts.Engine.Execute(ctx, "snapshot", map[string]any{"interactive": true})
		if err != nil {
			return "", nil, err
		}
		text, _ := data["snapshot"].(string)
		return text, data["refs"], nil
	}
	nativeArgsForRef := func(ref string) map[string]any {
		engineArgs := map[string]any{"ref": ref}
		if hasValue {
			engineArgs["text"] = value
		}
		return engineArgs
	}
	unresolved := func(outcome string, candidates []ActCandidate, snapshot string) map[string]any {
		result := map[string]any{
			"outcome":                  outcome,
			"instruction":              instruction,
			"snapshot":                 snapshot,
			"untrustedExternalContent": true,
		}
		if outcome == "ambiguous" {
			encoded := make([]map[string]any, 0, len(candidates))
			for _, candidate := range candidates {
				encoded = append(encoded, map[string]any{
					"role": candidate.Role, "name": candidate.Name, "ref": candidate.Ref,
				})
			}
			result["candidates"] = encoded
		}
		return result
	}

	snapshot1, refs1, err := snapshotOnce()
	if err != nil {
		return nil, err
	}
	match1 := MatchInstruction(instruction, refs1, MatchOptions{Nth: nth, HasNth: hasNth})
	if match1.Outcome != OutcomeMatched {
		return unresolved(match1.Outcome, match1.Candidates, snapshot1), nil
	}
	result, err := s.opts.Engine.Execute(ctx, verb, nativeArgsForRef(match1.Candidate.Ref))
	if err == nil {
		return map[string]any{
			"outcome":                  "matched",
			"resolvedRef":              match1.Candidate.Ref,
			"candidate":                map[string]any{"role": match1.Candidate.Role, "name": match1.Candidate.Name, "ref": match1.Candidate.Ref},
			"result":                   result,
			"retried":                  false,
			"untrustedExternalContent": true,
		}, nil
	}
	var commandErr *CommandError
	if !asCommandError(err, &commandErr) || commandErr.Code != "STALE_REFERENCE" {
		return nil, err
	}
	// One retry on a stale reference: re-snapshot, re-match the ORIGINAL
	// instruction, try once more. Any second failure surfaces as itself.
	snapshot2, refs2, err := snapshotOnce()
	if err != nil {
		return nil, err
	}
	match2 := MatchInstruction(instruction, refs2, MatchOptions{Nth: nth, HasNth: hasNth})
	if match2.Outcome != OutcomeMatched {
		return unresolved(match2.Outcome, match2.Candidates, snapshot2), nil
	}
	result, err = s.opts.Engine.Execute(ctx, verb, nativeArgsForRef(match2.Candidate.Ref))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"outcome":                  "matched",
		"resolvedRef":              match2.Candidate.Ref,
		"candidate":                map[string]any{"role": match2.Candidate.Role, "name": match2.Candidate.Name, "ref": match2.Candidate.Ref},
		"result":                   result,
		"retried":                  true,
		"untrustedExternalContent": true,
	}, nil
}

// navStateResult shapes the open response like the desktop nav state, with
// sanitized url/title. Navigation history flags are Electron-view state the
// cloud engine does not track; they default to false.
func (s *Service) navStateResult(data map[string]any) map[string]any {
	resultURL := ""
	if value, ok := data["url"].(string); ok {
		resultURL = SanitizeBrowserURL(value)
	}
	resultTitle := ""
	if value, ok := data["title"].(string); ok {
		resultTitle = SanitizeBrowserTitle(value)
	}
	return map[string]any{
		"url":                      resultURL,
		"title":                    resultTitle,
		"canGoBack":                false,
		"canGoForward":             false,
		"isLoading":                false,
		"untrustedExternalContent": true,
	}
}

// tabsResult converts the engine's tab list into the desktop tabs shape with
// sanitized url/title. The Electron-only viewId and change fields are
// intentionally omitted.
func (s *Service) tabsResult(data map[string]any) map[string]any {
	rawTabs, _ := data["tabs"].([]any)
	activeTabID := ""
	tabs := make([]map[string]any, 0, len(rawTabs))
	for _, raw := range rawTabs {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		id := stringField(entry["tabId"])
		active, _ := entry["active"].(bool)
		if active {
			activeTabID = id
		}
		tab := map[string]any{
			"id":     id,
			"url":    SanitizeBrowserURL(stringField(entry["url"])),
			"title":  SanitizeBrowserTitle(stringField(entry["title"])),
			"active": active,
		}
		if favicon, ok := entry["favicon"].(string); ok && favicon != "" {
			tab["favicon"] = favicon
		}
		tabs = append(tabs, tab)
	}
	return map[string]any{
		"activeTabId":              activeTabID,
		"tabs":                     tabs,
		"untrustedExternalContent": true,
	}
}

// activeTabResult returns the single active tab (desktop agentTabResult
// shape: a bare tab object, not wrapped).
func (s *Service) activeTabResult(ctx context.Context) (map[string]any, error) {
	data, err := s.opts.Engine.Execute(ctx, "tabs", nil)
	if err != nil {
		return nil, err
	}
	tabs := s.tabsResult(data)
	for _, raw := range tabs["tabs"].([]map[string]any) {
		if active, _ := raw["active"].(bool); active {
			return raw, nil
		}
	}
	return map[string]any{"untrustedExternalContent": true}, nil
}

func requireString(args map[string]any, key, code, message string, allowEmpty bool) (string, error) {
	value, ok := args[key].(string)
	if !ok || (!allowEmpty && strings.TrimSpace(value) == "") {
		return "", &CommandError{Code: code, Message: message}
	}
	if allowEmpty {
		return value, nil
	}
	return strings.TrimSpace(value), nil
}

func optionalTrimmed(args map[string]any, key string) (string, bool) {
	if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), true
	}
	return "", false
}

func stringField(value any) string {
	text, _ := value.(string)
	return text
}
