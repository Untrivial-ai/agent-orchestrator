package persistenthost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// ACPEventIDMetaKey identifies one replayable provider event. AO assigns the
	// value to every durable event derived from that frame.
	ACPEventIDMetaKey = "ao.persistentEventId"
)

type acpClientRequest struct {
	clientID   json.RawMessage
	method     string
	params     json.RawMessage
	generation uint64
}

type acpServerRequest struct {
	requestID   string
	frame       []byte
	promptOwned bool
}

type acpInteractionCommand struct {
	eventID     string
	canonical   string
	params      json.RawMessage
	promptOwned bool
}

type acpReplayKind uint8

const (
	acpReplayFrame acpReplayKind = iota
	acpReplayRequest
	acpReplayCommand
)

type acpReplayEntry struct {
	kind acpReplayKind
	key  string
	span acpJournalSpan
}

type acpClientFrames struct {
	provider []byte
	client   []byte
}

// acpRelay is deliberately only a JSON-RPC correlation layer. The pinned ACP
// SDK and all provider semantics remain in the adapter; the host owns only the
// connection state that cannot be reconstructed after a daemon dies.
type acpRelay struct {
	identity          string
	nextRequestID     int64
	nextEventID       uint64
	nextInteractionID uint64
	pending           map[string]acpClientRequest
	serverRequests    map[string]acpServerRequest
	serverOrder       []string
	commands          map[string]acpInteractionCommand
	commandOrder      []string
	promptReplay      []acpReplayEntry
	state             ACPState
	promptJournal     *acpPromptJournal
	promptResult      []byte
	cancelRequested   bool
}

func newACPRelay(ctx context.Context, journalPath string) (*acpRelay, error) {
	identity, err := randomToken()
	if err != nil {
		return nil, err
	}
	journal, err := openACPPromptJournal(ctx, journalPath)
	if err != nil {
		return nil, err
	}
	return &acpRelay{
		identity: identity,
		pending:  make(map[string]acpClientRequest), serverRequests: make(map[string]acpServerRequest),
		commands:      make(map[string]acpInteractionCommand),
		promptJournal: journal,
	}, nil
}

func (r *acpRelay) snapshot() *ACPState {
	state := r.state
	state.InitializeResult = append(json.RawMessage(nil), state.InitializeResult...)
	state.SessionResult = append(json.RawMessage(nil), state.SessionResult...)
	return &state
}

func (r *acpRelay) replayTo(ctx context.Context, dst io.Writer) error {
	for _, entry := range r.promptReplay {
		switch entry.kind {
		case acpReplayFrame:
			if err := r.promptJournal.replayTo(ctx, dst, entry.span); err != nil {
				return err
			}
		case acpReplayRequest:
			if request, ok := r.serverRequest(entry.key); ok {
				if _, err := dst.Write(request.frame); err != nil {
					return err
				}
			}
		case acpReplayCommand:
			if command, ok := r.commands[entry.key]; ok {
				if err := r.replayInteractionCommand(ctx, dst, entry.key, command); err != nil {
					return err
				}
			}
		}
	}
	for _, providerID := range r.serverOrder {
		request, ok := r.serverRequests[providerID]
		if !ok || request.promptOwned {
			continue
		}
		if _, err := dst.Write(request.frame); err != nil {
			return err
		}
	}
	for _, requestID := range r.commandOrder {
		command, ok := r.commands[requestID]
		if !ok || command.promptOwned {
			continue
		}
		if err := r.replayInteractionCommand(ctx, dst, requestID, command); err != nil {
			return err
		}
	}
	if len(r.promptResult) > 0 {
		_, err := dst.Write(r.promptResult)
		return err
	}
	return nil
}

func (r *acpRelay) replayInteractionCommand(
	ctx context.Context,
	dst io.Writer,
	requestID string,
	command acpInteractionCommand,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(command.params, &params); err != nil {
		return err
	}
	params["eventId"], _ = json.Marshal(command.eventID)
	_, pending := r.serverRequest(requestID)
	params["providerPending"], _ = json.Marshal(pending)
	encoded, _ := json.Marshal(params)
	_, err := dst.Write(marshalFrame(map[string]json.RawMessage{
		"jsonrpc": json.RawMessage(`"2.0"`),
		"method":  json.RawMessage(`"` + ACPInteractionCommandMethod + `"`),
		"params":  encoded,
	}, nil))
	return err
}

func (r *acpRelay) close(ctx context.Context) error { return r.promptJournal.close(ctx) }

