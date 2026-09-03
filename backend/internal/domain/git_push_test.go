package domain

import (
	"strings"
	"testing"
	"time"
)

func TestPushApprovalExpectedHeadSHAValidation(t *testing.T) {
	now := time.Now().UTC()
	valid := PushApproval{ID: "approval", ProjectID: "project", SessionID: "session", Repository: "repo",
		Remote: "origin", RemoteURL: "https://example.test/repo.git", Branch: "main",
		CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	for _, sha := range []string{strings.Repeat("a", 40), strings.Repeat("B", 64)} {
		valid.ExpectedHeadSHA = sha
		if err := valid.ValidateForCreate(); err != nil {
			t.Fatalf("valid SHA %q rejected: %v", sha, err)
		}
	}
	for _, sha := range []string{"abc", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("g", 40), strings.Repeat("a", 39) + "\n"} {
		valid.ExpectedHeadSHA = sha
		if err := valid.ValidateForCreate(); err == nil {
			t.Fatalf("invalid SHA %q accepted", sha)
		}
	}
}
