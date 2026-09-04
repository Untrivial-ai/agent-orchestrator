package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
)

// Codex permission overrides persist to subsequent turns and native resumes.
// Returning to Default must restore a provider-resolved baseline, not merely
// omit fields and accidentally retain an earlier bypass setting.
type nativePermissionSettings struct {
	ApprovalPolicy    json.RawMessage `json:"approvalPolicy"`
	ApprovalsReviewer string          `json:"approvalsReviewer"`
	Sandbox           json.RawMessage `json:"sandbox"`
}

func (p nativePermissionSettings) sandboxMode() (string, bool) {
	var sandbox struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(p.Sandbox, &sandbox) != nil {
		return "", false
	}
	switch sandbox.Type {
	case "readOnly":
		return "read-only", true
	case "workspaceWrite":
		return "workspace-write", true
	case "dangerFullAccess":
		return "danger-full-access", true
	default:
		return "", false
	}
}

func (p nativePermissionSettings) valid() bool {
	if p.ApprovalsReviewer != "user" && p.ApprovalsReviewer != "auto_review" {
		return false
	}
	if !validNativeApprovalPolicy(p.ApprovalPolicy) {
		return false
	}
	_, ok := p.sandboxMode()
	return ok
}

func validNativeApprovalPolicy(raw json.RawMessage) bool {
	var policy string
	if json.Unmarshal(raw, &policy) == nil {
		switch policy {
		case "untrusted", "on-failure", "on-request", "never":
			return true
		default:
			return false
		}
	}
	var object map[string]map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != 1 {
		return false
	}
	granular, ok := object["granular"]
	if !ok {
		return false
	}
	for _, key := range []string{"sandbox_approval", "rules", "mcp_elicitations"} {
		if _, ok := granular[key]; !ok {
			return false
		}
	}
	for key, value := range granular {
		switch key {
		case "sandbox_approval", "rules", "mcp_elicitations", "request_permissions", "skill_approval":
		default:
			return false
		}
		var allowed bool
		if string(value) == "null" || json.Unmarshal(value, &allowed) != nil {
			return false
		}
	}
	return true
}

func (p nativePermissionSettings) applyApproval(params map[string]any) {
	params["approvalPolicy"] = p.ApprovalPolicy
	params["approvalsReviewer"] = p.ApprovalsReviewer
}

func (c *conversation) resolveNativePermissions(ctx context.Context) (*nativePermissionSettings, error) {
	if c.nativePermissions != nil {
		return c.nativePermissions, nil
	}
	// config/read omits built-in defaults when config.toml has no permission
	// entries. An ephemeral, turn-less thread reports Codex's effective settings
	// including project config and requirements, without writing a conversation.
	resolveCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	var response struct {
		nativePermissionSettings
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := c.conn.request(resolveCtx, "thread/start", map[string]any{"cwd": c.workdir, "ephemeral": true}, &response); err != nil {
		return nil, errors.New("codex native permissions could not be resolved; choose an explicit approval policy or retry")
	}
	if response.Thread.ID == "" || response.Thread.ID == c.threadID {
		return nil, errors.New("codex native permissions probe did not return a separate thread")
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), handshakeTimeout)
	defer cleanupCancel()
	if err := c.conn.request(cleanupCtx, "thread/unsubscribe", map[string]any{"threadId": response.Thread.ID}, nil); err != nil {
		return nil, errors.New("codex native permissions probe could not be closed; retry before sending a turn")
	}
	if !response.valid() {
		return nil, errors.New("codex did not report usable native permissions; choose an explicit approval policy")
	}
	c.nativePermissions = &response.nativePermissionSettings
	return c.nativePermissions, nil
}