func (r *acpRelay) clientFrame(ctx context.Context, frame []byte, generation uint64) (acpClientFrames, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(frame, &envelope) != nil {
		return acpClientFrames{provider: frame}, nil //nolint:nilerr // preserve malformed input so the ACP peer owns its protocol error
	}
	var method string
	_ = json.Unmarshal(envelope["method"], &method)
	if method == ACPPromptAckMethod {
		var params struct {
			EventID string `json:"eventId"`
		}
		_ = json.Unmarshal(envelope["params"], &params)
		if params.EventID != "" && params.EventID == r.state.PendingResultEventID {
			if err := r.promptJournal.reset(ctx); err != nil {
				return acpClientFrames{}, err
			}
			r.promptResult = nil
			r.state.PendingResultEventID = ""
			r.commands = make(map[string]acpInteractionCommand)
			r.commandOrder = nil
			r.promptReplay = nil
		}
		return acpClientFrames{}, nil
	}
	if method == ACPInteractionCommandMethod {
		return r.acceptInteractionCommand(ctx, envelope)
	}
	if method == "$/cancel_request" {
		return acpClientFrames{provider: r.clientCancellation(envelope, frame, generation)}, nil
	}
	if method == "session/cancel" && r.state.ActivePrompt {
		if r.cancelRequested {
			return acpClientFrames{}, nil
		}
		r.cancelRequested = true
	}
	id := envelope["id"]
	if method == "" && len(id) > 0 {
		r.removeServerRequest(string(id))
	}
	if method == "" || len(id) == 0 || string(id) == "null" {
		return acpClientFrames{provider: frame}, nil
	}

	if method == "session/prompt" && (r.state.ActivePrompt || r.state.PendingResultEventID != "") {
		return acpClientFrames{client: rpcErrorFrame(id, "previous ACP prompt is active or not acknowledged")}, nil
	}
	r.nextRequestID++
	providerID, _ := json.Marshal(r.nextRequestID)
	r.pending[string(providerID)] = acpClientRequest{
		clientID: append(json.RawMessage(nil), id...), method: method,
		params: append(json.RawMessage(nil), envelope["params"]...), generation: generation,
	}
	envelope["id"] = providerID
	if method == "session/prompt" {
		r.state.ActivePrompt = true
		r.state.PendingResultEventID = ""
		r.cancelRequested = false
		if err := r.promptJournal.reset(ctx); err != nil {
			return acpClientFrames{}, err
		}
		r.promptResult = nil
		r.commands = make(map[string]acpInteractionCommand)
		r.commandOrder = nil
		r.promptReplay = nil
	}
	return acpClientFrames{provider: marshalFrame(envelope, frame)}, nil
}

func (r *acpRelay) acceptInteractionCommand(
	ctx context.Context,
	envelope map[string]json.RawMessage,
) (acpClientFrames, error) {
	id := envelope["id"]
	var identity struct {
		RequestID string `json:"requestId"`
	}
	params := envelope["params"]
	if len(id) == 0 || string(id) == "null" || json.Unmarshal(params, &identity) != nil || identity.RequestID == "" {
		return acpClientFrames{client: rpcErrorFrame(id, "invalid persistent interaction command")}, nil //nolint:nilerr // protocol validation errors are returned in-band
	}
	canonical, err := canonicalJSON(params)
	if err != nil {
		return acpClientFrames{client: rpcErrorFrame(id, "invalid persistent interaction command")}, nil //nolint:nilerr // protocol validation errors are returned in-band
	}
	if existing, ok := r.commands[identity.RequestID]; ok {
		if existing.canonical != canonical {
			return acpClientFrames{client: rpcErrorFrame(id, "persistent interaction command changed")}, nil
		}
		return acpClientFrames{client: rpcResultFrame(id, map[string]string{"eventId": existing.eventID})}, nil
	}
	request, pending := r.serverRequest(identity.RequestID)
	if !pending {
		return acpClientFrames{client: rpcErrorFrame(id, "persistent interaction is not pending")}, nil
	}
	if err := ctx.Err(); err != nil {
		return acpClientFrames{}, err
	}
	command := acpInteractionCommand{
		eventID: r.newEventID(), canonical: canonical,
		params:      append(json.RawMessage(nil), params...),
		promptOwned: request.promptOwned,
	}
	r.commands[identity.RequestID] = command
	r.commandOrder = append(r.commandOrder, identity.RequestID)
	if command.promptOwned {
		r.promptReplay = append(r.promptReplay, acpReplayEntry{kind: acpReplayCommand, key: identity.RequestID})
	}
	return acpClientFrames{client: rpcResultFrame(id, map[string]string{"eventId": command.eventID})}, nil
}

