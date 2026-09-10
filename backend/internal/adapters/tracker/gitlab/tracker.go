package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	scmgitlab "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/gitlab"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/tracker/httpkit"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	defaultBaseURL   = "https://gitlab.com/api/v4"
	defaultUserAgent = "ao-agent-orchestrator/tracker-gitlab"

	// GitLab's per_page maxes at 100. ListFilter.Limit is an optional
	// total-result cap; page size stays at the provider max.
	listPageSize = 100
	// Guard against a pathological Link cycle. At GitLab's max page size
	// this still permits a 5k-issue intake sweep before failing loud.
	maxListPages = 50
)

// Sentinel errors. Adapter-level callers should match on these via
// errors.Is; the orchestrator's lifecycle code is intentionally insulated
// from raw HTTP status codes.
var (
	ErrNotFound       = errors.New("gitlab tracker: issue not found")
	ErrRateLimited    = errors.New("gitlab tracker: rate limited")
	ErrAuthFailed     = errors.New("gitlab tracker: authentication failed")
	ErrWrongProvider  = errors.New("gitlab tracker: id is not a gitlab tracker id")
	ErrBadID          = errors.New("gitlab tracker: malformed native id")
	ErrHostNotAllowed = errors.New("gitlab tracker: host not in allowlist")
)

// RateLimitError is an alias for httpkit.RateLimitError so existing callers
// that use errors.As with *RateLimitError continue to work. The sentinel
// field is set to ErrRateLimited by the adapter's classifyError.
type RateLimitError = httpkit.RateLimitError

// Options configures a Tracker. All fields except Token are optional —
// production code typically sets Token alone; tests inject HTTPClient and
// BaseURL to point at an httptest fake.
//
// AllowedHosts is the list of self-managed GitLab hosts the tracker is
// permitted to talk to. gitlab.com (Host: "") is always allowed and does
// not need to appear here. A host not in this list and not gitlab.com is
// rejected before any credential is attached.
//
// HostTokens maps a self-managed host to a token override. Hosts in
// AllowedHosts without an explicit entry fall back to the default Token.
// The per-host selection ensures the gitlab.com token is not attached to a
// self-managed host and vice versa.
type Options struct {
	Token        scmgitlab.TokenSource
	HTTPClient   *http.Client
	BaseURL      string
	UserAgent    string
	AllowedHosts []string
	HostTokens   map[string]scmgitlab.TokenSource
}

// hostEntry holds the per-host base URL and token source. The default
// entry (for gitlab.com / Host: "") uses Options.BaseURL and Options.Token.
// Self-managed hosts get a derived base URL (https://<host>/api/v4) and
// either the per-host token from HostTokens or the default token.
type hostEntry struct {
	baseURL string
	tokens  scmgitlab.TokenSource
}

// Tracker implements ports.Tracker against the GitLab REST API v4.
//
// Construction performs a fail-fast token presence check (no network call).
// The first Preflight call validates the token against GitLab itself; a
// successful preflight is cached for the lifetime of the Tracker so repeat
// calls are free, while failures are intentionally NOT cached so a
// transient startup glitch doesn't permanently brick the adapter.
//
// The tracker is host-aware: it maintains a per-host base URL + token map,
// mirroring the SCM provider's clientForHost pattern. gitlab.com (zero-value
// Host: "") uses the default base URL and default token. Self-managed hosts
// must be in AllowedHosts; unconfigured hosts are rejected before any
// credential is attached.
type Tracker struct {
	http      *http.Client
	userAgent string

	// defaultHost is the config for gitlab.com (Host: "").
	defaultHost hostEntry

	// hosts maps each allowed self-managed host to its config (base URL +
	// token). Hosts in AllowedHosts without an explicit HostTokens entry fall
	// back to the default token.
	hosts map[string]hostEntry

	preflight httpkit.PreflightCache
}

