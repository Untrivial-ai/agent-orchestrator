package persistenthost

import "testing"

func TestCodexPermissionProofTracksProviderState(t *testing.T) {
	for _, tc := range []struct {
		name, request, update string
		known                 bool
	}{
		{"unrelated traffic", `{"method":"model/list"}`, `{"method":"turn/completed"}`, true},
		{"explicit read-only turn", `{"method":"turn/start","params":{"threadId":"t","approvalPolicy":"never","sandboxPolicy":{"type":"readOnly"}}}`, "", true},
		{"turn omits sandbox", `{"method":"turn/start","params":{"threadId":"t","approvalPolicy":"never"}}`, "", false},
		{"turn broadens", `{"method":"turn/start","params":{"threadId":"t","approvalPolicy":"never","sandboxPolicy":{"type":"dangerFullAccess"}}}`, "", false},
		{"resume awaiting receipt", `{"method":"thread/resume","params":{"threadId":"t"}}`, "", false},
		{"provider confirms turn", `{"method":"turn/start","params":{"threadId":"t"}}`, `{"method":"thread/settings/updated","params":{"threadId":"t","threadSettings":{"approvalPolicy":"never","sandboxPolicy":{"type":"readOnly"}}}}`, true},
		{"incomplete provider state", "", `{"method":"thread/settings/updated","params":{"threadId":"t","threadSettings":{"approvalPolicy":"never"}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &host{}
			h.observeCodexPermissions([]byte(`{"id":1,"result":{"thread":{"id":"t"},"approvalPolicy":"never","sandbox":{"type":"readOnly"}}}`))
			h.observeCodexRequest([]byte(tc.request))
			h.observeCodexPermissions([]byte(tc.update))
			policy, known := h.codexPermissions["t"]
			if known != tc.known {
				t.Fatalf("policy=%+v known=%v, want %v", policy, known, tc.known)
			}
			if known && (policy.ApprovalPolicy != "never" || policy.SandboxType != "readOnly") {
				t.Fatalf("policy=%+v", policy)
			}
		})
	}
}