func (r *acpRelay) clientCancellation(
	envelope map[string]json.RawMessage,
	fallback []byte,
	generation uint64,
) []byte {
	var params map[string]json.RawMessage
	if json.Unmarshal(envelope["params"], &params) != nil {
		return fallback
	}
	target := params["requestId"]
	for providerID, request := range r.pending {
		if request.generation != generation || string(request.clientID) != string(target) {
			continue
		}
		params["requestId"] = json.RawMessage(providerID)
		encoded, _ := json.Marshal(params)
		envelope["params"] = encoded
		return marshalFrame(envelope, fallback)
	}
	return fallback
}

// providerFrame returns the daemon-facing frame and whether the ACP profile owns
// its replay. A retained frame must not also enter the generic detached buffer or
// it would be replayed twice.
func (r *acpRelay) providerFrame(
	ctx context.Context,
	frame []byte,
	generation uint64,
	attached bool,
) ([]byte, bool, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(frame, &envelope) != nil {
		return frame, false, nil //nolint:nilerr // preserve malformed output for the SDK's protocol error
	}
	var method string
	_ = json.Unmarshal(envelope["method"], &method)
	id := envelope["id"]

	if method != "" {
		if len(id) > 0 && string(id) != "null" {
			rewritten, key, created := r.providerRequest(envelope, frame)
			if created && r.state.ActivePrompt {
				r.promptReplay = append(r.promptReplay, acpReplayEntry{kind: acpReplayRequest, key: key})
			}
			return rewritten, true, nil
		}
		if r.state.ActivePrompt {
			eventID := r.newEventID()
			injectMeta(envelope, ACPEventIDMetaKey, eventID)
			rewritten := marshalFrame(envelope, frame)
			span, err := r.promptJournal.append(ctx, rewritten)
			if err != nil {
				return nil, false, err
			}
			r.promptReplay = append(r.promptReplay, acpReplayEntry{kind: acpReplayFrame, span: span})
			if !attached {
				return nil, true, nil
			}
			return rewritten, true, nil
		}
		return frame, false, nil
	}
	if len(id) == 0 {
		return frame, false, nil
	}

	request, ok := r.pending[string(id)]
	if !ok {
		return frame, false, nil
	}
	delete(r.pending, string(id))
	r.captureResponse(request, envelope)

	if request.method == "session/prompt" {
		r.state.ActivePrompt = false
		r.cancelRequested = false
		eventID := r.newEventID()
		injectResultMeta(envelope, ACPEventIDMetaKey, eventID)
		rewritten := rewriteResponseID(envelope, request.clientID, frame)
		r.promptResult = promptResultNotification(envelope, eventID)
		r.state.PendingResultEventID = eventID
		if !attached {
			return nil, true, nil
		}
		if request.generation != generation {
			return append([]byte(nil), r.promptResult...), true, nil
		}
		return rewritten, true, nil
	}
	if request.generation != generation || !attached {
		return nil, false, nil
	}
	return rewriteResponseID(envelope, request.clientID, frame), false, nil
}

func (r *acpRelay) providerRequest(
	envelope map[string]json.RawMessage,
	fallback []byte,
) (frame []byte, key string, created bool) {
	id := envelope["id"]
	key = string(id)
	request := r.serverRequests[key]
	if request.requestID == "" {
		r.nextInteractionID++
		request.requestID = fmt.Sprintf("acp-request:%s:%d", r.identity, r.nextInteractionID)
		request.promptOwned = r.state.ActivePrompt
		r.serverOrder = append(r.serverOrder, key)
		created = true
	}
	injectMeta(envelope, ACPRequestIDMetaKey, request.requestID)
	request.frame = marshalFrame(envelope, fallback)
	r.serverRequests[key] = request
	return request.frame, request.requestID, created
}

func (r *acpRelay) serverRequest(requestID string) (acpServerRequest, bool) {
	for _, request := range r.serverRequests {
		if request.requestID == requestID {
			return request, true
		}
	}
	return acpServerRequest{}, false
}

func (r *acpRelay) removeServerRequest(providerID string) {
	if _, ok := r.serverRequests[providerID]; !ok {
		return
	}
	delete(r.serverRequests, providerID)
	for i, id := range r.serverOrder {
		if id == providerID {
			r.serverOrder = append(r.serverOrder[:i], r.serverOrder[i+1:]...)
			return
		}
	}
}

