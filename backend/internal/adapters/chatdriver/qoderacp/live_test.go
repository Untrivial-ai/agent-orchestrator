package qoderacp

import (
	"os"
	"testing"
)

// TestLiveQoderACP is intentionally an explicit release gate. Registration in
// chatdriver/registry must not happen until this authenticated suite exercises
// load/replay/approval/cancel/reconnect against the pinned executable.
func TestLiveQoderACP(t *testing.T) {
	if os.Getenv("AO_QODER_ACP_E2E") != "1" {
		t.Skip("set AO_QODER_ACP_E2E=1 with authenticated Qoder CLI")
	}
	t.Fatal("authenticated Qoder ACP lifecycle conformance has not been implemented; keep the driver unregistered")
}
