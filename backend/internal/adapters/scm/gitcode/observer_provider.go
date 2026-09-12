package gitcode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	reviewCommentLimitPerThread = 5
	listPageSize                = 50
)

// ---------------------------------------------------------------------------
// ParseRepository
// ---------------------------------------------------------------------------

// ParseRepository parses a Git remote URL and returns an SCMRepo when the
// remote points at gitcode.com. Supported formats:
//
//   - HTTPS: https://gitcode.com/owner/repo.git
//   - SSH:   git@gitcode.com:owner/repo.git
//   - SSH:   ssh://git@gitcode.com[:port]/owner/repo.git
//
// Only the public gitcode.com host is accepted; GitCode has no self-managed
// offering, so no other host is ever trusted with credentials.
func (p *Provider) ParseRepository(remote string) (ports.SCMRepo, bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ports.SCMRepo{}, false
	}

	hostOf := func(h string) (ports.SCMRepo, bool) {
		if !IsGitCodeDotCom(h) {
			return ports.SCMRepo{}, false
		}
		return ports.SCMRepo{Provider: "gitcode", Host: NormalizeHost(h)}, true
	}

	if strings.HasPrefix(remote, "ssh://") {
		u, err := url.Parse(remote)
		if err != nil || u.Host == "" {
			return ports.SCMRepo{}, false
		}
		base, ok := hostOf(u.Host)
		if !ok {
			return ports.SCMRepo{}, false
		}
		return withOwnerRepo(base, strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"))
	}

	if m := sshRemoteRe.FindStringSubmatch(remote); m != nil {
		base, ok := hostOf(m[1])
		if !ok {
			return ports.SCMRepo{}, false
		}
		return withOwnerRepo(base, strings.TrimSuffix(m[2], ".git"))
	}

	if u, err := url.Parse(remote); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		base, ok := hostOf(u.Host)
		if !ok {
			return ports.SCMRepo{}, false
		}
		return withOwnerRepo(base, strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"))
	}

	return ports.SCMRepo{}, false
}

var sshRemoteRe = regexp.MustCompile(`^git@([^:]+):(.+)$`)

// withOwnerRepo splits an "owner/repo" path into the final SCMRepo. GitCode
// repos live in a single owner namespace (user or organization), so exactly
// two segments are accepted.
func withOwnerRepo(base ports.SCMRepo, path string) (ports.SCMRepo, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ports.SCMRepo{}, false
	}
	base.Owner = parts[0]
	base.Name = parts[1]
	base.Repo = parts[0] + "/" + parts[1]
	return base, true
}

// ---------------------------------------------------------------------------
// Guards
// ---------------------------------------------------------------------------

// RepoPRListGuard returns a synthetic ETag: the updated_at of the most
// recently updated PR. GitCode v5 sends no ETag/Link headers, so the newest
// update timestamp is the cheapest change signal for the repository listing.
func (p *Provider) RepoPRListGuard(ctx context.Context, repo ports.SCMRepo, etag string) (ports.SCMGuardResult, error) {
	q := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {"1"},
	}
	resp, err := p.client.doGET(ctx, pullsPath(repo), q)
	if err != nil {
		return ports.SCMGuardResult{}, err
	}
	elems, err := decodeArrayBody(resp.Body)
	if err != nil {
		return ports.SCMGuardResult{}, err
	}
	if len(elems) == 0 {
		// An empty repo is a stable "no PRs" state; keep the previous etag so
		// the observer does not promote a refresh on every poll.
		return ports.SCMGuardResult{ETag: etag}, nil
	}
	var newest restPR
	if err := unmarshalBody(elems[0], &newest); err != nil {
		return ports.SCMGuardResult{}, err
	}
	synthetic := "updated:" + newest.UpdatedAt.Format(time.RFC3339Nano)
	return ports.SCMGuardResult{
		ETag:        synthetic,
		NotModified: etag != "" && etag == synthetic,
	}, nil
}

