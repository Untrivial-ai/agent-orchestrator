package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const billingAnnotation = "The job was not started because recent account payments have failed or your spending limit needs to be increased"

func billingBlockedCheck() map[string]any {
	return map[string]any{
		"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "FAILURE",
		"databaseId": float64(9001), "detailsUrl": "https://github.com/octocat/hello/actions/runs/1/job/9001",
		"steps":       map[string]any{"totalCount": float64(0)},
		"checkSuite":  map[string]any{"app": map[string]any{"slug": "github-actions"}},
		"annotations": map[string]any{"nodes": []any{map[string]any{"message": billingAnnotation}}},
	}
}

func checkFixture(checks ...any) map[string]any {
	fx := basePRFixture()
	var pr map[string]any
	fx.prData(func(m map[string]any) { pr = m })
	statusRollup(pr)["state"] = "FAILURE"
	statusContexts(pr)["nodes"] = checks
	return pr
}

func TestCheckStatusBillingEvidence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(map[string]any)
		want   domain.PRCheckStatus
	}{
		{"billing refusal", func(map[string]any) {}, domain.PRCheckUnknown},
		{"missing steps with explicit refusal", func(n map[string]any) { delete(n, "steps") }, domain.PRCheckUnknown},
		{"zero steps without annotation", func(n map[string]any) { delete(n, "annotations") }, domain.PRCheckFailed},
		{"third party", func(n map[string]any) {
			n["checkSuite"] = map[string]any{"app": map[string]any{"slug": "external-check"}}
		}, domain.PRCheckFailed},
		{"missing app", func(n map[string]any) { delete(n, "checkSuite") }, domain.PRCheckFailed},
		{"real steps", func(n map[string]any) { n["steps"] = map[string]any{"totalCount": float64(4)} }, domain.PRCheckFailed},
		{"invalid workflow", func(n map[string]any) {
			n["annotations"] = map[string]any{"nodes": []any{map[string]any{"message": "Invalid workflow file: unknown action"}}}
		}, domain.PRCheckFailed},
		{"unrelated billing text", func(n map[string]any) {
			n["annotations"] = map[string]any{"nodes": []any{map[string]any{"message": "billing test failed: spending limit needs to be increased"}}}
		}, domain.PRCheckFailed},
		{"startup failure", func(n map[string]any) { n["conclusion"] = "STARTUP_FAILURE" }, domain.PRCheckFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n := billingBlockedCheck()
			tt.mutate(n)
			if got := checkStatusFromGraphQL(n); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSCMObservationBillingAndMixedChecks(t *testing.T) {
	for _, tt := range []struct {
		name   string
		checks []any
		want   domain.CIState
	}{
		{"blocked", []any{billingBlockedCheck()}, domain.CIUnknown},
		{"blocked and passing", []any{billingBlockedCheck(), map[string]any{"__typename": "CheckRun", "name": "lint", "conclusion": "SUCCESS"}}, domain.CIUnknown},
		{"unknown and passing", []any{map[string]any{"__typename": "CheckRun", "name": "build", "status": "COMPLETED"}, map[string]any{"__typename": "CheckRun", "name": "lint", "conclusion": "SUCCESS"}}, domain.CIUnknown},
		{"blocked and failed", []any{billingBlockedCheck(), map[string]any{"__typename": "CheckRun", "name": "test", "conclusion": "FAILURE"}}, domain.CIFailing},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pr := checkFixture(tt.checks...)
			obs := scmObservationFromGraphQL(ports.SCMPRRef{}, pr)
			if got := obs.CI.Summary; got != string(tt.want) {
				t.Fatalf("observer summary = %q, want %q", got, tt.want)
			}
			if got := ciSummaryFromGraphQL(pr); got != tt.want {
				t.Fatalf("single summary = %q, want %q", got, tt.want)
			}
			if tt.name == "blocked" {
				ch := obs.CI.Checks[0]
				if ch.Conclusion != "failure" || ch.Status != string(domain.PRCheckUnknown) || !strings.Contains(ch.LogTail, "billing") || len(obs.CI.FailedChecks) != 0 {
					t.Fatalf("blocking evidence lost or actionable: %#v", obs.CI)
				}
			}
		})
	}
}

