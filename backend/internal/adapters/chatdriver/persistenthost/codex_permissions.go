package persistenthost

import "encoding/json"

// CodexPermissions is provider-reported state, retained by the process owner
// across daemon attachments. Missing state is unknown, never inferred from a
// requested config. The raw relay does not change the provider's wire protocol.
type CodexPermissions struct {
	ApprovalPolicy string `json:"approvalPolicy"`
	SandboxType    string `json:"sandboxType"`
}

// observeCodexRequest invalidates proof before a request can change it. A daemon
// crash between dispatch and the provider response must not replay stale proof.
// Called under host.mu before forwarding the request.
func (h *host) observeCodexRequest(frame []byte) {
	var request struct {
		Method string `json:"method"`
		Params struct {
			ThreadID       string          `json:"threadId"`
			ApprovalPolicy json.RawMessage `json:"approvalPolicy"`
			SandboxPolicy  struct {
				Type string `json:"type"`
			} `json:"sandboxPolicy"`
		} `json:"params"`
	}
	if json.Unmarshal(frame, &request) != nil || request.Params.ThreadID == "" {
		return
	}
	switch request.Method {
	case "thread/resume":
		delete(h.codexPermissions, request.Params.ThreadID)
	case "turn/start":
		// An explicitly read-only dispatch cannot broaden an already confirmed
		// read-only thread, including while its settings notification is in flight.
		previous := h.codexPermissions[request.Params.ThreadID]
		if previous.ApprovalPolicy == "never" && previous.SandboxType == "readOnly" &&
			codexApprovalPolicy(request.Params.ApprovalPolicy) == "never" && request.Params.SandboxPolicy.Type == "readOnly" {
			return
		}
		delete(h.codexPermissions, request.Params.ThreadID)
	}
}

// observeCodexPermissions uses the native thread-open response or subsequent
// settings notification. Requested settings and turn acceptance are not proof
// of the effective provider policy. Called under host.mu.
func (h *host) observeCodexPermissions(frame []byte) {
	var message struct {
		Method string `json:"method"`
		Result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
			ApprovalPolicy json.RawMessage `json:"approvalPolicy"`
			Sandbox        struct {
				Type string `json:"type"`
			} `json:"sandbox"`
		} `json:"result"`
		Params struct {
			ThreadID       string `json:"threadId"`
			ThreadSettings struct {
				ApprovalPolicy json.RawMessage `json:"approvalPolicy"`
				SandboxPolicy  struct {
					Type string `json:"type"`
				} `json:"sandboxPolicy"`
			} `json:"threadSettings"`
		} `json:"params"`
	}
	if json.Unmarshal(frame, &message) != nil {
		return
	}
	id := message.Result.Thread.ID
	policy := CodexPermissions{ApprovalPolicy: codexApprovalPolicy(message.Result.ApprovalPolicy), SandboxType: message.Result.Sandbox.Type}
	if message.Method == "thread/settings/updated" {
		id = message.Params.ThreadID
		policy = CodexPermissions{ApprovalPolicy: codexApprovalPolicy(message.Params.ThreadSettings.ApprovalPolicy),
			SandboxType: message.Params.ThreadSettings.SandboxPolicy.Type}
	}
	if id == "" {
		return
	}
	if policy.ApprovalPolicy == "" || policy.SandboxType == "" {
		// thread/read returns history without policy fields. It does not change
		// permissions; resume already invalidated its receipt before dispatch.
		if message.Method == "thread/settings/updated" || message.Result.ApprovalPolicy != nil {
			delete(h.codexPermissions, id)
		}
		return
	}
	if h.codexPermissions == nil {
		h.codexPermissions = make(map[string]CodexPermissions)
	}
	h.codexPermissions[id] = policy
}

// Granular policies cannot prove "never". Decode them as unknown without losing
// the enclosing thread identity needed to invalidate a previous receipt.
func codexApprovalPolicy(raw json.RawMessage) string {
	var policy string
	_ = json.Unmarshal(raw, &policy)
	return policy
}