// New returns a Tracker. It fails fast when no token can be obtained so
// daemons crash at startup rather than at first issue lookup.
func New(opts Options) (*Tracker, error) {
	src := opts.Token
	if src == nil {
		return nil, ErrNoToken
	}
	if _, err := src.Token(context.Background()); err != nil {
		return nil, err
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}

	// Build per-host config for self-managed hosts.
	hosts := make(map[string]hostEntry, len(opts.AllowedHosts))
	for _, raw := range opts.AllowedHosts {
		h := scmgitlab.NormalizeHost(raw)
		if h == "" || scmgitlab.IsGitLabDotCom(h) {
			continue
		}
		he := hostEntry{
			baseURL: "https://" + h + "/api/v4",
			tokens:  src, // fall back to default token
		}
		if ts, ok := opts.HostTokens[h]; ok && ts != nil {
			he.tokens = ts
		}
		hosts[h] = he
	}

	t := &Tracker{
		http:        opts.HTTPClient,
		userAgent:   ua,
		defaultHost: hostEntry{baseURL: baseURL, tokens: src},
		hosts:       hosts,
	}
	if t.http == nil {
		t.http = &http.Client{Timeout: 30 * time.Second}
	}
	return t, nil
}

// configForHost returns the per-host config (base URL + token) for the given
// host. Host "" and "gitlab.com" use the default config (gitlab.com).
// Self-managed hosts must be in the allowlist; unconfigured hosts return an
// error so callers fail closed before any credential is attached.
func (t *Tracker) configForHost(host string) (hostEntry, error) {
	host = scmgitlab.NormalizeHost(host)
	if scmgitlab.IsGitLabDotCom(host) {
		return t.defaultHost, nil
	}
	if he, ok := t.hosts[host]; ok {
		return he, nil
	}
	return hostEntry{}, fmt.Errorf("gitlab tracker: host %q not in allowlist: %w", host, ErrHostNotAllowed)
}

// ConfigForHost returns nil if the given host is allowed by the tracker
// (gitlab.com or in AllowedHosts), or ErrHostNotAllowed otherwise. No
// network call is made — this is safe for use in wiring tests that need to
// assert host routing without DNS resolution.
func (t *Tracker) ConfigForHost(host string) error {
	_, err := t.configForHost(host)
	return err
}

// Statically assert Tracker satisfies the port. If this stops compiling, the
// port shape changed and the adapter needs to follow.
var _ ports.Tracker = (*Tracker)(nil)

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

