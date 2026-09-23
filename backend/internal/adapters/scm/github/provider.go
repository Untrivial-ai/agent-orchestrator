package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ciFailureLogTailLines is the number of trailing lines of a failed job's
// log we splice into the observation. 20 lines is enough to catch the
// usual "X tests failed" tail without bloating the per-PR row.
const ciFailureLogTailLines = 20

// ProviderOptions configures a Provider. Production code typically sets
// Token; tests inject a pre-built Client pointed at httptest.
type ProviderOptions struct {
	Client     *Client
	HTTPClient *http.Client
	Token      TokenSource
	// SkipTokenPreflight defers token validation until the first provider call.
	// Daemon wiring uses this so gh-token shell-out never blocks readiness.
	SkipTokenPreflight bool
	RESTBase           string
	GraphQLURL         string
	UserAgent          string
	Logger             *slog.Logger
}

// Provider observes one GitHub pull request and returns a normalized
// ports.PRObservation for the PR Manager to persist. There is no polling
// loop in v1 — the loop is a follow-up PR (#35); this adapter is the
// observation primitive that loop will call.
type Provider struct {
	client           *Client
	logger           *slog.Logger
	identityMu       sync.Mutex
	identity         ports.SCMIdentity
	identityResolved bool
}

// NewProvider returns a Provider. If opts.Client is supplied it is used
// verbatim; otherwise a Client is built from the other options. When a
// Token source is supplied it is exercised once so missing credentials
// surface at daemon startup rather than at first observation, unless
// SkipTokenPreflight is set. Tests that want an unauthenticated fake pass
// opts.Client directly.
func NewProvider(opts ProviderOptions) (*Provider, error) {
	if opts.Client == nil && opts.Token != nil && !opts.SkipTokenPreflight {
		if _, err := opts.Token.Token(context.Background()); err != nil {
			return nil, err
		}
	}
	c := opts.Client
	if c == nil {
		c = NewClient(ClientOptions{
			HTTPClient: opts.HTTPClient,
			Token:      opts.Token,
			RESTBase:   opts.RESTBase,
			GraphQLURL: opts.GraphQLURL,
			UserAgent:  opts.UserAgent,
		})
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{client: c, logger: logger}, nil
}

// SCMCredentialsAvailable checks whether this provider can obtain a token. The
// SCM observer calls it lazily during the first poll that has SCM subjects, so
// daemon readiness is not blocked by shelling out to gh auth token and idle
// daemons do not warn about missing credentials.
func (p *Provider) SCMCredentialsAvailable(ctx context.Context) (bool, error) {
	if p.client == nil || p.client.tokens == nil {
		return true, nil
	}
	if _, err := p.client.tokens.Token(ctx); err != nil {
		if errors.Is(err, ErrNoToken) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// AuthenticatedIdentity returns the account associated with the active GitHub
// token. Successful results are cached for the provider's lifetime.
func (p *Provider) AuthenticatedIdentity(ctx context.Context) (ports.SCMIdentity, error) {
	p.identityMu.Lock()
	defer p.identityMu.Unlock()
	if p.identityResolved {
		return p.identity, nil
	}
	resp, err := p.client.doREST(ctx, http.MethodGet, "/user", nil, nil)
	if err != nil {
		return ports.SCMIdentity{}, err
	}
	var user struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	if err := json.Unmarshal(resp.Body, &user); err != nil {
		return ports.SCMIdentity{}, fmt.Errorf("github scm: decode authenticated user: %w", err)
	}
	p.identity = ports.SCMIdentity{
		Login: strings.TrimSpace(user.Login),
		Human: strings.EqualFold(strings.TrimSpace(user.Type), "User"),
	}
	p.identityResolved = true
	return p.identity, nil
}

// Observe fetches the current state of one PR by its github.com URL and
// returns a normalized ports.PRObservation. Any required network call
// failing yields Fetched=false (caller must not infer "PR closed" from a
// failed observation).
func (p *Provider) Observe(ctx context.Context, prURL string) (ports.PRObservation, error) {
	out := ports.PRObservation{URL: prURL}
	owner, repo, number, err := parsePRURL(prURL)
	if err != nil {
		return out, err
	}
	out.Number = number

	rest, err := p.fetchRESTPull(ctx, owner, repo, number)
	if err != nil {
		// Network/auth/rate-limit failures must surface as Fetched:false.
		// Stable terminal states like 404 also surface that way — the PR
		// Manager keeps the prior row rather than fabricating closed/merged.
		return out, scmObserveError(err)
	}

	out.Draft = rest.Draft
	out.Merged = rest.Merged || (rest.MergedAt != "")
	out.Closed = strings.EqualFold(rest.State, "closed") && !out.Merged

	gq, err := p.fetchGraphQL(ctx, owner, repo, number)
	if err != nil {
		return out, scmObserveError(err)
	}

	out.CI = ciSummaryFromGraphQL(gq)
	out.Review = reviewDecisionFromGraphQL(gq)
	out.Mergeability = mergeabilityFromGraphQL(gq, rest, out.CI, out.Review)
	out.Checks = checksFromGraphQL(gq, rest.Head.SHA)
	out.Comments = commentsFromGraphQL(gq)

	// Log-tail enrichment is best-effort: a job-log fetch failure must not
	// flip the observation to Fetched:false, because we already have the
	// authoritative CI summary from GraphQL. Stamp a one-liner instead.
	for i := range out.Checks {
		if !isFailingCheckStatus(out.Checks[i].Status) {
			continue
		}
		jobID := jobIDForCheck(gq, out.Checks[i].Name)
		if jobID == 0 {
			continue
		}
		log, fetchErr := p.fetchJobLogTail(ctx, owner, repo, jobID)
		if fetchErr != nil {
			out.Checks[i].LogTail = fmt.Sprintf("<log fetch failed: %s>", scrubError(fetchErr))
			continue
		}
		out.Checks[i].LogTail = tailLines(log, ciFailureLogTailLines)
	}

	out.Fetched = true
	return out, nil
}

func scmObserveError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: %w", ports.ErrSCMPRNotFound, err)
	}
	return err
}

// ---------------------------------------------------------------------------
// REST: pull payload
// ---------------------------------------------------------------------------

type restPull struct {
	State    string `json:"state"`
	Draft    bool   `json:"draft"`
	Merged   bool   `json:"merged"`
	MergedAt string `json:"merged_at"`
	Head     struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
}

func (p *Provider) fetchRESTPull(ctx context.Context, owner, repo string, number int) (restPull, error) {
	resp, err := p.client.doREST(ctx, http.MethodGet, repoPath(owner, repo, "pulls", strconv.Itoa(number)), nil, nil)
	if err != nil {
		return restPull{}, err
	}
	if len(resp.Body) == 0 {
		return restPull{}, errors.New("github scm: empty pull response")
	}
	var pull restPull
	if err := json.Unmarshal(resp.Body, &pull); err != nil {
		return restPull{}, fmt.Errorf("github scm: decode pull: %w", err)
	}
	return pull, nil
}

// ---------------------------------------------------------------------------
// GraphQL: the heavy lift
// ---------------------------------------------------------------------------

// graphQLCheckContextLimit caps how many statusCheckRollup contexts we
// request in one GraphQL hop. 100 is GitHub's documented per-page max
// for the contexts connection. When the rollup has MORE than this many
// contexts the response surfaces pageInfo.hasNextPage=true and
// ciSummaryFromGraphQL is conservative (see the "CIUnknown on
// hasNextPage when not already CIFailing" branch — a partial visible
// set could hide a failure, so we degrade the verdict rather than
// risk reporting a broken PR as passing).
const graphQLCheckContextLimit = 100

// prObservationQuery is the GraphQL query (derived from PR #28, credited
// to @whoisasx) that pulls everything we need in one round trip:
//   - reviewDecision (APPROVED / CHANGES_REQUESTED / REVIEW_REQUIRED / null)
//   - mergeable + mergeStateStatus (DIRTY / BLOCKED / UNSTABLE / CLEAN / ...)
//   - latest commit's statusCheckRollup (CheckRuns + StatusContexts) so we
//     can derive a CIState without an extra REST hop, and so that bot vs
//     human is detected via __typename on review comments.
const prObservationQuery = `query($owner:String!,$repo:String!,$number:Int!){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      number
      url
      state
      isDraft
      merged
      closed
      mergeable
      mergeStateStatus
      reviewDecision
      headRefOid
      commits(last:1){ nodes{ commit{
        oid
        statusCheckRollup{
          state
          contexts(first:CONTEXT_LIMIT){
            nodes{
              __typename
              ... on CheckRun  { name status conclusion detailsUrl url databaseId }
              ... on StatusContext { context state targetUrl }
            }
            pageInfo{ hasNextPage }
          }
        }
      } } }
      reviewThreads(last:100){ nodes{
        id
        isResolved
        comments(first:100){ nodes{
          id
          body
          path
          line
          url
          pullRequestReview{ databaseId }
          author{ login __typename }
        } }
      } }
    }
  }
}`

func (p *Provider) fetchGraphQL(ctx context.Context, owner, repo string, number int) (map[string]any, error) {
	q := strings.Replace(prObservationQuery, "CONTEXT_LIMIT", strconv.Itoa(graphQLCheckContextLimit), 1)
	data, err := p.client.doGraphQL(ctx, q, map[string]any{"owner": owner, "repo": repo, "number": number})
	if err != nil {
		return nil, err
	}
	repoData, _ := data["repository"].(map[string]any)
	pr, _ := repoData["pullRequest"].(map[string]any)
	if pr == nil {
		return nil, fmt.Errorf("%w: pull request not found in graphql response", ErrNotFound)
	}
	return pr, nil
}

// ---------------------------------------------------------------------------
// REST: per-job log tail
// ---------------------------------------------------------------------------

func (p *Provider) fetchJobLogTail(ctx context.Context, owner, repo string, jobID int64) (string, error) {
	logPath := repoPath(owner, repo, "actions", "jobs", strconv.FormatInt(jobID, 10), "logs")
	body, err := p.client.fetchPlainText(ctx, logPath)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ---------------------------------------------------------------------------
// Projection helpers
// ---------------------------------------------------------------------------

// ciSummaryFromGraphQL maps the per-PR status rollup onto domain.CIState.
// If ANY visible context concluded failure-class we return CIFailing.
// Otherwise any pending context wins over passing. An empty rollup is
// CIUnknown. When the rollup is paginated (pageInfo.hasNextPage=true)
// the verdict is conservative: a known failure is still safe — failures
// don't get un-failed by more pages — but passing/pending/unknown
// verdicts could hide a failing context on the next page, so we degrade
// them all to CIUnknown rather than risk reporting a broken PR as ready.
func ciSummaryFromGraphQL(pr map[string]any) domain.CIState {
	roll := statusRollup(pr)
	if roll == nil {
		return domain.CIUnknown
	}
	contexts, _ := roll["contexts"].(map[string]any)
	rawNodes := nodes(contexts["nodes"])
	if len(rawNodes) == 0 {
		// GitHub returns a top-level "state" on the rollup even when the
		// nodes list is empty (e.g. SUCCESS / FAILURE / PENDING). Honor it
		// rather than returning CIUnknown for an otherwise-decided PR.
		return mapRollupState(str(roll["state"]))
	}
	pending, passing := false, false
	for _, n := range rawNodes {
		st := checkStatusFromGraphQL(n)
		switch st {
		case domain.PRCheckFailed, domain.PRCheckCancelled:
			return domain.CIFailing
		case domain.PRCheckQueued, domain.PRCheckInProgress:
			pending = true
		case domain.PRCheckPassed:
			passing = true
		}
	}
	if pageInfoHasMore(contexts) {
		return domain.CIUnknown
	}
	switch {
	case pending:
		return domain.CIPending
	case passing:
		return domain.CIPassing
	default:
		return domain.CIUnknown
	}
}

// pageInfoHasMore reports whether the rollup contexts have a next page
// the current request didn't fetch. We treat a missing pageInfo block
// as "no more" (older API shapes that don't expose pagination simply
// return everything in one page).
func pageInfoHasMore(contexts map[string]any) bool {
	pi, ok := contexts["pageInfo"].(map[string]any)
	if !ok {
		return false
	}
	return boolv(pi["hasNextPage"])
}

func mapRollupState(s string) domain.CIState {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "SUCCESS":
		return domain.CIPassing
	case "FAILURE", "ERROR":
		return domain.CIFailing
	case "PENDING", "EXPECTED":
		return domain.CIPending
	default:
		return domain.CIUnknown
	}
}

// reviewDecisionFromGraphQL normalizes the GraphQL reviewDecision enum
// onto the domain vocabulary. Re-implemented inline because the helper
// referenced in the task brief lived against types that no longer exist.
func reviewDecisionFromGraphQL(pr map[string]any) domain.ReviewDecision {
	switch strings.ToUpper(strings.TrimSpace(str(pr["reviewDecision"]))) {
	case "APPROVED":
		return domain.ReviewApproved
	case "CHANGES_REQUESTED":
		return domain.ReviewChangesRequest
	case "REVIEW_REQUIRED":
		return domain.ReviewRequired
	default:
		return domain.ReviewNone
	}
}

// mergeabilityFromGraphQL composes the merge verdict from three signals:
// the REST mergeable/rebaseable booleans, the GraphQL mergeStateStatus,
// and the already-derived CIState + ReviewDecision. The rules follow the
// spec table in doc.go.
func mergeabilityFromGraphQL(pr map[string]any, rest restPull, ci domain.CIState, review domain.ReviewDecision) domain.Mergeability {
	// REST's mergeable_state is the tiebreaker: GraphQL's
	// mergeStateStatus enum (DIRTY / BLOCKED / UNSTABLE / CLEAN /
	// UNKNOWN) is the primary; if it is empty we fall back to the
	// REST string (lowercase: "dirty" / "blocked" / "unstable" /
	// "clean" / "behind" / "unknown") uppercased so the same switch
	// covers both shapes. The REST API does NOT expose a
	// `merge_state_status` field — earlier revs of this code chased
	// that ghost; we use mergeable_state instead.
	state := strings.ToUpper(strings.TrimSpace(firstNonEmpty(str(pr["mergeStateStatus"]), rest.MergeableState)))
	rawMergeable := strings.ToUpper(strings.TrimSpace(str(pr["mergeable"])))

	switch state {
	case "DIRTY":
		return domain.MergeConflicting
	case "BLOCKED":
		return domain.MergeBlocked
	case "UNSTABLE":
		return domain.MergeUnstable
	}
	if rawMergeable == "CONFLICTING" {
		return domain.MergeConflicting
	}

	if rest.Draft || boolv(pr["isDraft"]) {
		return domain.MergeBlocked
	}
	if review == domain.ReviewChangesRequest || review == domain.ReviewRequired {
		return domain.MergeBlocked
	}
	if ci == domain.CIFailing {
		return domain.MergeBlocked
	}

	// REST's mergeable_state ("clean" / "blocked" / "behind" / "dirty" / "unstable"
	// / "draft" / "unknown") backs up the GraphQL view when GitHub hasn't
	// computed the rollup yet.
	switch strings.ToLower(strings.TrimSpace(rest.MergeableState)) {
	case "clean":
		if rawMergeable == "MERGEABLE" || (rest.Mergeable != nil && *rest.Mergeable) {
			return domain.MergeMergeable
		}
	case "dirty":
		return domain.MergeConflicting
	case "blocked":
		return domain.MergeBlocked
	case "unstable":
		return domain.MergeUnstable
	}

	if rawMergeable == "MERGEABLE" && state == "CLEAN" {
		return domain.MergeMergeable
	}
	return domain.MergeUnknown
}

// checksFromGraphQL projects each context node into a PRCheckObservation.
// StatusContext (commit-status) and CheckRun (Actions) are both flattened
// into the same slice because downstream consumers don't distinguish.
func checksFromGraphQL(pr map[string]any, headSHA string) []ports.PRCheckObservation {
	roll := statusRollup(pr)
	contexts, _ := roll["contexts"].(map[string]any)
	rawNodes := nodes(contexts["nodes"])
	if len(rawNodes) == 0 {
		return nil
	}
	out := make([]ports.PRCheckObservation, 0, len(rawNodes))
	for _, n := range rawNodes {
		typ := str(n["__typename"])
		var name, urlOut string
		switch typ {
		case "CheckRun":
			name = str(n["name"])
			urlOut = firstNonEmpty(str(n["detailsUrl"]), str(n["url"]))
		case "StatusContext":
			name = str(n["context"])
			urlOut = str(n["targetUrl"])
		default:
			continue
		}
		if name == "" {
			continue
		}
		out = append(out, ports.PRCheckObservation{
			Name:       name,
			CommitHash: headSHA,
			Status:     checkStatusFromGraphQL(n),
			URL:        urlOut,
		})
	}
	return out
}

// commentsFromGraphQL flattens unresolved review threads into one comment
// per node, dropping bot authors entirely (the spec keeps Resolved=false
// always since we filter resolved threads out client-side).
func commentsFromGraphQL(pr map[string]any) []ports.PRCommentObservation {
	threads, _ := pr["reviewThreads"].(map[string]any)
	rawNodes := nodes(threads["nodes"])
	if len(rawNodes) == 0 {
		return nil
	}
	var out []ports.PRCommentObservation
	for _, th := range rawNodes {
		if boolv(th["isResolved"]) {
			continue
		}
		comments, _ := th["comments"].(map[string]any)
		for _, cn := range nodes(comments["nodes"]) {
			author, _ := cn["author"].(map[string]any)
			parentReview, _ := cn["pullRequestReview"].(map[string]any)
			if isBotAuthor(author) {
				continue
			}
			out = append(out, ports.PRCommentObservation{
				ID:       str(cn["id"]),
				ThreadID: str(th["id"]),
				ReviewID: decimalID(parentReview["databaseId"]),
				Author:   str(author["login"]),
				File:     str(cn["path"]),
				Line:     int(num(cn["line"])),
				Body:     str(cn["body"]),
				URL:      str(cn["url"]),
				Resolved: false,
			})
		}
	}
	return out
}

// isBotAuthor uses ONLY GitHub's typed signal (__typename or User.Type
// === "Bot"). The strings.Contains(login, "bot") fallback from PR #28
// was deliberately dropped — aa-18 flagged it as a false-positive
// magnet (logins like "robothon", "lambot123" tripped it).
func isBotAuthor(author map[string]any) bool {
	if strings.EqualFold(str(author["__typename"]), "Bot") {
		return true
	}
	if strings.EqualFold(str(author["type"]), "Bot") {
		return true
	}
	return false
}

// jobIDForCheck looks up the Actions job ID for a check by name, so we
// can call /actions/jobs/{job_id}/logs. StatusContext rows have no job
// ID (they're commit statuses, not Actions runs); those return 0 and
// the log fetch is skipped for them.
func jobIDForCheck(pr map[string]any, name string) int64 {
	roll := statusRollup(pr)
	contexts, _ := roll["contexts"].(map[string]any)
	for _, n := range nodes(contexts["nodes"]) {
		if str(n["__typename"]) != "CheckRun" {
			continue
		}
		if str(n["name"]) != name {
			continue
		}
		return int64(num(n["databaseId"]))
	}
	return 0
}

// statusRollup extracts the commits[0].commit.statusCheckRollup blob
// from the GraphQL pullRequest payload. Nil when the PR has no commits
// or GitHub hasn't computed the rollup yet.
func statusRollup(pr map[string]any) map[string]any {
	commits, _ := pr["commits"].(map[string]any)
	for _, n := range nodes(commits["nodes"]) {
		commit, _ := n["commit"].(map[string]any)
		roll, _ := commit["statusCheckRollup"].(map[string]any)
		if roll != nil {
			return roll
		}
	}
	return nil
}

// checkStatusFromGraphQL maps the (status, conclusion) tuple of one node
// onto the domain enum. Failure-class conclusions always win — pending
// status with a final conclusion of "failure" is still a failed check.
func checkStatusFromGraphQL(n map[string]any) domain.PRCheckStatus {
	typ := str(n["__typename"])
	if typ == "StatusContext" {
		switch strings.ToUpper(strings.TrimSpace(str(n["state"]))) {
		case "SUCCESS":
			return domain.PRCheckPassed
		case "FAILURE", "ERROR":
			return domain.PRCheckFailed
		case "PENDING", "EXPECTED":
			return domain.PRCheckInProgress
		default:
			return domain.PRCheckUnknown
		}
	}
	conclusion := strings.ToUpper(strings.TrimSpace(str(n["conclusion"])))
	status := strings.ToUpper(strings.TrimSpace(str(n["status"])))
	switch conclusion {
	case "SUCCESS", "NEUTRAL":
		return domain.PRCheckPassed
	case "FAILURE", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return domain.PRCheckFailed
	case "CANCELLED":
		return domain.PRCheckCancelled
	case "SKIPPED", "STALE":
		return domain.PRCheckSkipped
	}
	switch status {
	case "QUEUED", "PENDING", "REQUESTED", "WAITING":
		return domain.PRCheckQueued
	case "IN_PROGRESS":
		return domain.PRCheckInProgress
	case "COMPLETED":
		// Completed without a conclusion is unusual but reachable — treat
		// it as unknown so the caller does not over-trust an absent state.
		return domain.PRCheckUnknown
	}
	return domain.PRCheckUnknown
}

func isFailingCheckStatus(s domain.PRCheckStatus) bool {
	return s == domain.PRCheckFailed || s == domain.PRCheckCancelled
}

// ---------------------------------------------------------------------------
// URL + path helpers
// ---------------------------------------------------------------------------

// parsePRURL accepts both the canonical github.com web URL and the API
// pulls URL. Returns owner, repo, number or an error wrapping ErrNotFound
// for shapes we don't recognise (so the caller surfaces them like any
// other "PR isn't on GitHub" outcome).
func parsePRURL(prURL string) (string, string, int, error) {
	if prURL == "" {
		return "", "", 0, fmt.Errorf("%w: empty PR url", ErrNotFound)
	}
	u, err := url.Parse(prURL)
	if err != nil {
		return "", "", 0, fmt.Errorf("%w: parse url: %w", ErrNotFound, err)
	}
	host := strings.ToLower(u.Host)
	// Accept github.com (web) and api.github.com (REST/GraphQL). GitHub
	// Enterprise hosts must end in .github.com or .ghe.io (GitHub's own
	// dedicated TLDs); anything else looks like a bad URL or a different
	// SCM and is rejected.
	switch {
	case host == "":
		// Allow path-only URLs (parsePRURL is also exercised via API
		// paths without a host in some tests).
	case host == "github.com", host == "www.github.com", host == "api.github.com":
		// canonical
	case strings.HasSuffix(host, ".github.com") || strings.HasSuffix(host, ".ghe.io"):
		// enterprise
	default:
		return "", "", 0, fmt.Errorf("%w: host %q is not a github host", ErrNotFound, host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// Web form: /owner/repo/pull/123
	if len(parts) >= 4 && (parts[2] == "pull" || parts[2] == "pulls") {
		owner, repo := parts[0], parts[1]
		n, err := strconv.Atoi(parts[3])
		if err != nil || n <= 0 {
			return "", "", 0, fmt.Errorf("%w: bad PR number %q", ErrNotFound, parts[3])
		}
		return owner, repo, n, nil
	}
	// API form: /repos/owner/repo/pulls/123
	if len(parts) >= 5 && parts[0] == "repos" && parts[3] == "pulls" {
		owner, repo := parts[1], parts[2]
		n, err := strconv.Atoi(parts[4])
		if err != nil || n <= 0 {
			return "", "", 0, fmt.Errorf("%w: bad PR number %q", ErrNotFound, parts[4])
		}
		return owner, repo, n, nil
	}
	return "", "", 0, fmt.Errorf("%w: not a github PR url: %s", ErrNotFound, prURL)
}

func repoPath(owner, repo string, elems ...string) string {
	all := append([]string{"repos", owner, repo}, elems...)
	for i := range all {
		all[i] = url.PathEscape(all[i])
	}
	return "/" + path.Join(all...)
}

// ---------------------------------------------------------------------------
// Small JSON-ish accessors
// ---------------------------------------------------------------------------

func nodes(v any) []map[string]any {
	a, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(a))
	for _, item := range a {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func boolv(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func num(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}

func decimalID(v any) string {
	switch value := v.(type) {
	case json.Number:
		return value.String()
	case float64:
		if value > 0 {
			return strconv.FormatInt(int64(value), 10)
		}
	case int:
		if value > 0 {
			return strconv.Itoa(value)
		}
	case int64:
		if value > 0 {
			return strconv.FormatInt(value, 10)
		}
	case string:
		return strings.TrimSpace(value)
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func tailLines(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// scrubError keeps the error message single-line so the LogTail field
// stays a tidy one-liner instead of leaking multi-line API payloads
// into the PR row.
func scrubError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	return strings.TrimSpace(msg)
}
