//go:build chatui_regression

package controllers_test

import (
	"net/http"
	"testing"
)

// The generated frontend client percent-encodes the colon in AO's synthetic
// root-branch identifier. The loopback API must hand the decoded durable ID to
// the conversation service; otherwise a branch advertised by the read model is
// impossible to activate through the public route.
func TestChatUIRegressionEncodedSyntheticBranchIDIsDecoded(t *testing.T) {
	const rootBranchID = "conversation:root"
	svc := &fakeChatService{activate: rootBranchID}
	srv := newChatTestServer(t, svc)

	body, status, _ := doRequest(t, srv, http.MethodPost,
		"/api/v1/sessions/ao-1/conversation/branches/conversation%3Aroot/activate", "")
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s; want %d", status, body, http.StatusAccepted)
	}
	if svc.gotBranchID != rootBranchID {
		t.Fatalf("service branch id = %q, want decoded %q", svc.gotBranchID, rootBranchID)
	}
}