// glIssue is the subset of fields we read off the GitLab issue payload.
type glIssue struct {
	IID         int      `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	WebURL      string   `json:"web_url"`
	Labels      []string `json:"labels"`
	Assignees   []glUser `json:"assignees"`
}

type glUser struct {
	Username string `json:"username"`
}

// Get fetches a single issue by id and maps it onto the normalized domain.Issue.
func (t *Tracker) Get(ctx context.Context, id domain.TrackerID) (domain.Issue, error) {
	projectPath, iid, err := t.parseID(id)
	if err != nil {
		return domain.Issue{}, err
	}
	he, err := t.configForHost(id.Host)
	if err != nil {
		return domain.Issue{}, err
	}
	path := fmt.Sprintf("/projects/%s/issues/%d", url.PathEscape(projectPath), iid)

	resp, err := t.do(ctx, he, http.MethodGet, path, nil)
	if err != nil {
		return domain.Issue{}, err
	}
	var raw glIssue
	if err := json.Unmarshal(resp, &raw); err != nil {
		return domain.Issue{}, fmt.Errorf("gitlab tracker: decode issue: %w", err)
	}
	issue := issueFromGitLab(projectPath, raw)
	issue.ID.Host = id.Host // preserve host for round-trip
	return issue, nil
}

// issueFromGitLab projects a raw GitLab issue payload into the normalized
// domain.Issue. projectPath is passed in because the TrackerID.Native
// shape is "path/to/project#iid" and we want the returned ID to round-trip
// through the same adapter. The caller sets Host on the returned Issue after
// this function returns.
func issueFromGitLab(projectPath string, raw glIssue) domain.Issue {
	assignees := make([]string, 0, len(raw.Assignees))
	for _, a := range raw.Assignees {
		assignees = append(assignees, a.Username)
	}
	out := domain.Issue{
		ID: domain.TrackerID{
			Provider: domain.TrackerProviderGitLab,
			Native:   fmt.Sprintf("%s#%d", projectPath, raw.IID),
		},
		Title:     raw.Title,
		Body:      raw.Description,
		State:     mapStateFromGitLab(raw.State),
		URL:       raw.WebURL,
		Labels:    raw.Labels,
		Assignees: assignees,
	}
	if len(out.Labels) == 0 {
		out.Labels = nil
	}
	if len(out.Assignees) == 0 {
		out.Assignees = nil
	}
	return out
}

// mapStateFromGitLab projects GitLab's opened/closed state onto the
// normalized state vocabulary.
func mapStateFromGitLab(state string) domain.NormalizedIssueState {
	if strings.EqualFold(state, "closed") {
		return domain.IssueDone
	}
	return domain.IssueOpen
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

// List returns issues for a project, filtered by state/labels/assignee.
// Pagination is followed via GitLab's Link-header (rel="next") until no next
// link remains; ListFilter.Limit, when set, caps the total accumulated
// issue count.
func (t *Tracker) List(ctx context.Context, repo domain.TrackerRepo, filter domain.ListFilter) ([]domain.Issue, error) {
	if repo.Provider != domain.TrackerProviderGitLab {
		return nil, fmt.Errorf("%w: provider=%q", ErrWrongProvider, repo.Provider)
	}
	projectPath, err := parseGitLabRepo(repo.Native)
	if err != nil {
		return nil, err
	}
	he, err := t.configForHost(repo.Host)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	switch filter.State {
	case domain.ListOpen:
		q.Set("state", "opened")
	case domain.ListClosed:
		q.Set("state", "closed")
	default:
		q.Set("state", "all")
	}
	if len(filter.Labels) > 0 {
		q.Set("labels", strings.Join(filter.Labels, ","))
	}
	if filter.Assignee != "" {
		q.Set("assignee_username", filter.Assignee)
	}
	q.Set("per_page", strconv.Itoa(listPageSize))

	path := fmt.Sprintf("/projects/%s/issues?%s", url.PathEscape(projectPath), q.Encode())
	out := make([]domain.Issue, 0)
	if filter.Limit > 0 {
		out = make([]domain.Issue, 0, filter.Limit)
	}
	for page := 0; path != ""; page++ {
		if page >= maxListPages {
			return nil, fmt.Errorf("gitlab tracker: list pagination exceeded %d pages", maxListPages)
		}
		respBody, nextPath, err := t.roundTrip(ctx, he, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var raw []glIssue
		if err := json.Unmarshal(respBody, &raw); err != nil {
			return nil, fmt.Errorf("gitlab tracker: decode list: %w", err)
		}
		pageIssues := make([]domain.Issue, 0, len(raw))
		for _, r := range raw {
			issue := issueFromGitLab(projectPath, r)
			issue.ID.Host = repo.Host // preserve host for round-trip
			pageIssues = append(pageIssues, issue)
		}
		var done bool
		out, done = httpkit.AppendIssuesWithLimit(out, pageIssues, filter.Limit)
		if done {
			break
		}
		path = nextPath
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

// Preflight verifies the configured token is currently accepted by GitLab
// (one GET /user). It does NOT prove the token has the scope or visibility
// needed for any specific Get/List call — those may still fail with
// ErrAuthFailed even after a successful Preflight.
//
// Successful checks are cached via httpkit.PreflightCache (double-checked
// atomic+mutex). Failures are intentionally NOT cached so a transient
// startup glitch is recoverable on a subsequent call.
func (t *Tracker) Preflight(ctx context.Context) error {
	return t.preflight.Run(ctx, func(ctx context.Context) error {
		// Preflight checks the default (gitlab.com) token. Self-managed host
		// tokens are validated on first use.
		_, err := t.do(ctx, t.defaultHost, http.MethodGet, "/user", nil)
		return err
	})
}

// ---------------------------------------------------------------------------
// HTTP plumbing
// ---------------------------------------------------------------------------

func (t *Tracker) do(ctx context.Context, he hostEntry, method, path string, body any) ([]byte, error) {
	respBody, _, err := t.roundTrip(ctx, he, method, path, body)
	return respBody, err
}

func (t *Tracker) roundTrip(ctx context.Context, he hostEntry, method, path string, body any) ([]byte, string, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, "", fmt.Errorf("gitlab tracker: encode body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, he.baseURL+path, rdr)
	if err != nil {
		return nil, "", fmt.Errorf("gitlab tracker: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", t.userAgent)
	tok, err := he.tokens.Token(ctx)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("gitlab tracker: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	nextPath := httpkit.ParseLinkNext(resp.Header.Get("Link"), he.baseURL)
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, "", fmt.Errorf("gitlab tracker: read response body: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return respBody, nextPath, nil
	}
	return respBody, nextPath, classifyError(resp, respBody)
}

func classifyError(resp *http.Response, body []byte) error {
	msg := httpkit.Message(body)
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	case http.StatusTooManyRequests:
		return httpkit.BuildRateLimitError(resp, msg, ErrRateLimited)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrAuthFailed, msg)
	}
	return fmt.Errorf("gitlab tracker: %d %s", resp.StatusCode, msg)
}

// ---------------------------------------------------------------------------
// ID parsing
// ---------------------------------------------------------------------------

func (t *Tracker) parseID(id domain.TrackerID) (projectPath string, iid int, err error) {
	if id.Provider != domain.TrackerProviderGitLab {
		return "", 0, fmt.Errorf("%w: provider=%q", ErrWrongProvider, id.Provider)
	}
	return parseGitLabID(id.Native)
}

// parseGitLabID accepts "path/to/project#IID" and returns the project path
// and issue iid. The project path may contain multiple segments for nested
// groups (e.g. "group/subgroup/project").
func parseGitLabID(native string) (projectPath string, iid int, err error) {
	hash := strings.IndexByte(native, '#')
	if hash < 0 {
		return "", 0, fmt.Errorf("%w: missing #iid", ErrBadID)
	}
	projectPath = native[:hash]
	iidStr := native[hash+1:]
	if err := validateProjectPath(projectPath); err != nil {
		return "", 0, err
	}
	n, parseErr := strconv.Atoi(iidStr)
	if parseErr != nil || n <= 0 {
		return "", 0, fmt.Errorf("%w: bad iid %q", ErrBadID, iidStr)
	}
	return projectPath, n, nil
}

// parseGitLabRepo accepts "path/to/project" and rejects empty strings,
// paths without a slash, and paths containing whitespace or "#".
func parseGitLabRepo(native string) (string, error) {
	if native == "" {
		return "", fmt.Errorf("%w: empty repo", ErrBadID)
	}
	if err := validateProjectPath(native); err != nil {
		return "", err
	}
	return native, nil
}

// validateProjectPath checks that a project path is non-empty, contains at
// least one "/" separator, and has no whitespace or "#" characters. It
// accepts paths with multiple segments (nested groups).
func validateProjectPath(path string) error {
	if path == "" {
		return fmt.Errorf("%w: empty project path", ErrBadID)
	}
	if !strings.Contains(path, "/") {
		return fmt.Errorf("%w: missing project path separator", ErrBadID)
	}
	if strings.ContainsAny(path, " \t\n\r#") {
		return fmt.Errorf("%w: invalid project path %q", ErrBadID, path)
	}
	return nil
}
