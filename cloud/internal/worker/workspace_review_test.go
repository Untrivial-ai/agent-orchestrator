package worker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceReviewProtocolJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 20, 10, 30, 0, 0, time.UTC)
	response := WorkspaceReviewResponse{
		WorkspaceVersion: "version-1",
		CompareBaseSHA:   strings.Repeat("a", 40),
		CompareBaseRef:   "refs/ao/diff-base",
		CompareMode:      "base",
		Files: []WorkspaceReviewFileSummary{{
			Path: "src/app.ts", PreviousPath: "src/old.ts", Status: WorkspaceReviewRenamed,
			Additions: 3, Deletions: 1, Size: 42, Editable: true, FileFingerprint: "fp-1",
		}},
		Sections: WorkspaceReviewSections{
			Staged:    []WorkspaceReviewFileSummary{},
			Unstaged:  []WorkspaceReviewFileSummary{},
			Untracked: []WorkspaceReviewFileSummary{},
			Committed: []WorkspaceReviewFileSummary{},
		},
		Commits: []WorkspaceReviewCommit{{
			SHA: strings.Repeat("b", 40), Subject: "change", Author: "AO", Timestamp: now,
			Files: []WorkspaceReviewFileSummary{},
		}},
		Summary: WorkspaceReviewSummary{Files: 1, Additions: 3, Deletions: 1},
		Ahead:   intPointer(1), Behind: intPointer(0),
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"workspaceVersion":"version-1"`, `"compareBaseSha":"` + strings.Repeat("a", 40) + `"`,
		`"previousPath":"src/old.ts"`, `"fileFingerprint":"fp-1"`,
		`"staged":[]`, `"unstaged":[]`, `"untracked":[]`, `"committed":[]`,
		`"commitSha"`, // forbidden below: guards accidental field-name overlap
	} {
		if field == `"commitSha"` {
			if strings.Contains(string(encoded), field) {
				t.Fatalf("summary unexpectedly contains %s: %s", field, encoded)
			}
			continue
		}
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("JSON missing %s: %s", field, encoded)
		}
	}

	var decoded WorkspaceReviewResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Files[0].Status != WorkspaceReviewRenamed || decoded.Commits[0].Timestamp != now {
		t.Fatalf("round trip mismatch: %#v", decoded)
	}
}

func TestWorkspaceReviewRequestContracts(t *testing.T) {
	commit := strings.Repeat("c", 40)
	cases := []struct {
		name  string
		value any
		want  []string
	}{
		{"file", WorkspaceReviewFileRequest{Path: "a.txt", Scope: WorkspaceReviewStaged, CommitSHA: commit}, []string{`"path":"a.txt"`, `"scope":"staged"`, `"commitSha":"` + commit + `"`}},
		{"diffs", WorkspaceReviewDiffsRequest{Scope: WorkspaceReviewCombined, Paths: []string{"a.txt"}, ContextLines: 7, IgnoreWhitespace: true, WorkspaceVersion: "v1"}, []string{`"scope":"combined"`, `"paths":["a.txt"]`, `"contextLines":7`, `"ignoreWhitespace":true`, `"workspaceVersion":"v1"`}},
		{"revision", WorkspaceReviewRevisionRequest{Path: "a.txt", Scope: WorkspaceReviewCommitted, Side: WorkspaceReviewBefore, WorkspaceVersion: "v1", ExpectedRevision: "r1", CommitSHA: commit}, []string{`"side":"before"`, `"expectedRevision":"r1"`}},
		{"tree", WorkspaceReviewTreeRequest{Path: "src"}, []string{`"path":"src"`}},
		{"search", WorkspaceReviewSearchRequest{Query: "app", Cursor: "next", Limit: 25}, []string{`"query":"app"`, `"cursor":"next"`, `"limit":25`}},
		{"write", WorkspaceReviewWriteRequest{Path: "a.txt", Content: "new", ExpectedFileFingerprint: "fp"}, []string{`"content":"new"`, `"expectedFileFingerprint":"fp"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(encoded), want) {
					t.Fatalf("JSON missing %s: %s", want, encoded)
				}
			}
		})
	}
}

func TestWorkspaceReviewScopesAreStable(t *testing.T) {
	want := []WorkspaceReviewScope{
		WorkspaceReviewCombined,
		WorkspaceReviewCommitted,
		WorkspaceReviewStaged,
		WorkspaceReviewUnstaged,
		WorkspaceReviewUntracked,
	}
	if got := strings.Join(scopesToStrings(want), ","); got != "combined,committed,staged,unstaged,untracked" {
		t.Fatalf("scopes = %q", got)
	}
}

func scopesToStrings(scopes []WorkspaceReviewScope) []string {
	result := make([]string, len(scopes))
	for i, scope := range scopes {
		result[i] = string(scope)
	}
	return result
}

func intPointer(value int) *int { return &value }
