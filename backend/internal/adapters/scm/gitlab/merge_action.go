package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var mergeHeadSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

var _ ports.SCMMerger = (*Provider)(nil)

// MergePullRequest performs a GitLab squash merge guarded by the reviewed head
// SHA. GitLab's sha query parameter is a compare-and-swap precondition: if the
// live head has advanced, GitLab responds 409 and the merge is rejected.
func (p *Provider) MergePullRequest(ctx context.Context, request ports.SCMMergeRequest) (ports.SCMMergeResult, error) {
	if p == nil || p.client == nil {
		return ports.SCMMergeResult{}, fmt.Errorf("gitlab scm: merge provider is not configured")
	}
	if request.PR.Number <= 0 {
		return ports.SCMMergeResult{}, fmt.Errorf("gitlab scm: invalid merge request number %d", request.PR.Number)
	}
	if strings.TrimSpace(request.PR.Repo.Owner) == "" || strings.TrimSpace(request.PR.Repo.Name) == "" {
		return ports.SCMMergeResult{}, fmt.Errorf("gitlab scm: invalid repository owner/name")
	}
	if request.Method != ports.SCMMergeSquash {
		return ports.SCMMergeResult{}, fmt.Errorf("gitlab scm: unsupported merge method %q", request.Method)
	}
	expectedHead := strings.TrimSpace(request.ExpectedHeadSHA)
	if !mergeHeadSHAPattern.MatchString(expectedHead) {
		return ports.SCMMergeResult{}, fmt.Errorf("gitlab scm: invalid expected head sha")
	}

	client, err := p.clientForRepoErr(request.PR.Repo)
	if err != nil {
		return ports.SCMMergeResult{}, err
	}

	path := fmt.Sprintf("/projects/%s/merge_requests/%d/merge",
		projectPath(request.PR.Repo.Owner, request.PR.Repo.Name), request.PR.Number)
	q := url.Values{}
	q.Set("sha", expectedHead)
	q.Set("squash", "true")

	resp, err := client.doMERGE(ctx, path, q, nil)
	if err != nil {
		switch resp.StatusCode {
		case http.StatusNotFound:
			return ports.SCMMergeResult{}, fmt.Errorf("%w: %w", ports.ErrSCMNotFound, err)
		case http.StatusConflict:
			return ports.SCMMergeResult{}, fmt.Errorf("%w: %w", ports.ErrSCMHeadChanged, err)
		case http.StatusMethodNotAllowed, http.StatusNotAcceptable:
			return ports.SCMMergeResult{}, fmt.Errorf("%w: %w", ports.ErrSCMNotMergeable, err)
		default:
			return ports.SCMMergeResult{}, err
		}
	}

	var result struct {
		MergeCommitSHA string `json:"merge_commit_sha"`
		SHA            string `json:"sha"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return ports.SCMMergeResult{}, fmt.Errorf("gitlab scm: decode merge response: %w", err)
	}
	// GitLab returns merge_commit_sha on success. Fall back to sha for
	// self-managed instances that return a different field name.
	mergeSHA := result.MergeCommitSHA
	if mergeSHA == "" {
		mergeSHA = result.SHA
	}
	if mergeSHA == "" {
		return ports.SCMMergeResult{}, fmt.Errorf("gitlab scm: merge response missing commit sha")
	}
	return ports.SCMMergeResult{MergeCommitSHA: mergeSHA}, nil
}
