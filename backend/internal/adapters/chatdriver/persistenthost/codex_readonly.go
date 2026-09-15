package persistenthost

import "encoding/json"

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
		delete(h.codexReadOnly, request.Params.ThreadID)
	case "turn/start":
		// An explicitly read-only dispatch cannot broaden an already confirmed
		// read-only thread, including while its settings notification is in flight.
		if h.codexReadOnly[request.Params.ThreadID] &&
			codexApprovalPolicy(request.Params.ApprovalPolicy) == "never" && request.Params.SandboxPolicy.Type == "readOnly" {
			return
		}
		delete(h.codexReadOnly, request.Params.ThreadID)
	}
}

// observeCodexReadOnly uses the native thread-open response or subsequent
// settings notification. Requested settings and turn acceptance are not proof
// of the effective provider policy. Called under host.mu.
func (h *host) observeCodexReadOnly(frame []byte) {
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
	approvalPolicy := message.Result.ApprovalPolicy
	sandboxType := message.Result.Sandbox.Type
	if message.Method == "thread/settings/updated" {
		id = message.Params.ThreadID
		approvalPolicy = message.Params.ThreadSettings.ApprovalPolicy
		sandboxType = message.Params.ThreadSettings.SandboxPolicy.Type
	}
	if id == "" {
		return
	}
	if approvalPolicy == nil || sandboxType == "" {
		// thread/read returns history without policy fields. It does not change
		// permissions; resume already invalidated its receipt before dispatch.
		if message.Method == "thread/settings/updated" || message.Result.ApprovalPolicy != nil {
			delete(h.codexReadOnly, id)
		}
		return
	}
	if codexApprovalPolicy(approvalPolicy) != "never" || sandboxType != "readOnly" {
		delete(h.codexReadOnly, id)
		return
	}
	if h.codexReadOnly == nil {
		h.codexReadOnly = make(map[string]bool)
	}
	h.codexReadOnly[id] = true
}

// Granular policies cannot prove "never". Decode them as unknown without losing
// the enclosing thread identity needed to invalidate a previous receipt.
func codexApprovalPolicy(raw json.RawMessage) string {
	var policy string
	_ = json.Unmarshal(raw, &policy)
	return policy
}
