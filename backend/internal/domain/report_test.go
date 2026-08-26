package domain

import (
	"strings"
	"testing"
)

func TestValidateReportNote(t *testing.T) {
	for _, typ := range []ReportType{ReportFreeForm, ReportPRCreated, ReportArtifact, ReportCheckpoint, ReportNeedsInput, ReportStuck, ReportDone} {
		if !typ.Valid() {
			t.Errorf("%s invalid", typ)
		}
	}
	if err := ValidateReportNote(ReportFreeForm, strings.Repeat("界", 1000)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReportNote(ReportFreeForm, strings.Repeat("界", 1001)); err == nil {
		t.Fatal("expected length error")
	}
	if !IsGitHubPullRequestURL("https://github.com/owner/repo/pull/42") || IsGitHubPullRequestURL("https://github.example/owner/repo/pull/42") {
		t.Fatal("PR URL validation mismatch")
	}
}
