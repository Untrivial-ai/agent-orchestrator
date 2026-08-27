package onedev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// fetchConcurrency bounds the parallel per-PR detail fetches inside one
	// FetchPullRequests batch, matching the GitLab adapter.
	fetchConcurrency = 5
	// batchLimit is the largest ref batch FetchPullRequests accepts. It
	// mirrors the observer's own BatchSize so an oversized batch is a
	// programming error, not a silent partial fetch.
	batchLimit = 25
	// prListPageSize is the per-page count for PR listings. The client clamps
	// to OneDev's ceiling, above which the server answers HTTP 406.
	prListPageSize = 100
	// prBuildsPageCount bounds how many of a pull request's builds are
	// considered when assembling its CI snapshot. Only the builds matching the
	// PR's current build commit survive the filter, and OneDev submits one
	// build per job per commit, so this is generous even for a PR that has
	// been retried repeatedly.
	prBuildsPageCount = 50
	// checksGuardWindow is how many of a project's most recent builds
	// CommitChecksGuard hashes. See that method for why the window exists and
	// what it bounds.
	checksGuardWindow = 20
)

// ---------------------------------------------------------------------------
// Query construction
// ---------------------------------------------------------------------------

// quoteQueryValue renders a value for OneDev's query DSL, which quotes values
// with double quotes and escapes with a backslash. Project paths and branch
// names do not normally contain either, but a value is interpolated into a
// query the server parses, so it is escaped rather than trusted.
func quoteQueryValue(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// queryDate formats a timestamp for OneDev's date criteria. OneDev parses the
// value in UTC whether or not an offset is present, so the timestamp is always
// converted to UTC first: sending a local-zone wall-clock time would shift the
// cursor by the offset and silently drop updates inside that window.
func queryDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// projectRef renders a project path for a query. OneDev identifies a project
// by its full path ("Homelab/curatarr"), which is exactly SCMRepo.Repo for
// this adapter — see ParseRepository.
func projectRef(repo ports.SCMRepo) string { return repo.Repo }

// pullRequestRef renders the project-qualified pull-request reference OneDev's
// build query requires, e.g. "productone#106". A bare number is rejected with
// "Reference project not specified" even when the surrounding query already
// constrains the project, so the project path is always included.
func pullRequestRef(repo ports.SCMRepo, number int) string {
	return projectRef(repo) + "#" + strconv.Itoa(number)
}

// ---------------------------------------------------------------------------
// REST payloads
// ---------------------------------------------------------------------------

// restPullRequest is the subset of OneDev's pull-request payload AO consumes.
// Note that "id" (the internal request id every /pulls/{requestId} route is
// keyed by) is distinct from "number" (the per-project number users see).
type restPullRequest struct {
	ID              int64             `json:"id"`
	Number          int               `json:"number"`
	Status          string            `json:"status"`
	Title           string            `json:"title"`
	TargetBranch    string            `json:"targetBranch"`
	SourceBranch    string            `json:"sourceBranch"`
	BaseCommitHash  string            `json:"baseCommitHash"`
	BuildCommitHash string            `json:"buildCommitHash"`
	MergeStrategy   string            `json:"mergeStrategy"`
	CheckError      string            `json:"checkError"`
	SubmitDate      *time.Time        `json:"submitDate"`
	CloseDate       *time.Time        `json:"closeDate"`
	LastActivity    *restLastActivity `json:"lastActivity"`
	SubmitterID     int64             `json:"submitterId"`
	TargetProjectID int64             `json:"targetProjectId"`
	SourceProjectID int64             `json:"sourceProjectId"`
}

type restLastActivity struct {
	Date        *time.Time `json:"date"`
	Description string     `json:"description"`
	UserID      int64      `json:"userId"`
}

// restMergePreview is OneDev's precomputed merge of a request into its target.
// A null mergeCommitHash means the merge conflicts; an absent preview means
// OneDev has not computed one yet.
type restMergePreview struct {
	TargetHeadCommitHash string `json:"targetHeadCommitHash"`
	HeadCommitHash       string `json:"headCommitHash"`
	MergeStrategy        string `json:"mergeStrategy"`
	MergeCommitHash      string `json:"mergeCommitHash"`
}

type restPullRequestUpdate struct {
	ID                   int64      `json:"id"`
	HeadCommitHash       string     `json:"headCommitHash"`
	TargetHeadCommitHash string     `json:"targetHeadCommitHash"`
	Date                 *time.Time `json:"date"`
}

type restBuild struct {
	ID         int64      `json:"id"`
	Number     int        `json:"number"`
	JobName    string     `json:"jobName"`
	Status     string     `json:"status"`
	CommitHash string     `json:"commitHash"`
	RefName    string     `json:"refName"`
	SubmitDate *time.Time `json:"submitDate"`
	FinishDate *time.Time `json:"finishDate"`
	ProjectID  int64      `json:"projectId"`
	RequestID  *int64     `json:"requestId"`
}

// restReview is one reviewer's verdict. status is OneDev's
// PullRequestReview.Status enum: PENDING, APPROVED, REQUESTED_FOR_CHANGES or
// EXCLUDED.
type restReview struct {
	ID         int64      `json:"id"`
	Status     string     `json:"status"`
	StatusDate *time.Time `json:"statusDate"`
	UserID     int64      `json:"userId"`
	RequestID  int64      `json:"requestId"`
}

type restPullRequestComment struct {
	ID        int64      `json:"id"`
	Date      *time.Time `json:"date"`
	Content   string     `json:"content"`
	UserID    int64      `json:"userId"`
	RequestID int64      `json:"requestId"`
}

type restUser struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"fullName"`
	Disabled bool   `json:"disabled"`
	Type     string `json:"type"`
}

type restProject struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// Host and URL helpers
// ---------------------------------------------------------------------------

// hostForRepo resolves a repository's allowlist entry, so a caller can build
// browser URLs against the instance AO is actually configured to talk to
// rather than the authority written in a git remote.
func (p *Provider) hostForRepo(repo ports.SCMRepo) (allowedHost, error) {
	h, ok := p.resolveHost(repo.Host)
	if !ok {
		return allowedHost{}, fmt.Errorf("onedev scm: host %q: %w", repo.Host, ErrHostNotAllowed)
	}
	return h, nil
}

// webURL builds a browser URL for one of OneDev's per-project views. OneDev
// reserves the "~" prefix for its own routes, so a project path can never
// shadow "~pulls" or "~builds".
func webURL(h allowedHost, projectPath, view string, number int) string {
	return h.scheme + "://" + h.authority + "/" + projectPath + "/~" + view + "/" + strconv.Itoa(number)
}

// ---------------------------------------------------------------------------
// ListPRsByRepo
// ---------------------------------------------------------------------------

// ListPRsByRepo lists a project's pull requests, optionally narrowed to those
// touched since updatedAfter (a zero time lists everything).
//
// The listing is not filtered to open requests: the observer relies on seeing
// merged and discarded requests so a tracked PR's terminal transition is
// observed rather than inferred from its disappearance.
//
// Three details of OneDev's query language are load-bearing here and are
// pinned by TestListPRsByRepoQuery:
//
//   - The date operator is "since". "is after" is rejected with HTTP 406
//     "Invalid query" — OneDev spells the exclusive-lower-bound operator
//     differently from every other provider AO talks to.
//   - The project criterion is "Target Project", matched on the project's full
//     path.
//   - Results are ordered by last activity descending, which is also what
//     makes RepoPRListGuard's synthesised token sound.
func (p *Provider) ListPRsByRepo(ctx context.Context, repo ports.SCMRepo, updatedAfter time.Time) ([]ports.SCMPRObservation, error) {
	client, err := p.clientForRepo(repo)
	if err != nil {
		return nil, err
	}
	host, err := p.hostForRepo(repo)
	if err != nil {
		return nil, err
	}

	criteria := []string{`"Target Project" is ` + quoteQueryValue(projectRef(repo))}
	if !updatedAfter.IsZero() {
		criteria = append(criteria, `"Last Activity Date" is since `+quoteQueryValue(queryDate(updatedAfter)))
	}
	q := url.Values{
		"query": {strings.Join(criteria, " and ") + ` order by "Last Activity Date" desc`},
		"count": {strconv.Itoa(prListPageSize)},
	}

	var result []ports.SCMPRObservation
	_, err = client.doGETPaginated(ctx, "/pulls", q, func(body []byte) (int, error) {
		var page []restPullRequest
		if err := json.Unmarshal(body, &page); err != nil {
			return 0, fmt.Errorf("onedev scm: unmarshal pull request list: %w", err)
		}
		for i := range page {
			pr := &page[i]
			p.cache.setRequestID(repo.Host, repo.Repo, pr.Number, pr.ID)
			result = append(result, p.prObservation(ctx, client, host, repo, pr))
		}
		return len(page), nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// prObservation normalizes one OneDev pull request.
//
// Author and head-repository resolution are best-effort: both need a second
// lookup (user id to login, project id to path) and neither is worth failing a
// whole listing over. A missing author simply leaves the observer's
// author-match check without an opinion, which falls back to branch-based
// attribution.
func (p *Provider) prObservation(ctx context.Context, client *Client, host allowedHost, repo ports.SCMRepo, pr *restPullRequest) ports.SCMPRObservation {
	state, merged, closed := normalizePRStatus(pr.Status)
	prURL := webURL(host, repo.Repo, "pulls", pr.Number)

	obs := ports.SCMPRObservation{
		ProviderID:        strconv.FormatInt(pr.ID, 10),
		URL:               prURL,
		HTMLURL:           prURL,
		Number:            pr.Number,
		State:             string(state),
		Merged:            merged,
		Closed:            closed,
		SourceBranch:      pr.SourceBranch,
		TargetBranch:      pr.TargetBranch,
		HeadRepo:          repo.Repo,
		Title:             pr.Title,
		BaseSHA:           pr.BaseCommitHash,
		Author:            p.resolveUserLogin(ctx, client, repo.Host, pr.SubmitterID),
		ProviderState:     pr.Status,
		CreatedAtProvider: safeTime(pr.SubmitDate),
	}
	if pr.LastActivity != nil {
		obs.UpdatedAtProvider = safeTime(pr.LastActivity.Date)
	}
	if merged {
		obs.MergedAtProvider = safeTime(pr.CloseDate)
	}
	if closed {
		obs.ClosedAtProvider = safeTime(pr.CloseDate)
	}
	// OneDev pull requests may be raised from a different project in the tree
	// (its equivalent of a fork). The head repository decides which sessions
	// may claim the PR by branch prefix, so it must name the project the
	// source branch actually lives in.
	if pr.SourceProjectID != 0 && pr.SourceProjectID != pr.TargetProjectID {
		if path := p.resolveProjectPath(ctx, client, repo.Host, pr.SourceProjectID); path != "" {
			obs.HeadRepo = path
		}
	}
	return obs
}

// normalizePRStatus maps OneDev's PullRequest.Status enum (OPEN, MERGED,
// DISCARDED) onto AO's normalized PR state. OneDev has no draft concept, so
// no request ever normalizes to "draft".
func normalizePRStatus(status string) (state domain.PRState, merged, closed bool) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "OPEN":
		return domain.PRStateOpen, false, false
	case "MERGED":
		return domain.PRStateMerged, true, false
	case "DISCARDED":
		return domain.PRStateClosed, false, true
	default:
		// An unrecognised status is treated as closed rather than open: AO
		// acts on open PRs, so guessing "open" for a state this adapter does
		// not understand is the more damaging of the two errors.
		return domain.PRStateClosed, false, true
	}
}

func safeTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// ---------------------------------------------------------------------------
// Id resolution
// ---------------------------------------------------------------------------

// resolveRequestID maps a project-scoped PR number onto the internal request
// id OneDev's /pulls/{requestId} routes need. The two are different numbers
// and OneDev exposes no by-number route, so the mapping is looked up through
// the query API and cached — it never changes, since OneDev does not reuse a
// PR number within a project.
func (p *Provider) resolveRequestID(ctx context.Context, client *Client, repo ports.SCMRepo, number int) (int64, error) {
	if number <= 0 {
		return 0, fmt.Errorf("onedev scm: pull request number %d: %w", number, ErrNotFound)
	}
	if id, ok := p.cache.getRequestID(repo.Host, repo.Repo, number); ok {
		return id, nil
	}
	q := url.Values{
		"query": {`"Target Project" is ` + quoteQueryValue(projectRef(repo)) +
			` and "Number" is ` + quoteQueryValue(strconv.Itoa(number))},
		"offset": {"0"},
		"count":  {"1"},
	}
	resp, err := client.doGET(ctx, "/pulls", q)
	if err != nil {
		return 0, err
	}
	var matches []restPullRequest
	if err := json.Unmarshal(resp.Body, &matches); err != nil {
		return 0, fmt.Errorf("onedev scm: unmarshal pull request lookup: %w", err)
	}
	if len(matches) == 0 {
		return 0, fmt.Errorf("onedev scm: %s#%d: %w", repo.Repo, number, ErrNotFound)
	}
	p.cache.setRequestID(repo.Host, repo.Repo, number, matches[0].ID)
	return matches[0].ID, nil
}

// resolveUserLogin resolves a OneDev user id to a login, returning "" when it
// cannot. OneDev uses -1 for system-generated activity, and a deleted account
// answers 404; neither is a failure worth propagating, because the login is
// display and attribution metadata rather than a fact the observation's
// correctness rests on.
func (p *Provider) resolveUserLogin(ctx context.Context, client *Client, host string, userID int64) string {
	if userID <= 0 {
		return ""
	}
	if login, ok := p.cache.getUser(host, userID); ok {
		return login
	}
	resp, err := client.doGET(ctx, "/users/"+strconv.FormatInt(userID, 10), nil)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// A deleted account will not reappear; cache the miss so the
			// lookup is not retried on every poll.
			p.cache.setUser(host, userID, "")
		}
		return ""
	}
	var user restUser
	if err := json.Unmarshal(resp.Body, &user); err != nil {
		return ""
	}
	p.cache.setUser(host, userID, user.Name)
	return user.Name
}

// resolveProjectPath resolves a OneDev project id to its full path, returning
// "" when it cannot. Like resolveUserLogin this is best-effort: the caller
// falls back to the repository it already knows.
func (p *Provider) resolveProjectPath(ctx context.Context, client *Client, host string, projectID int64) string {
	if projectID <= 0 {
		return ""
	}
	if path, ok := p.cache.getProjectPath(host, projectID); ok {
		return path
	}
	resp, err := client.doGET(ctx, "/projects/"+strconv.FormatInt(projectID, 10), nil)
	if err != nil {
		return ""
	}
	var project restProject
	if err := json.Unmarshal(resp.Body, &project); err != nil {
		return ""
	}
	p.cache.setProjectPath(host, projectID, project.Path)
	return project.Path
}

// ---------------------------------------------------------------------------
// Guards
// ---------------------------------------------------------------------------

// RepoPRListGuard reports whether a project's pull requests can have changed
// since the caller's token.
//
// OneDev sends neither ETag nor Last-Modified — verified against a live
// instance by inspecting response headers — so there is no conditional request
// to make and no 304 to receive. Rather than pretend otherwise, the guard
// synthesises its own validator: it asks for the single most recently active
// pull request in the project and hashes that request's identity and activity
// timestamp into a token.
//
// That token is sound for this endpoint. Every change to any pull request in
// the project (opening, updating, commenting, merging, discarding) bumps that
// request's last-activity date, which makes it the newest and so changes the
// hash. A tie or a deletion changes the identity half of the hash, which
// errs towards reporting a change.
//
// A guard with no prior token always reports changed, so a cold start does a
// full listing.
func (p *Provider) RepoPRListGuard(ctx context.Context, repo ports.SCMRepo, etag string) (ports.SCMGuardResult, error) {
	client, err := p.clientForRepo(repo)
	if err != nil {
		return ports.SCMGuardResult{}, err
	}
	q := url.Values{
		"query":  {`"Target Project" is ` + quoteQueryValue(projectRef(repo)) + ` order by "Last Activity Date" desc`},
		"offset": {"0"},
		"count":  {"1"},
	}
	resp, err := client.doGET(ctx, "/pulls", q)
	if err != nil {
		return ports.SCMGuardResult{}, err
	}
	var newest []restPullRequest
	if err := json.Unmarshal(resp.Body, &newest); err != nil {
		return ports.SCMGuardResult{}, fmt.Errorf("onedev scm: unmarshal pull request guard: %w", err)
	}

	parts := []string{"onedev-prlist/1", repo.Host, repo.Repo, strconv.Itoa(len(newest))}
	for _, pr := range newest {
		activity := time.Time{}
		if pr.LastActivity != nil {
			activity = safeTime(pr.LastActivity.Date)
		}
		parts = append(parts,
			strconv.FormatInt(pr.ID, 10),
			strconv.Itoa(pr.Number),
			pr.Status,
			activity.UTC().Format(time.RFC3339Nano),
		)
	}
	return guardResult(etag, parts), nil
}

// CommitChecksGuard reports whether a commit's CI state can have changed since
// the caller's token.
//
// This guard is weaker than RepoPRListGuard's and deliberately so. OneDev has
// no queryable per-commit build lookup that AO can rely on: the global build
// query's "Commit" criterion resolves the revision against no project context
// and answers HTTP 404 "Unable to find revision" even for a commit that
// exists, and pull-request builds run against the merge-preview commit rather
// than the branch head the observer passes in. Builds also carry no
// last-activity field, so there is no single cheap value that moves whenever a
// build's status does.
//
// What the guard does instead is hash a bounded window of the project's most
// recently submitted builds, including each one's status and finish time. Any
// status transition inside that window changes the token, which is what the
// observer uses to promote a pull request to a full refresh. A build that
// falls out of the window while still running would not, which is why the
// window exists rather than a count=1 probe — and why this is a
// promote-earlier optimisation rather than a correctness dependency. The
// observer already forces an unconditional refresh once a tracked PR's
// snapshot passes DefaultPRMaxAge, so the worst case is a delayed CI update,
// not a lost one.
//
// The guard never reports NotModified against an empty caller token, so a cold
// cache always fetches.
func (p *Provider) CommitChecksGuard(ctx context.Context, repo ports.SCMRepo, headSHA, etag string) (ports.SCMGuardResult, error) {
	if strings.TrimSpace(headSHA) == "" {
		return ports.SCMGuardResult{}, fmt.Errorf("onedev scm: empty head SHA: %w", ErrNotFound)
	}
	client, err := p.clientForRepo(repo)
	if err != nil {
		return ports.SCMGuardResult{}, err
	}
	q := url.Values{
		"query":  {`"Project" is ` + quoteQueryValue(projectRef(repo)) + ` order by "Submit Date" desc`},
		"offset": {"0"},
		"count":  {strconv.Itoa(checksGuardWindow)},
	}
	resp, err := client.doGET(ctx, "/builds", q)
	if err != nil {
		return ports.SCMGuardResult{}, err
	}
	var builds []restBuild
	if err := json.Unmarshal(resp.Body, &builds); err != nil {
		return ports.SCMGuardResult{}, fmt.Errorf("onedev scm: unmarshal build guard: %w", err)
	}

	parts := []string{"onedev-checks/1", repo.Host, repo.Repo, headSHA, strconv.Itoa(len(builds))}
	for _, b := range builds {
		parts = append(parts,
			strconv.FormatInt(b.ID, 10),
			b.Status,
			b.CommitHash,
			safeTime(b.FinishDate).UTC().Format(time.RFC3339Nano),
		)
	}
	return guardResult(etag, parts), nil
}

// guardResult hashes a guard's inputs into a token and compares it with the
// caller's. An empty caller token never matches, so the first poll after a
// restart always reports changed.
func guardResult(previous string, parts []string) ports.SCMGuardResult {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	token := hex.EncodeToString(sum[:])
	return ports.SCMGuardResult{
		ETag:        token,
		NotModified: previous != "" && previous == token,
	}
}

// ---------------------------------------------------------------------------
// FetchPullRequests
// ---------------------------------------------------------------------------

// FetchPullRequests fetches a detailed observation per ref: request metadata,
// merge preview, CI builds, and reviews.
//
// Results are positionally aligned with refs — result[i] answers refs[i] — and
// a ref that could not be fetched leaves a Fetched=false placeholder carrying
// its error. The observer attributes each observation to the subject it was
// requested for by position, so an observation must never move.
func (p *Provider) FetchPullRequests(ctx context.Context, refs []ports.SCMPRRef) ([]ports.SCMObservation, error) {
	if len(refs) > batchLimit {
		return nil, fmt.Errorf("onedev scm: batch size %d exceeds limit %d", len(refs), batchLimit)
	}
	results := make([]ports.SCMObservation, len(refs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, fetchConcurrency)
	var firstErr error

	for i, ref := range refs {
		wg.Add(1)
		go func(idx int, r ports.SCMPRRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			obs, err := p.fetchSinglePR(ctx, r)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				p.logger.Warn("onedev scm: fetch pull request failed", "repo", r.Repo.Repo, "pr", r.Number, "err", err)
				if firstErr == nil {
					firstErr = err
				}
				results[idx] = ports.SCMObservation{
					Fetched:  false,
					Provider: ProviderKey,
					Host:     r.Repo.Host,
					Repo:     r.Repo.Repo,
					PR:       ports.SCMPRObservation{Number: r.Number, URL: r.URL},
					Error:    err,
				}
				return
			}
			results[idx] = obs
		}(i, ref)
	}
	wg.Wait()
	return results, firstErr
}

// fetchSinglePR assembles one pull request's observation. Every sub-fetch
// failure propagates: the observer preserves the last durable state for a
// Fetched=false observation, which is strictly better than overwriting good
// CI or review facts with an empty snapshot.
func (p *Provider) fetchSinglePR(ctx context.Context, ref ports.SCMPRRef) (ports.SCMObservation, error) {
	repo := ref.Repo
	client, err := p.clientForRepo(repo)
	if err != nil {
		return ports.SCMObservation{}, err
	}
	host, err := p.hostForRepo(repo)
	if err != nil {
		return ports.SCMObservation{}, err
	}
	requestID, err := p.resolveRequestID(ctx, client, repo, ref.Number)
	if err != nil {
		return ports.SCMObservation{}, err
	}

	resp, err := client.doGET(ctx, prPath(requestID), nil)
	if err != nil {
		return ports.SCMObservation{}, err
	}
	var pr restPullRequest
	if err := json.Unmarshal(resp.Body, &pr); err != nil {
		return ports.SCMObservation{}, fmt.Errorf("onedev scm: unmarshal pull request %d: %w", ref.Number, err)
	}

	preview, hasPreview, err := p.fetchMergePreview(ctx, client, requestID)
	if err != nil {
		return ports.SCMObservation{}, err
	}
	headSHA := preview.HeadCommitHash
	if headSHA == "" {
		// No merge preview yet (OneDev computes it asynchronously). The head
		// commit is still recoverable from the request's update history, and
		// the observer needs it to guard CI at all.
		headSHA, err = p.fetchHeadCommit(ctx, client, requestID)
		if err != nil {
			return ports.SCMObservation{}, err
		}
	}

	ci, err := p.fetchCI(ctx, client, host, repo, &pr, headSHA)
	if err != nil {
		return ports.SCMObservation{}, err
	}
	reviews, err := p.fetchReviews(ctx, client, requestID)
	if err != nil {
		return ports.SCMObservation{}, err
	}
	decision := reviewDecision(reviews)

	prObs := p.prObservation(ctx, client, host, repo, &pr)
	prObs.HeadSHA = headSHA
	prObs.MergeCommitSHA = preview.MergeCommitHash
	prObs.ProviderMergeable = pr.MergeStrategy
	prObs.ProviderMergeStateStatus = pr.CheckError
	if requested := strings.TrimSpace(ref.URL); requested != "" && requested != strings.TrimSpace(prObs.URL) {
		prObs.URLAlias = requested
	}

	return ports.SCMObservation{
		Fetched:      true,
		ObservedAt:   time.Now(),
		Provider:     ProviderKey,
		Host:         repo.Host,
		Repo:         repo.Repo,
		PR:           prObs,
		CI:           ci,
		Review:       ports.SCMReviewObservation{Decision: string(decision)},
		Mergeability: mergeability(&pr, preview, hasPreview, ci.Summary, string(decision)),
	}, nil
}

func prPath(requestID int64, sub ...string) string {
	path := "/pulls/" + strconv.FormatInt(requestID, 10)
	if len(sub) > 0 {
		path += "/" + strings.Join(sub, "/")
	}
	return path
}

// fetchMergePreview reads OneDev's precomputed merge of a request into its
// target. The preview is computed asynchronously, so an absent one is a normal
// transient state reported as hasPreview=false rather than an error — the
// caller renders that as "mergeability unknown" instead of guessing.
func (p *Provider) fetchMergePreview(ctx context.Context, client *Client, requestID int64) (restMergePreview, bool, error) {
	resp, err := client.doGET(ctx, prPath(requestID, "merge-preview"), nil)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return restMergePreview{}, false, nil
		}
		return restMergePreview{}, false, err
	}
	body := strings.TrimSpace(string(resp.Body))
	if body == "" || body == "null" {
		return restMergePreview{}, false, nil
	}
	var preview restMergePreview
	if err := json.Unmarshal(resp.Body, &preview); err != nil {
		return restMergePreview{}, false, fmt.Errorf("onedev scm: unmarshal merge preview: %w", err)
	}
	return preview, true, nil
}

// fetchHeadCommit returns the head commit of a request's most recent update.
// OneDev's pull-request payload carries the base and build commits but not the
// head, so the update history is the only place it can be read when no merge
// preview exists.
func (p *Provider) fetchHeadCommit(ctx context.Context, client *Client, requestID int64) (string, error) {
	resp, err := client.doGET(ctx, prPath(requestID, "updates"), nil)
	if err != nil {
		return "", err
	}
	var updates []restPullRequestUpdate
	if err := json.Unmarshal(resp.Body, &updates); err != nil {
		return "", fmt.Errorf("onedev scm: unmarshal pull request updates: %w", err)
	}
	newest := ""
	var newestID int64
	for _, u := range updates {
		if u.HeadCommitHash != "" && (newest == "" || u.ID >= newestID) {
			newest, newestID = u.HeadCommitHash, u.ID
		}
	}
	return newest, nil
}

// ---------------------------------------------------------------------------
// CI
// ---------------------------------------------------------------------------

// fetchCI assembles a pull request's CI snapshot from its builds.
//
// The builds are found with the project-qualified pull-request criterion —
// "Pull Request" is "productone#106". The project qualifier is mandatory: a
// bare number is rejected with "Reference project not specified" even when the
// query already names the project.
//
// That query returns every build the request has ever produced, including
// those for superseded commits, so the results are narrowed to the commit CI
// actually built. For a pull request that is the merge-preview commit OneDev
// records as buildCommitHash; a project configured to build the source branch
// directly uses the head commit instead, so both are accepted. Retries produce
// several builds for one job name, and the query is ordered newest-first, so
// the first build seen for a job name wins.
func (p *Provider) fetchCI(ctx context.Context, client *Client, host allowedHost, repo ports.SCMRepo, pr *restPullRequest, headSHA string) (ports.SCMCIObservation, error) {
	q := url.Values{
		"query":  {`"Pull Request" is ` + quoteQueryValue(pullRequestRef(repo, pr.Number)) + ` order by "Submit Date" desc`},
		"offset": {"0"},
		"count":  {strconv.Itoa(prBuildsPageCount)},
	}
	resp, err := client.doGET(ctx, "/builds", q)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// OneDev answers 404 when the pull-request reference itself
			// cannot be resolved. A request AO is actively observing does
			// exist, so this is the "no builds" shape rather than a failure.
			return ports.SCMCIObservation{Summary: string(domain.CIUnknown), HeadSHA: headSHA}, nil
		}
		return ports.SCMCIObservation{}, fmt.Errorf("onedev scm: fetch builds: %w", err)
	}
	var builds []restBuild
	if err := json.Unmarshal(resp.Body, &builds); err != nil {
		return ports.SCMCIObservation{}, fmt.Errorf("onedev scm: unmarshal builds: %w", err)
	}

	current := map[string]bool{}
	for _, sha := range []string{pr.BuildCommitHash, headSHA} {
		if sha != "" {
			current[sha] = true
		}
	}

	seenJobs := map[string]bool{}
	var checks, failed []ports.SCMCheckObservation
	for _, b := range builds {
		if len(current) > 0 && b.CommitHash != "" && !current[b.CommitHash] {
			continue
		}
		if seenJobs[b.JobName] {
			continue
		}
		seenJobs[b.JobName] = true

		status := buildStatusToCheckStatus(b.Status)
		check := ports.SCMCheckObservation{
			Name:       b.JobName,
			Status:     string(status),
			Conclusion: b.Status,
			URL:        webURL(host, repo.Repo, "builds", b.Number),
			ProviderID: strconv.FormatInt(b.ID, 10),
		}
		checks = append(checks, check)
		if isFailingCheckStatus(status) {
			failed = append(failed, check)
		}
	}

	return ports.SCMCIObservation{
		Summary:           string(ciSummary(checks)),
		HeadSHA:           headSHA,
		FailedFingerprint: failedFingerprint(headSHA, failed),
		Checks:            checks,
		FailedChecks:      failed,
		// The build query is bounded by prBuildsPageCount but narrowed to one
		// build per job name at the current commit, so the snapshot is
		// complete for the commit rather than a truncated window.
		Partial: false,
	}, nil
}

// buildStatusToCheckStatus maps OneDev's Build.Status enum onto AO's check
// status. The enum is WAITING, PENDING, RUNNING, FAILED, CANCELLED, TIMED_OUT
// and SUCCESSFUL.
func buildStatusToCheckStatus(status string) domain.PRCheckStatus {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SUCCESSFUL":
		return domain.PRCheckPassed
	case "FAILED", "TIMED_OUT":
		return domain.PRCheckFailed
	case "CANCELLED":
		return domain.PRCheckCancelled
	case "RUNNING":
		return domain.PRCheckInProgress
	case "WAITING", "PENDING":
		return domain.PRCheckQueued
	default:
		return domain.PRCheckUnknown
	}
}

func isFailingCheckStatus(s domain.PRCheckStatus) bool {
	return s == domain.PRCheckFailed || s == domain.PRCheckCancelled
}

// ciSummary rolls per-check statuses up into AO's aggregate CI state. A
// failure anywhere dominates, then anything still in flight, and only an
// all-finished, all-passing set reports passing.
func ciSummary(checks []ports.SCMCheckObservation) domain.CIState {
	if len(checks) == 0 {
		return domain.CIUnknown
	}
	pending, passed := false, false
	for _, c := range checks {
		switch domain.PRCheckStatus(c.Status) {
		case domain.PRCheckFailed, domain.PRCheckCancelled:
			return domain.CIFailing
		case domain.PRCheckQueued, domain.PRCheckInProgress:
			pending = true
		case domain.PRCheckPassed:
			passed = true
		}
	}
	switch {
	case pending:
		return domain.CIPending
	case passed:
		return domain.CIPassing
	default:
		return domain.CIUnknown
	}
}

// failedFingerprint is a stable signature of the current failing checks, used
// by the observer to notice that a failure set changed without diffing it.
func failedFingerprint(headSHA string, checks []ports.SCMCheckObservation) string {
	if len(checks) == 0 {
		return ""
	}
	parts := make([]string, len(checks))
	for i, c := range checks {
		parts[i] = strings.Join([]string{headSHA, c.Name, c.Status, c.Conclusion, c.URL, c.ProviderID}, "\x00")
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1e")))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Reviews
// ---------------------------------------------------------------------------

func (p *Provider) fetchReviews(ctx context.Context, client *Client, requestID int64) ([]restReview, error) {
	resp, err := client.doGET(ctx, prPath(requestID, "reviews"), nil)
	if err != nil {
		return nil, fmt.Errorf("onedev scm: fetch reviews: %w", err)
	}
	var reviews []restReview
	if err := json.Unmarshal(resp.Body, &reviews); err != nil {
		return nil, fmt.Errorf("onedev scm: unmarshal reviews: %w", err)
	}
	return reviews, nil
}

// reviewDecision derives AO's normalized review decision from OneDev's
// per-reviewer statuses. A requested change dominates an approval — a reviewer
// asking for changes is the signal AO must not lose — and a still-pending
// reviewer means the review is required but not yet given. EXCLUDED reviewers
// (removed from the request) carry no signal.
func reviewDecision(reviews []restReview) domain.ReviewDecision {
	pending, approved := false, false
	for _, r := range reviews {
		switch normalizeReviewStatus(r.Status) {
		case domain.ReviewChangesRequest:
			return domain.ReviewChangesRequest
		case domain.ReviewRequired:
			pending = true
		case domain.ReviewApproved:
			approved = true
		}
	}
	switch {
	case pending:
		return domain.ReviewRequired
	case approved:
		return domain.ReviewApproved
	default:
		return domain.ReviewNone
	}
}

// normalizeReviewStatus maps OneDev's PullRequestReview.Status enum onto AO's
// review decision vocabulary.
func normalizeReviewStatus(status string) domain.ReviewDecision {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "APPROVED":
		return domain.ReviewApproved
	case "REQUESTED_FOR_CHANGES":
		return domain.ReviewChangesRequest
	case "PENDING":
		return domain.ReviewRequired
	default:
		// EXCLUDED, and anything a later OneDev release adds.
		return domain.ReviewNone
	}
}

// FetchReviewThreads fetches a pull request's review verdicts and its
// request-level comments.
//
// OneDev's inline (code) comments are deliberately absent. Its REST API
// exposes code comments only as GET /~api/code-comments/{commentId} — there is
// no per-request listing and no query resource — so an inline thread cannot be
// enumerated from a pull request. Request-level comments therefore each become
// a single-comment thread with no file anchor, and the observation is marked
// Partial so the store merges these rows rather than treating them as a
// complete replacement snapshot that would delete threads AO cannot see.
func (p *Provider) FetchReviewThreads(ctx context.Context, ref ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	repo := ref.Repo
	client, err := p.clientForRepo(repo)
	if err != nil {
		return ports.SCMReviewObservation{}, err
	}
	host, err := p.hostForRepo(repo)
	if err != nil {
		return ports.SCMReviewObservation{}, err
	}
	requestID, err := p.resolveRequestID(ctx, client, repo, ref.Number)
	if err != nil {
		return ports.SCMReviewObservation{}, err
	}

	reviews, err := p.fetchReviews(ctx, client, requestID)
	if err != nil {
		return ports.SCMReviewObservation{}, err
	}
	prURL := webURL(host, repo.Repo, "pulls", ref.Number)

	summaries := make([]ports.SCMReviewSummaryObservation, 0, len(reviews))
	for _, r := range reviews {
		state := normalizeReviewStatus(r.Status)
		if state == domain.ReviewNone {
			continue
		}
		author := p.resolveUserLogin(ctx, client, repo.Host, r.UserID)
		summaries = append(summaries, ports.SCMReviewSummaryObservation{
			ID:          "review:" + strconv.FormatInt(r.ID, 10),
			Author:      author,
			State:       string(state),
			URL:         prURL,
			IsBot:       isBotAuthor(author),
			SubmittedAt: safeTime(r.StatusDate),
		})
	}

	resp, err := client.doGET(ctx, prPath(requestID, "comments"), nil)
	if err != nil {
		return ports.SCMReviewObservation{}, fmt.Errorf("onedev scm: fetch comments: %w", err)
	}
	var comments []restPullRequestComment
	if err := json.Unmarshal(resp.Body, &comments); err != nil {
		return ports.SCMReviewObservation{}, fmt.Errorf("onedev scm: unmarshal comments: %w", err)
	}

	threads := make([]ports.SCMReviewThreadObservation, 0, len(comments))
	for _, c := range comments {
		author := p.resolveUserLogin(ctx, client, repo.Host, c.UserID)
		id := strconv.FormatInt(c.ID, 10)
		bot := isBotAuthor(author)
		threads = append(threads, ports.SCMReviewThreadObservation{
			ID: "comment:" + id,
			// A request-level comment is not anchored to a file, and OneDev
			// has no resolve state for one, so it is never reported resolved.
			Resolved: false,
			IsBot:    bot,
			Comments: []ports.SCMReviewCommentObservation{{
				ID:     id,
				Author: author,
				Body:   c.Content,
				URL:    prURL,
				IsBot:  bot,
			}},
		})
	}

	return ports.SCMReviewObservation{
		Decision: string(reviewDecision(reviews)),
		Reviews:  summaries,
		Threads:  threads,
		Partial:  true,
	}, nil
}

// isBotAuthor recognises the conventional bot-account naming AO's other
// adapters key off. OneDev has no account-type flag for bots, so naming is the
// only signal available.
func isBotAuthor(login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return false
	}
	return strings.HasSuffix(login, "[bot]") || strings.HasSuffix(login, "-bot") || strings.HasPrefix(login, "bot-")
}

// ---------------------------------------------------------------------------
// Mergeability
// ---------------------------------------------------------------------------

// mergeability turns OneDev's merge preview into AO's normalized verdict.
//
// The preview is authoritative about conflicts and nothing else: OneDev
// documents a null mergeCommitHash as "there are conflicts". Everything else
// that blocks a merge — failing CI, an outstanding review, a failed merge
// check — is layered on top the same way the GitLab adapter layers it.
func mergeability(pr *restPullRequest, preview restMergePreview, hasPreview bool, ciState, reviewDecision string) ports.SCMMergeabilityObservation {
	state, _, _ := normalizePRStatus(pr.Status)
	if state != domain.PRStateOpen {
		return ports.SCMMergeabilityObservation{
			State:    string(domain.MergeBlocked),
			Blockers: []string{"blocked_by_provider"},
		}
	}
	if !hasPreview {
		// OneDev computes the preview asynchronously; until it exists nothing
		// is known about whether the merge would apply.
		return ports.SCMMergeabilityObservation{State: string(domain.MergeUnknown)}
	}
	if preview.MergeCommitHash == "" {
		return ports.SCMMergeabilityObservation{
			State:    string(domain.MergeConflicting),
			Conflict: true,
			Blockers: []string{"conflicts"},
		}
	}

	var blockers []string
	if strings.TrimSpace(pr.CheckError) != "" {
		blockers = append(blockers, "blocked_by_provider")
	}
	if ciState == string(domain.CIFailing) {
		blockers = append(blockers, "ci_failing")
	}
	switch reviewDecision {
	case string(domain.ReviewChangesRequest):
		blockers = append(blockers, "changes_requested")
	case string(domain.ReviewRequired):
		blockers = append(blockers, "review_required")
	}
	if len(blockers) > 0 {
		return ports.SCMMergeabilityObservation{
			State:    string(domain.MergeBlocked),
			Blockers: blockers,
		}
	}
	return ports.SCMMergeabilityObservation{
		State:     string(domain.MergeMergeable),
		Mergeable: true,
	}
}

// ---------------------------------------------------------------------------
// FetchFailedCheckLogTail
// ---------------------------------------------------------------------------

// FetchFailedCheckLogTail returns the tail of a failed build's log.
//
// GET /~api/streaming/build-logs/{buildId} streams length-prefixed frames
// rather than returning a document, so the body is decoded incrementally and
// only a fixed number of trailing lines is retained — see tailBuildLog. The
// build id comes from the check's ProviderID, which fetchCI stamped.
func (p *Provider) FetchFailedCheckLogTail(ctx context.Context, repo ports.SCMRepo, check ports.SCMCheckObservation) (string, error) {
	buildID := strings.TrimSpace(check.ProviderID)
	if buildID == "" {
		return "", fmt.Errorf("onedev scm: check %q has no build id", check.Name)
	}
	client, err := p.clientForRepo(repo)
	if err != nil {
		return "", err
	}
	body, err := client.doGETStream(ctx, "/streaming/build-logs/"+url.PathEscape(buildID), nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	return tailBuildLog(body, buildLogTailLines)
}
