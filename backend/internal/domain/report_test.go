package domain

import (
	"strings"
	"testing"
	"time"
)

func TestValidateReportContent(t *testing.T) {
	pr := "https://github.com/owner/repo/pull/42"
	tests := []struct {
		name          string
		state         ReportState
		note, message string
		outputs       []ReportOutput
		wantErr       bool
	}{
		{name: "free form", message: strings.Repeat("界", 1000)},
		{name: "output only", outputs: []ReportOutput{{Kind: ReportOutputArtifact, Reference: "opaque://anything"}, {Kind: ReportOutputPRCreated, Reference: pr}, {Kind: ReportOutputPRReviewed, Reference: pr}}},
		{name: "state only", state: ReportCheckpoint, note: "checkpoint"},
		{name: "state and outputs", state: ReportDone, note: "finished", outputs: []ReportOutput{{Kind: ReportOutputArtifact, Reference: "result"}}},
		{name: "empty", wantErr: true},
		{name: "message with output", message: "message", outputs: []ReportOutput{{Kind: ReportOutputArtifact, Reference: "result"}}, wantErr: true},
		{name: "note without state", note: "note", outputs: []ReportOutput{{Kind: ReportOutputArtifact, Reference: "result"}}, wantErr: true},
		{name: "state missing note", state: ReportDone, wantErr: true},
		{name: "unknown state", state: "working", note: "note", wantErr: true},
		{name: "long message", message: strings.Repeat("x", 1001), wantErr: true},
		{name: "long note", state: ReportStuck, note: strings.Repeat("x", 1001), wantErr: true},
		{name: "empty artifact", outputs: []ReportOutput{{Kind: ReportOutputArtifact}}, wantErr: true},
		{name: "invalid created PR", outputs: []ReportOutput{{Kind: ReportOutputPRCreated, Reference: "https://example.com/o/r/pull/1"}}, wantErr: true},
		{name: "invalid reviewed PR", outputs: []ReportOutput{{Kind: ReportOutputPRReviewed, Reference: "git://github.com/o/r/pull/1"}}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReportContent(tc.state, tc.note, tc.message, tc.outputs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestReportRecordValidatesSettlementDeadline(t *testing.T) {
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	rec := ReportRecord{ID: "rpt_1", SessionID: "worker", ProjectID: "ao", State: ReportDone, Note: "done", CreatedAt: now, DeliveryState: ReportPending, AvailableAt: now.Add(ReportSettlementWindow), SettlementDeadline: now.Add(ReportSettlementWindow), RepeatCount: 1}
	if err := rec.Validate(); err != nil {
		t.Fatal(err)
	}
	rec.SettlementDeadline = time.Time{}
	if err := rec.Validate(); err == nil {
		t.Fatal("expected missing settlement deadline to fail")
	}
}

func TestIsGitHubPullRequestURL(t *testing.T) {
	if !IsGitHubPullRequestURL("https://github.com/owner/repo/pull/42") || IsGitHubPullRequestURL("https://github.example/owner/repo/pull/42") {
		t.Fatal("PR URL validation mismatch")
	}
}