func TestCheckQueriesRequestBillingEvidence(t *testing.T) {
	batch, _ := buildSCMBatchQuery([]ports.SCMPRRef{{Number: 42}})
	for name, query := range map[string]string{"single": prObservationQuery, "batch": batch, "page": buildCheckContextsQuery(ports.SCMPRRef{}, "next")} {
		for _, field := range []string{"steps(first:1)", "annotations(first:5)", "checkSuite{ app{ slug } }", "nodes{ message }"} {
			if !strings.Contains(query, field) {
				t.Errorf("%s query missing %s", name, field)
			}
		}
	}
}

func TestFetchPullRequestsBillingOnEveryPage(t *testing.T) {
	for _, paginated := range []bool{false, true} {
		name := "batch"
		if paginated {
			name = "later page"
		}
		t.Run(name, func(t *testing.T) {
			fake := newFakeGH(t)
			first := checkFixture(billingBlockedCheck())
			if paginated {
				first = checkFixture(map[string]any{"__typename": "CheckRun", "name": "lint", "conclusion": "SUCCESS"})
				statusContexts(first)["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "next"}
			}
			fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
				pr, alias := first, "pr0"
				if fake.callsTo(http.MethodPost, "/graphql") == 2 {
					pr, alias = checkFixture(billingBlockedCheck()), "repo"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{alias: map[string]any{"pullRequest": pr}}})
			})
			obs, err := newProviderForTest(t, fake).FetchPullRequests(ctx(), []ports.SCMPRRef{{Repo: ports.SCMRepo{Provider: "github", Owner: "octocat", Name: "hello"}, Number: 42}})
			if err != nil {
				t.Fatal(err)
			}
			if len(obs) != 1 || obs[0].CI.Summary != string(domain.CIUnknown) || len(obs[0].CI.FailedChecks) != 0 {
				t.Fatalf("observations = %#v", obs)
			}
		})
	}
}

func TestFailedPollingFingerprintRetainsAttemptIdentity(t *testing.T) {
	first := []ports.SCMCheckObservation{{Name: "build", Status: "failed", Conclusion: "failure", URL: "run/1", ProviderID: "1"}}
	next := []ports.SCMCheckObservation{{Name: "build", Status: "failed", Conclusion: "failure", URL: "run/2", ProviderID: "2"}}
	if githubFailedFingerprint("sha", first) == githubFailedFingerprint("sha", next) {
		t.Fatal("polling must still fetch the new attempt's logs")
	}
}

func TestObserveBillingDoesNotFetchMissingLog(t *testing.T) {
	fake := newFakeGH(t)
	fx := basePRFixture()
	fx.prData(func(pr map[string]any) {
		statusRollup(pr)["state"] = "FAILURE"
		statusContexts(pr)["nodes"] = []any{billingBlockedCheck()}
	})
	fx.install(t, fake)
	obs, err := newProviderForTest(t, fake).Observe(ctx(), "https://github.com/octocat/hello/pull/42")
	if err != nil {
		t.Fatal(err)
	}
	if obs.CI != domain.CIUnknown || len(obs.Checks) != 1 || !strings.Contains(obs.Checks[0].LogTail, "billing") {
		t.Fatalf("observation lost blocking evidence: %#v", obs)
	}
	if len(fake.calls()) != 2 {
		t.Fatalf("billing refusal should only fetch PR and GraphQL: %#v", fake.calls())
	}
}

func TestFetchPullRequestsRejectsIncompleteCheckPages(t *testing.T) {
	for _, reason := range []string{"repeated cursor", "too many pages", "changed head"} {
		t.Run(reason, func(t *testing.T) {
			fake := newFakeGH(t)
			fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
				call := fake.callsTo(http.MethodPost, "/graphql")
				pr := checkFixture(billingBlockedCheck())
				cursor := "next"
				if reason == "too many pages" {
					cursor = fmt.Sprintf("next-%d", call)
				}
				statusContexts(pr)["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": cursor}
				alias := "pr0"
				if call > 1 {
					alias = "repo"
					if reason == "changed head" {
						commits := pr["commits"].(map[string]any)
						nodes(commits["nodes"])[0]["commit"].(map[string]any)["oid"] = "new-head"
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{alias: map[string]any{"pullRequest": pr}}})
			})
			obs, err := newProviderForTest(t, fake).FetchPullRequests(ctx(), []ports.SCMPRRef{{Number: 42}})
			if err == nil || len(obs) != 0 {
				t.Fatalf("incomplete fetch produced observations: %#v, %v", obs, err)
			}
			if calls := fake.callsTo(http.MethodPost, "/graphql"); calls > githubCheckRunsMaxPages {
				t.Fatalf("unbounded pagination: %d calls", calls)
			}
		})
	}
}
