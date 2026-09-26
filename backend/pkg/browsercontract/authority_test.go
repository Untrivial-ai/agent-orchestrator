package browsercontract_test

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
)

func TestAuthorityIssueAndValid(t *testing.T) {
	authority := browsercontract.NewAuthority()
	token, verifier, err := authority.Issue("sess-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" || verifier == "" {
		t.Fatal("Issue returned empty token or verifier")
	}
	if !authority.Valid("sess-1", token, verifier) {
		t.Error("Valid rejected the capability it issued")
	}
	if authority.Valid("sess-2", token, verifier) {
		t.Error("Valid accepted a capability for a different session")
	}
	if authority.Valid("sess-1", "tampered", verifier) {
		t.Error("Valid accepted a tampered token")
	}
	if authority.Valid("sess-1", "", verifier) || authority.Valid("sess-1", token, "") {
		t.Error("Valid accepted empty credentials")
	}
}

func TestAuthorityIssueRequiresSessionID(t *testing.T) {
	authority := browsercontract.NewAuthority()
	if _, _, err := authority.Issue(""); err == nil {
		t.Fatal("Issue with empty session id must fail")
	}
}

func TestAuthorityIssuesUniqueTokens(t *testing.T) {
	authority := browsercontract.NewAuthority()
	first, firstVerifier, err := authority.Issue("sess-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	second, secondVerifier, err := authority.Issue("sess-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if first == second || firstVerifier == secondVerifier {
		t.Error("Issue must not repeat tokens or verifiers")
	}
	if authority.Valid("sess-1", first, secondVerifier) {
		t.Error("verifiers must be bound to their own token")
	}
}