// CommitChecksGuard is a no-op: GitCode v5 exposes no CI pipeline or commit
// status endpoints, so there is nothing to guard. The zero result (empty
// ETag) makes the observer skip CI-driven refresh promotion without error.
func (p *Provider) CommitChecksGuard(context.Context, ports.SCMRepo, string, string) (ports.SCMGuardResult, error) {
	return ports.SCMGuardResult{}, nil
}

// ---------------------------------------------------------------------------
// ListPRsByRepo
// ---------------------------------------------------------------------------

// ListPRsByRepo lists pull requests with state=all so merged/closed PRs are
// discovered for state-transition tracking, ordered by updated desc. When
// updatedAfter is non-zero, paging stops early once a page's newest PR is
// older than the cursor (the listing is sorted desc, so everything after it
// is older too).
func (p *Provider) ListPRsByRepo(ctx context.Context, repo ports.SCMRepo, updatedAfter time.Time) ([]ports.SCMPRObservation, error) {
	q := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"desc"},
	}
	var result []ports.SCMPRObservation
	err := p.client.doGETPaged(ctx, pullsPath(repo), q, listPageSize, func(elems []json.RawMessage) (bool, error) {
		for _, raw := range elems {
			var pr restPR
			if err := unmarshalBody(raw, &pr); err != nil {
				return false, err
			}
			if !updatedAfter.IsZero() && !pr.UpdatedAt.After(updatedAfter) {
				return true, nil // sorted desc: everything older follows
			}
			result = append(result, prToObservation(repo, &pr))
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// FetchPullRequests
// ---------------------------------------------------------------------------

// FetchPullRequests fetches each requested PR detail, returning observations
// positionally aligned with refs. A failed fetch leaves a Fetched=false
// placeholder carrying the error so the observer can classify it per PR.
func (p *Provider) FetchPullRequests(ctx context.Context, refs []ports.SCMPRRef) ([]ports.SCMObservation, error) {
	results := make([]ports.SCMObservation, len(refs))
	for i, ref := range refs {
		obs, err := p.fetchSinglePR(ctx, ref)
		if err != nil {
			results[i] = ports.SCMObservation{
				Fetched: false,
				Provider: "gitcode",
				Host:    ref.Repo.Host,
				Repo:    ref.Repo.Repo,
				PR:      ports.SCMPRObservation{Number: ref.Number, URL: ref.URL},
				Error:   err,
			}
			continue
		}
		results[i] = obs
	}
	return results, nil
}

func (p *Provider) fetchSinglePR(ctx context.Context, ref ports.SCMPRRef) (ports.SCMObservation, error) {
	repo := ref.Repo
	resp, err := p.client.doGET(ctx, fmt.Sprintf("%s/%d", pullsPath(repo), ref.Number), nil)
	if err != nil {
		return ports.SCMObservation{}, err
	}
	var pr restPR
	if err := unmarshalBody(resp.Body, &pr); err != nil {
		return ports.SCMObservation{}, err
	}

	prObs := prToObservation(repo, &pr)
	if requested := strings.TrimSpace(ref.URL); requested != "" && requested != strings.TrimSpace(prObs.URL) {
		prObs.URLAlias = requested
	}

	// GitCode v5 has no CI endpoints: CI is reported unknown so the observer
	// keeps the kanban CI column blank instead of fabricating a passing state.
	ci := ports.SCMCIObservation{Summary: string(domain.CIUnknown), HeadSHA: pr.Head.SHA}

	// Review decision is unknown: GitCode v5 has no review-summary endpoint.
	// Thread-level comments are fetched separately via FetchReviewThreads.
	return ports.SCMObservation{
		Fetched:      true,
		ObservedAt:   time.Now(),
		Provider:     "gitcode",
		Host:         repo.Host,
		Repo:         repo.Repo,
		PR:           prObs,
		CI:           ci,
		Review:       ports.SCMReviewObservation{Decision: string(domain.ReviewNone)},
		Mergeability: mergeabilityFromPR(&pr),
	}, nil
}

// FetchFailedCheckLogTail is unsupported: GitCode v5 has no CI endpoints, so
// no failing check can ever exist for this provider.
func (p *Provider) FetchFailedCheckLogTail(context.Context, ports.SCMRepo, ports.SCMCheckObservation) (string, error) {
	return "", nil
}

// ---------------------------------------------------------------------------
// FetchReviewThreads
// ---------------------------------------------------------------------------

// FetchReviewThreads derives review threads from PR comments grouped by
// discussion_id. GitCode v5 has no dedicated review or discussion endpoint;
// the flat comment list is the only thread-shaped surface, and discussion_id
// is the provider's grouping key for reply chains.
func (p *Provider) FetchReviewThreads(ctx context.Context, ref ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	repo := ref.Repo
	path := fmt.Sprintf("%s/%d/comments", pullsPath(repo), ref.Number)
	q := url.Values{}
	var comments []restComment
	err := p.client.doGETPaged(ctx, path, q, listPageSize, func(elems []json.RawMessage) (bool, error) {
		for _, raw := range elems {
			var c restComment
			if err := unmarshalBody(raw, &c); err != nil {
				return false, err
			}
			comments = append(comments, c)
		}
		return false, nil
	})
	if err != nil {
		return ports.SCMReviewObservation{}, err
	}

	byDiscussion := make(map[string][]restComment)
	var order []string
	for _, c := range comments {
		if _, seen := byDiscussion[c.DiscussionID]; !seen {
			order = append(order, c.DiscussionID)
		}
		byDiscussion[c.DiscussionID] = append(byDiscussion[c.DiscussionID], c)
	}

	threads := make([]ports.SCMReviewThreadObservation, 0, len(order))
	for _, id := range order {
		group := byDiscussion[id]
		allBot := true
		var normalized []ports.SCMReviewCommentObservation
		for j, c := range group {
			isBot := isBotAuthor(c.User.Login)
			if !isBot {
				allBot = false
			}
			if j < reviewCommentLimitPerThread {
				normalized = append(normalized, ports.SCMReviewCommentObservation{
					ID:     strconv.FormatInt(c.ID, 10),
					Author: c.User.Login,
					Body:   c.Body,
					URL:    commentURL(ref, c),
					IsBot:  isBot,
				})
			}
		}
		if len(normalized) == 0 {
			continue
		}
		threads = append(threads, ports.SCMReviewThreadObservation{
			ID:       id,
			Resolved: false, // GitCode v5 exposes no thread-resolved state
			IsBot:    allBot,
			Comments: normalized,
		})
	}

	return ports.SCMReviewObservation{
		Decision: string(domain.ReviewNone),
		Threads:  threads,
	}, nil
}

func commentURL(ref ports.SCMPRRef, c restComment) string {
	if ref.URL != "" {
		return ref.URL + "#comment_" + strconv.FormatInt(c.ID, 10)
	}
	return ""
}

// ---------------------------------------------------------------------------
// REST payload types
// ---------------------------------------------------------------------------

func pullsPath(repo ports.SCMRepo) string {
	return "/repos/" + repo.Owner + "/" + repo.Name + "/pulls"
}

// restPR is the subset of the GitCode v5 pull-request payload the adapter
// consumes. Times arrive as RFC3339 strings with a +08:00 offset, or as empty
// strings when absent, hence flexTime.
type restPR struct {
	ID      int64  `json:"id"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	HTMLURL string `json:"html_url"`

	User struct {
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	} `json:"user"`

	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`

	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`

	Mergeable      *bool  `json:"mergeable"`
	MergeCommitSHA string `json:"merge_commit_sha"`

	CreatedAt flexTime `json:"created_at"`
	UpdatedAt flexTime `json:"updated_at"`
	MergedAt  flexTime `json:"merged_at"`
	ClosedAt  flexTime `json:"closed_at"`
}

// restComment is one GitCode v5 PR comment. Comments belonging to the same
// reply chain share a discussion_id.
type restComment struct {
	ID           int64    `json:"id"`
	DiscussionID string   `json:"discussion_id"`
	Body         string   `json:"body"`
	CommentType  string   `json:"comment_type"`
	CreatedAt    flexTime `json:"created_at"`
	User         struct {
		Login string `json:"login"`
	} `json:"user"`
}

// flexTime unmarshals GitCode timestamps: RFC3339 strings (with +08:00
// offset), empty strings, or null — all without error. Absent values decode
// to the zero time.Time.
type flexTime struct{ time.Time }

func (t *flexTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		t.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Time = time.Time{}
		return nil
	}
	t.Time = parsed
	return nil
}

// prToObservation maps a GitCode PR payload onto the provider-neutral
// observation. GitCode states are open/merged/closed.
func prToObservation(repo ports.SCMRepo, pr *restPR) ports.SCMPRObservation {
	state := domain.PRStateClosed
	switch pr.State {
	case "open":
		if pr.Draft {
			state = domain.PRStateDraft
		} else {
			state = domain.PRStateOpen
		}
	case "merged":
		state = domain.PRStateMerged
	}

	author := pr.User.Login
	if author == "" {
		author = pr.User.Name
	}

	headRepo := repo.Repo
	if pr.Head.Repo != nil && pr.Head.Repo.FullName != "" {
		headRepo = pr.Head.Repo.FullName
	}

	providerMergeable := ""
	if pr.Mergeable != nil {
		providerMergeable = strconv.FormatBool(*pr.Mergeable)
	}

	return ports.SCMPRObservation{
		ProviderID:          strconv.FormatInt(pr.ID, 10),
		URL:                pr.HTMLURL,
		HTMLURL:            pr.HTMLURL,
		Number:             pr.Number,
		State:              string(state),
		Draft:              pr.Draft,
		Merged:             pr.State == "merged",
		Closed:             pr.State == "closed",
		SourceBranch:       pr.Head.Ref,
		TargetBranch:       pr.Base.Ref,
		HeadRepo:           headRepo,
		HeadSHA:            pr.Head.SHA,
		BaseSHA:            pr.Base.SHA,
		MergeCommitSHA:     pr.MergeCommitSHA,
		Title:              pr.Title,
		Author:             author,
		AuthorAvatarURL:    pr.User.AvatarURL,
		ProviderState:      pr.State,
		ProviderMergeable:  providerMergeable,
		CreatedAtProvider:  pr.CreatedAt.Time,
		UpdatedAtProvider: pr.UpdatedAt.Time,
		MergedAtProvider:  pr.MergedAt.Time,
		ClosedAtProvider:  pr.ClosedAt.Time,
	}
}

// mergeabilityFromPR derives the normalized mergeability verdict from
// GitCode's boolean mergeable field. The provider does not explain why a PR
// is not mergeable, so a false value maps to MergeBlocked with a generic
// blocker rather than claiming a conflict. Drafts are always blocked.
func mergeabilityFromPR(pr *restPR) ports.SCMMergeabilityObservation {
	if pr.Draft {
		return ports.SCMMergeabilityObservation{State: string(domain.MergeBlocked), Blockers: []string{"draft"}}
	}
	if pr.Mergeable == nil {
		return ports.SCMMergeabilityObservation{State: string(domain.MergeUnknown)}
	}
	if *pr.Mergeable {
		return ports.SCMMergeabilityObservation{State: string(domain.MergeMergeable), Mergeable: true}
	}
	return ports.SCMMergeabilityObservation{State: string(domain.MergeBlocked), Mergeable: false, Blockers: []string{"not_mergeable"}}
}

// isBotAuthor reports whether a GitCode account is an automation actor.
// gitcode-bot is GitCode's own CI/review bot; the suffix and name matches
// cover the usual third-party suspects.
func isBotAuthor(username string) bool {
	lower := strings.ToLower(username)
	if strings.HasSuffix(lower, "[bot]") || strings.HasSuffix(lower, "-bot") || strings.HasSuffix(lower, "_bot") {
		return true
	}
	switch lower {
	case "gitcode-bot", "gitcodebot", "ghost", "dependabot[bot]", "renovate[bot]":
		return true
	}
	return false
}

// unmarshalBody decodes a JSON payload into v with a provider-prefixed error.
func unmarshalBody(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("gitcode scm: unmarshal response: %w", err)
	}
	return nil
}
