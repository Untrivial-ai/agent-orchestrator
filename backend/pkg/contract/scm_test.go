package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func TestSCMContractJSONUsesProviderNeutralFields(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	value := contract.PullRequestSummary{
		URL:      "github://o/r/pull/7",
		Number:   7,
		State:    contract.PRStateOpen,
		Provider: "github",
		Repo:     "o/r",
		CI: contract.PullRequestCISummary{
			State:         contract.CIFailing,
			FailingChecks: []contract.PullRequestFailingCheck{},
		},
		Review: contract.PullRequestReviewSummary{
			Decision:     contract.ReviewChangesRequest,
			UnresolvedBy: []contract.PullRequestUnresolvedReviewer{},
			Reviews:      []contract.PullRequestSubmittedReview{},
		},
		Mergeability: contract.PullRequestMergeabilitySummary{
			State:         contract.MergeBlocked,
			Reasons:       []string{"ci_failing"},
			PRURL:         "https://github.com/o/r/pull/7",
			ConflictFiles: []contract.PullRequestConflictFile{},
		},
		UpdatedAt:        now,
		ObservedAt:       now,
		CIObservedAt:     now,
		ReviewObservedAt: now,
	}

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["repository"] != "o/r" {
		t.Fatalf("repository = %#v", got["repository"])
	}
	mergeability, ok := got["mergeability"].(map[string]any)
	if !ok || mergeability["pullRequestUrl"] != "https://github.com/o/r/pull/7" {
		t.Fatalf("mergeability = %#v", got["mergeability"])
	}
	if _, exists := got["repo"]; exists {
		t.Fatal("transport exposed local repo field name")
	}
}

func TestSharedSCMVocabulariesAndCloudReadRoutesMatch(t *testing.T) {
	repoRoot := scmRepoRoot(t)
	specData, err := os.ReadFile(filepath.Join(repoRoot, "contracts", "cloud", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths      map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(specData, &document); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/cloud/v1/orgs/{orgId}/sessions/{sessionId}/pull-requests",
		"/api/cloud/v1/orgs/{orgId}/sessions/{sessionId}/reviews",
	} {
		if _, ok := document.Paths[path]; !ok {
			t.Fatalf("Cloud schema has no session-scoped read path %s", path)
		}
	}

	productData, err := os.ReadFile(filepath.Join(repoRoot, "packages", "product-ui", "src", "scm-models.ts"))
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		schema       string
		productConst string
		want         []string
	}{
		{"PullRequestState", "PULL_REQUEST_STATES", []string{
			string(contract.PRStateDraft), string(contract.PRStateOpen),
			string(contract.PRStateMerged), string(contract.PRStateClosed),
		}},
		{"CIState", "CI_STATES", []string{
			string(contract.CIUnknown), string(contract.CIPending),
			string(contract.CIPassing), string(contract.CIFailing),
		}},
		{"PullRequestCheckStatus", "PULL_REQUEST_CHECK_STATUSES", []string{
			string(contract.PRCheckUnknown), string(contract.PRCheckQueued),
			string(contract.PRCheckInProgress), string(contract.PRCheckPassed),
			string(contract.PRCheckFailed), string(contract.PRCheckSkipped),
			string(contract.PRCheckCancelled),
		}},
		{"ReviewDecision", "REVIEW_DECISIONS", []string{
			string(contract.ReviewNone), string(contract.ReviewApproved),
			string(contract.ReviewChangesRequest), string(contract.ReviewRequired),
		}},
		{"MergeabilityState", "MERGEABILITY_STATES", []string{
			string(contract.MergeUnknown), string(contract.MergeMergeable),
			string(contract.MergeConflicting), string(contract.MergeBlocked),
			string(contract.MergeUnstable),
		}},
		{"AOReviewRunStatus", "AO_REVIEW_RUN_STATUSES", []string{
			string(contract.AOReviewRunRunning), string(contract.AOReviewRunComplete),
			string(contract.AOReviewRunDelivered), string(contract.AOReviewRunFailed),
			string(contract.AOReviewRunCancelled),
		}},
		{"AOReviewVerdict", "AO_REVIEW_VERDICTS", []string{
			string(contract.AOReviewVerdictNone), string(contract.AOReviewVerdictApproved),
			string(contract.AOReviewVerdictChangesRequested),
		}},
		{"AOReviewState", "AO_REVIEW_STATES", []string{
			string(contract.AOReviewNeedsReview), string(contract.AOReviewRunning),
			string(contract.AOReviewUpToDate), string(contract.AOReviewChangesRequested),
			string(contract.AOReviewIneligible),
		}},
	}

	for _, check := range checks {
		t.Run(check.schema, func(t *testing.T) {
			if got := document.Components.Schemas[check.schema].Enum; !slices.Equal(got, check.want) {
				t.Fatalf("Cloud %s = %q, want %q", check.schema, got, check.want)
			}
			if got := typescriptStringArray(t, productData, check.productConst); !slices.Equal(got, check.want) {
				t.Fatalf("product UI %s = %q, want %q", check.productConst, got, check.want)
			}
		})
	}
}

func scmRepoRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Join(filepath.Dir(currentFile), "..", "..", "..")
}

func typescriptStringArray(t *testing.T, data []byte, name string) []string {
	t.Helper()
	pattern := `(?s)` + regexp.QuoteMeta(name) + ` = \[(.*?)\] as const`
	block := regexp.MustCompile(pattern).FindSubmatch(data)
	if len(block) != 2 {
		t.Fatalf("product UI has no %s array", name)
	}
	matches := regexp.MustCompile(`"([^"]*)"`).FindAllSubmatch(block[1], -1)
	out := make([]string, len(matches))
	for i, match := range matches {
		out[i] = string(match[1])
	}
	return out
}