func canonicalJSON(raw json.RawMessage) (string, error) {
	if !json.Valid(raw) {
		return "", errors.New("invalid JSON")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func rpcResultFrame(id json.RawMessage, result any) []byte {
	encoded, _ := json.Marshal(result)
	return marshalFrame(map[string]json.RawMessage{
		"jsonrpc": json.RawMessage(`"2.0"`), "id": id, "result": encoded,
	}, nil)
}

func rpcErrorFrame(id json.RawMessage, message string) []byte {
	encoded, _ := json.Marshal(map[string]any{"code": -32602, "message": message})
	return marshalFrame(map[string]json.RawMessage{
		"jsonrpc": json.RawMessage(`"2.0"`), "id": id, "error": encoded,
	}, nil)
}

func (r *acpRelay) captureResponse(request acpClientRequest, envelope map[string]json.RawMessage) {
	if len(envelope["error"]) > 0 && string(envelope["error"]) != "null" {
		return
	}
	result := envelope["result"]
	switch request.method {
	case "initialize":
		var params struct {
			Meta map[string]string `json:"_meta"`
		}
		_ = json.Unmarshal(request.params, &params)
		r.state.InitialPermissions = params.Meta[ACPInitialPermissionsMetaKey]
		r.state.InitializeResult = append(json.RawMessage(nil), result...)
	case "session/new", "session/load", "session/resume":
		r.state.SessionResult = append(json.RawMessage(nil), result...)
		var response struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(result, &response)
		if response.SessionID != "" {
			r.state.SessionID = response.SessionID
		} else {
			var params struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(request.params, &params)
			if params.SessionID != "" {
				r.state.SessionID = params.SessionID
			}
		}
	}
}

func (r *acpRelay) newEventID() string {
	r.nextEventID++
	return fmt.Sprintf("acp-host:%s:%d", r.identity, r.nextEventID)
}

func rewriteResponseID(envelope map[string]json.RawMessage, id json.RawMessage, fallback []byte) []byte {
	envelope["id"] = append(json.RawMessage(nil), id...)
	return marshalFrame(envelope, fallback)
}

func promptResultNotification(response map[string]json.RawMessage, eventID string) []byte {
	params := map[string]json.RawMessage{}
	if result := response["result"]; len(result) > 0 {
		params["result"] = result
	}
	if rpcErr := response["error"]; len(rpcErr) > 0 {
		params["error"] = rpcErr
	}
	eventJSON, _ := json.Marshal(eventID)
	params["eventId"] = eventJSON
	encodedParams, _ := json.Marshal(params)
	method, _ := json.Marshal(ACPPromptResultMethod)
	return marshalFrame(map[string]json.RawMessage{
		"jsonrpc": json.RawMessage(`"2.0"`), "method": method, "params": encodedParams,
	}, nil)
}

func injectResultMeta(envelope map[string]json.RawMessage, key, value string) {
	var rpcErr map[string]json.RawMessage
	if json.Unmarshal(envelope["error"], &rpcErr) == nil && rpcErr != nil {
		// Error data can be any JSON value. Wrap it rather than overwrite it.
		data := map[string]json.RawMessage{"providerData": rpcErr["data"]}
		data[key], _ = json.Marshal(value)
		if len(data["providerData"]) == 0 {
			delete(data, "providerData")
		}
		rpcErr["data"], _ = json.Marshal(data)
		envelope["error"], _ = json.Marshal(rpcErr)
		return
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(envelope["result"], &result) != nil {
		return
	}
	if result == nil {
		result = make(map[string]json.RawMessage)
	}
	injectRawMeta(result, key, value)
	encoded, _ := json.Marshal(result)
	envelope["result"] = encoded
}

func injectMeta(envelope map[string]json.RawMessage, key, value string) {
	var params map[string]json.RawMessage
	if json.Unmarshal(envelope["params"], &params) != nil || params == nil {
		params = make(map[string]json.RawMessage)
	}
	injectRawMeta(params, key, value)
	encoded, _ := json.Marshal(params)
	envelope["params"] = encoded
}

func injectRawMeta(object map[string]json.RawMessage, key, value string) {
	var meta map[string]json.RawMessage
	if json.Unmarshal(object["_meta"], &meta) != nil || meta == nil {
		meta = make(map[string]json.RawMessage)
	}
	encoded, _ := json.Marshal(value)
	meta[key] = encoded
	encodedMeta, _ := json.Marshal(meta)
	object["_meta"] = encodedMeta
}

func marshalFrame(envelope map[string]json.RawMessage, fallback []byte) []byte {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return fallback
	}
	return append(encoded, '\n')
}
