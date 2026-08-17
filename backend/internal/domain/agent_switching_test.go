package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestComputeAgentSwitchRequestFingerprintNormalizesAndIncludesModel(t *testing.T) {
	base := ComputeAgentSwitchRequestFingerprint("session-1", HarnessCodex, "gpt-5.4")
	same := ComputeAgentSwitchRequestFingerprint("session-1", HarnessCodex, "  gpt-5.4  ")
	other := ComputeAgentSwitchRequestFingerprint("session-1", HarnessCodex, "gpt-5.4-mini")

	if base != same {
		t.Fatalf("normalized model fingerprint = %q, want %q", same, base)
	}
	if base == other {
		t.Fatal("different model overrides produced the same request fingerprint")
	}
	if !strings.HasPrefix(string(base), "v1:") {
		t.Fatalf("new fingerprint = %q, want persisted v1 prefix", base)
	}
}

func TestAgentSwitchRequestFingerprintMatchesLegacyRequestWithoutModel(t *testing.T) {
	payload, err := json.Marshal(struct {
		SessionID     SessionID    `json:"sessionId"`
		TargetHarness AgentHarness `json:"targetHarness"`
		Note          string       `json:"note"`
	}{SessionID: "session-1", TargetHarness: HarnessCodex, Note: ""})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	legacy := AgentSwitchRequestFingerprint("v1:" + hex.EncodeToString(sum[:]))
	if !legacy.Valid() {
		t.Fatalf("legacy fingerprint should remain valid: %q", legacy)
	}
	if !legacy.MatchesRequest("session-1", HarnessCodex, "") {
		t.Fatal("legacy fingerprint should match the same request without a model override")
	}
	if legacy.MatchesRequest("session-1", HarnessCodex, "gpt-5.4") {
		t.Fatal("legacy fingerprint must not match a request with a model override")
	}
}
