package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestCISummaryExposesOnlyConfirmedCurrentBlockingReason(t *testing.T) {
	const reason = "Job execution was blocked by account billing or spending limits."
	checks := []domain.PullRequestCheck{
		{Name: "blocked", CommitHash: "head", Status: domain.PRCheckUnknown, Conclusion: "failure", LogTail: reason},
		{Name: "old", CommitHash: "previous", Status: domain.PRCheckUnknown, Conclusion: "failure", LogTail: reason},
		{Name: "unknown", CommitHash: "head", Status: domain.PRCheckUnknown, Conclusion: "failure", LogTail: "raw private diagnostics"},
		{Name: "test", CommitHash: "head", Status: domain.PRCheckFailed, Conclusion: "failure", LogTail: reason},
	}
	for _, state := range []domain.CIState{domain.CIUnknown, domain.CIFailing} {
		t.Run(string(state), func(t *testing.T) {
			result := summarizeCI(domain.PullRequest{CI: state, HeadSHA: "head"}, checks)
			body, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"blockedChecks":[{"name":"blocked","reason":"`+reason+`"}]`) {
				t.Fatalf("missing confirmed blocking reason: %s", body)
			}
			if strings.Contains(string(body), "private") || strings.Contains(string(body), `"old"`) {
				t.Fatalf("leaked unrelated or stale evidence: %s", body)
			}
		})
	}
	for _, pr := range []domain.PullRequest{{CI: domain.CIUnknown, Closed: true}, {CI: domain.CIUnknown, Merged: true}, {CI: domain.CIPassing}} {
		body, err := json.Marshal(summarizeCI(pr, checks))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), reason) {
			t.Fatalf("inactive or superseded blocking reason: %s", body)
		}
	}
}
