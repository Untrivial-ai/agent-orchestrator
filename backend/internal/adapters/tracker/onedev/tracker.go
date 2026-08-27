package onedev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/tracker/httpkit"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	defaultUserAgent = "ao-agent-orchestrator/tracker-onedev"

	// DefaultAssigneeField is the custom field the tracker reads assignees
	// from. OneDev has no built-in assignee: the stock issue template defines
	// a USER-list custom field named "Assignees", and an instance that renamed
	// it configures AO_ONEDEV_ISSUE_ASSIGNEE_FIELD.
	DefaultAssigneeField = "Assignees"

	// maxPageCount is OneDev's per-page ceiling. A larger count is rejected
	// with HTTP 406 ("Count should not be greater than 100"), so the tracker
	// clamps rather than letting the server reject the request.
	maxPageCount = 100

	// maxListPages bounds a single List walk so a pathological result set
	// cannot spin forever. At the page ceiling this still permits a
	// 2000-issue sweep before failing loud.
	maxListPages = 20

	// errorBodyMaxBytes caps how much of an error body is read before
	// classification. OneDev's own error bodies are short plain text, but a
	// proxy in front of an instance may return a large HTML page.
	errorBodyMaxBytes = 4096

	// messageMaxRunes bounds the error text carried into a wrapped error so
	// one malformed response cannot produce a log line of unbounded width.
	messageMaxRunes = 200
)

// Sentinel errors. Callers match on these via errors.Is; the orchestrator's
// lifecycle code is intentionally insulated from raw HTTP status codes.
var (
	ErrNotFound       = errors.New("onedev tracker: issue not found")
	ErrRateLimited    = errors.New("onedev tracker: rate limited")
	ErrAuthFailed     = errors.New("onedev tracker: authentication failed")
	ErrWrongProvider  = errors.New("onedev tracker: id is not a onedev tracker id")
	ErrBadID          = errors.New("onedev tracker: malformed native id")
	ErrHostNotAllowed = errors.New("onedev tracker: host not in allowlist")

	// ErrNoAllowedHosts mirrors the SCM provider's sentinel: OneDev has no
	// public instance, so an empty allowlist means the tracker has nowhere to
	// talk to. Construction fails loudly rather than yielding a tracker that
	// rejects every id it is later handed.
	ErrNoAllowedHosts = errors.New("onedev tracker: no allowed hosts configured (set AO_ONEDEV_ALLOWED_HOSTS)")
)

// RateLimitError is an alias for httpkit.RateLimitError so callers using
// errors.As with *RateLimitError work the same way across tracker adapters.
// OneDev itself does not throttle its REST API; this exists for a reverse
// proxy or WAF in front of an instance.
type RateLimitError = httpkit.RateLimitError

// Options configures a Tracker.
//
// AllowedHosts is required — see ErrNoAllowedHosts. HostTokens maps an allowed
// host to a credential override; hosts without an entry fall back to Token.
type Options struct {
	Token        TokenSource
	HTTPClient   *http.Client
	UserAgent    string
	AllowedHosts []string
	HostTokens   map[string]TokenSource

	// States maps this instance's issue state names onto the normalized
	// vocabulary. Nil uses DefaultStateMap.
	States StateMap

	// AssigneeField names the custom field carrying assignees. Empty uses
	// DefaultAssigneeField.
	AssigneeField string
}

// hostEntry pairs an allowlisted instance with the credential source used for
// it, so a credential is never carried across instances — OneDev user
// databases are per-instance.
type hostEntry struct {
	host   allowedHost
	tokens TokenSource
}

// Tracker implements ports.Tracker against OneDev's REST API.
//
// Construction is a local check only: the allowlist is parsed and a credential
// source must be present, but no credential is resolved and no network call is
// made. That matches how internal/daemon wires the OneDev SCM provider
// (SkipTokenPreflight), so a credential helper that is momentarily unavailable
// at daemon boot does not disable issue lookups for the life of the process.
type Tracker struct {
	http      *http.Client
	userAgent string

	// hosts maps an allowlisted authority to its entry.
	hosts map[string]hostEntry
	// byHostname indexes authorities by port-less hostname, so a repository
	// recorded against OneDev's git SSH port resolves to the instance
	// allowlisted under its HTTP API port. A hostname listed more than once is
	// ambiguous and is rejected rather than resolved arbitrarily.
	byHostname map[string][]string
	// order is the allowlist in stable sorted order, so Preflight's behaviour
	// does not depend on map iteration.
	order []string

	states        StateMap
	assigneeField string

	preflight httpkit.PreflightCache
}

// Statically assert Tracker satisfies the port. If this stops compiling, the
// port shape changed and the adapter needs to follow.
var _ ports.Tracker = (*Tracker)(nil)

// New returns a Tracker, failing fast on an empty or malformed host allowlist
// and on a missing credential source.
func New(opts Options) (*Tracker, error) {
	if opts.Token == nil {
		return nil, ErrNoToken
	}
	hosts := make(map[string]hostEntry, len(opts.AllowedHosts))
	byHostname := map[string][]string{}
	for _, raw := range opts.AllowedHosts {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		h, err := parseAllowedHost(raw)
		if err != nil {
			return nil, err
		}
		if prev, dup := hosts[h.authority]; dup {
			// The same instance listed twice under different schemes would
			// let config order decide whether the connection is encrypted.
			// Refuse rather than pick — the SCM provider refuses too, so the
			// two adapters cannot disagree about one allowlist.
			if prev.host.scheme != h.scheme {
				return nil, fmt.Errorf(
					"onedev tracker: host %q is listed with conflicting schemes %q and %q; list it once",
					h.authority, prev.host.scheme, h.scheme)
			}
			continue
		}
		hosts[h.authority] = hostEntry{host: h, tokens: opts.Token}
		byHostname[h.hostname()] = append(byHostname[h.hostname()], h.authority)
	}
	if len(hosts) == 0 {
		return nil, ErrNoAllowedHosts
	}

	// Per-host credential keys resolve the same way a repository host does, so
	// an entry written as "10.0.0.30" still selects the allowlisted
	// "10.0.0.30:6610". A key that names no configured host is dropped rather
	// than silently attached to the wrong instance.
	for _, raw := range sortedKeys(opts.HostTokens) {
		src := opts.HostTokens[raw]
		if src == nil {
			continue
		}
		authority, ok := resolveAuthority(hosts, byHostname, raw)
		if !ok {
			continue
		}
		entry := hosts[authority]
		entry.tokens = src
		hosts[authority] = entry
	}

	order := sortedKeys(hosts)
	for name := range byHostname {
		sort.Strings(byHostname[name])
	}

	states := opts.States
	if len(states) == 0 {
		states = DefaultStateMap()
	}
	assignee := strings.TrimSpace(opts.AssigneeField)
	if assignee == "" {
		assignee = DefaultAssigneeField
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = defaultUserAgent
	}
	return &Tracker{
		http:          client,
		userAgent:     ua,
		hosts:         hosts,
		byHostname:    byHostname,
		order:         order,
		states:        states,
		assigneeField: assignee,
	}, nil
}

// AllowedHosts returns the configured allowlist in stable order, rendered as
// scheme-qualified entries.
func (t *Tracker) AllowedHosts() []string {
	out := make([]string, 0, len(t.order))
	for _, authority := range t.order {
		out = append(out, t.hosts[authority].host.String())
	}
	return out
}

// ConfigForHost reports nil when the given host resolves to an allowlisted
// instance and ErrHostNotAllowed otherwise. No network call is made, so wiring
// tests can assert host routing without DNS.
func (t *Tracker) ConfigForHost(host string) error {
	_, err := t.entryForHost(host)
	return err
}

// entryForHost resolves a TrackerID/TrackerRepo host onto an allowlisted
// instance. Unlike GitLab there is no default: a blank host cannot mean
// "the public instance", because there isn't one. A blank host resolves only
// when exactly one instance is configured, which is the single-instance
// deployment every self-hosted estate starts as.
func (t *Tracker) entryForHost(host string) (hostEntry, error) {
	if strings.TrimSpace(host) == "" {
		if len(t.order) == 1 {
			return t.hosts[t.order[0]], nil
		}
		return hostEntry{}, fmt.Errorf(
			"onedev tracker: no host on tracker id and %d instances configured: %w",
			len(t.order), ErrHostNotAllowed)
	}
	authority, ok := resolveAuthority(t.hosts, t.byHostname, host)
	if !ok {
		return hostEntry{}, fmt.Errorf("onedev tracker: host %q: %w", host, ErrHostNotAllowed)
	}
	return t.hosts[authority], nil
}

// resolveAuthority maps a host string onto an allowlisted authority. An exact
// authority match wins; failing that the port-less hostname is matched, since
// OneDev serves git and its API on different ports. An ambiguous hostname is
// rejected rather than guessed.
func resolveAuthority(hosts map[string]hostEntry, byHostname map[string][]string, host string) (string, bool) {
	key := normalizeHostKey(host)
	if key == "" {
		return "", false
	}
	if _, ok := hosts[key]; ok {
		return key, true
	}
	matches := byHostname[hostnameOf(key)]
	if len(matches) != 1 {
		return "", false
	}
	return matches[0], true
}

// ---------------------------------------------------------------------------
// REST payloads
// ---------------------------------------------------------------------------

// restIssue is the subset of OneDev's issue payload AO consumes. Note that
// "id" (the internal entity id every /issues/{id} route is keyed by) is
// distinct from "number" (the per-project number users see and that appears in
// a TrackerID).
type restIssue struct {
	ID          int64  `json:"id"`
	Number      int    `json:"number"`
	State       string `json:"state"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ProjectID   int64  `json:"projectId"`
}

// ---------------------------------------------------------------------------
// Query construction
// ---------------------------------------------------------------------------

// quoteQueryValue renders a value for OneDev's query DSL, which quotes with
// double quotes and escapes with a backslash. Project paths and state names do
// not normally contain either, but the value is interpolated into a query the
// server parses, so it is escaped rather than trusted.
func quoteQueryValue(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// listQuery builds the OneDev issue query for a List call, or reports
// ok=false when the filter cannot match anything and no request should be
// made.
//
// The state criteria are the part worth reading twice. A bare "open" criteria
// — which works for pull requests — is rejected for issues with HTTP 406
// "Invalid query", so state is always expressed through the "State" field. An
// unknown state *value* is not an error (it simply matches nothing), which is
// what makes emitting an operator-configured state name safe.
func (t *Tracker) listQuery(projectPath string, filter domain.ListFilter) (string, bool) {
	criteria := []string{`"Project" is ` + quoteQueryValue(projectPath)}

	terminal := t.states.TerminalStates()
	switch filter.State {
	case domain.ListOpen:
		// Exclude the finished states rather than enumerate the live ones, so
		// a state this instance defines but nobody mapped stays in the result
		// — the same direction as UnmappedState.
		for _, state := range terminal {
			criteria = append(criteria, `"State" is not `+quoteQueryValue(state))
		}
	case domain.ListClosed:
		if len(terminal) == 0 {
			// No state maps to done or cancelled, so nothing can be closed.
			// Emitting no criteria here would return every issue instead.
			return "", false
		}
		ors := make([]string, 0, len(terminal))
		for _, state := range terminal {
			ors = append(ors, `"State" is `+quoteQueryValue(state))
		}
		criteria = append(criteria, "("+strings.Join(ors, " or ")+")")
	}

	if c, ok := t.assigneeCriteria(filter.Assignee); ok {
		criteria = append(criteria, c)
	}

	// Newest-touched first, so a Limit truncates the stale tail rather than
	// an arbitrary slice.
	return strings.Join(criteria, " and ") + ` order by "Last Activity Date" desc`, true
}

// assigneeCriteria translates ListFilter.Assignee into a criterion on the
// configured assignee field. The provider-neutral wildcards intake uses ("*"
// for "anyone", "none" for "nobody") map onto OneDev's empty-value operators;
// any other value is matched literally.
func (t *Tracker) assigneeCriteria(assignee string) (string, bool) {
	assignee = strings.TrimSpace(assignee)
	field := quoteQueryValue(t.assigneeField)
	switch {
	case assignee == "":
		return "", false
	case assignee == "*":
		return field + " is not empty", true
	case strings.EqualFold(assignee, "none"):
		return field + " is empty", true
	default:
		return field + " is " + quoteQueryValue(assignee), true
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

// Get fetches a single issue by id and maps it onto the normalized
// domain.Issue.
//
// The lookup goes through the query API rather than GET /~api/issues/{id}:
// that route takes OneDev's internal entity id, while a TrackerID carries the
// per-project number users see. The query returns the full issue, so resolving
// the number costs no extra round-trip.
func (t *Tracker) Get(ctx context.Context, id domain.TrackerID) (domain.Issue, error) {
	if id.Provider != domain.TrackerProviderOneDev {
		return domain.Issue{}, fmt.Errorf("%w: provider=%q", ErrWrongProvider, id.Provider)
	}
	projectPath, number, err := parseNativeID(id.Native)
	if err != nil {
		return domain.Issue{}, err
	}
	entry, err := t.entryForHost(id.Host)
	if err != nil {
		return domain.Issue{}, err
	}
	q := url.Values{
		"query": {`"Project" is ` + quoteQueryValue(projectPath) +
			` and "Number" is ` + quoteQueryValue(strconv.Itoa(number))},
		"offset": {"0"},
		"count":  {"1"},
	}
	body, err := t.get(ctx, entry, "/issues", q)
	if err != nil {
		return domain.Issue{}, err
	}
	var raw []restIssue
	if err := json.Unmarshal(body, &raw); err != nil {
		return domain.Issue{}, fmt.Errorf("onedev tracker: decode issue: %w", err)
	}
	if len(raw) == 0 {
		return domain.Issue{}, fmt.Errorf("%w: %s#%d", ErrNotFound, projectPath, number)
	}
	return t.buildIssue(ctx, entry, projectPath, raw[0])
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

// List returns a project's issues, filtered by state/labels/assignee.
// Pagination walks OneDev's offset/count paging until a short page arrives —
// there is no Link header to follow. ListFilter.Limit caps the total.
func (t *Tracker) List(ctx context.Context, repo domain.TrackerRepo, filter domain.ListFilter) ([]domain.Issue, error) {
	if repo.Provider != domain.TrackerProviderOneDev {
		return nil, fmt.Errorf("%w: provider=%q", ErrWrongProvider, repo.Provider)
	}
	projectPath, err := parseProjectPath(repo.Native)
	if err != nil {
		return nil, err
	}
	entry, err := t.entryForHost(repo.Host)
	if err != nil {
		return nil, err
	}
	query, ok := t.listQuery(projectPath, filter)
	if !ok {
		return []domain.Issue{}, nil
	}

	count := maxPageCount
	if filter.Limit > 0 && filter.Limit < count {
		count = filter.Limit
	}
	out := make([]domain.Issue, 0, count)
	offset := 0
	for page := 0; page < maxListPages; page++ {
		q := url.Values{
			"query":  {query},
			"offset": {strconv.Itoa(offset)},
			"count":  {strconv.Itoa(count)},
		}
		body, err := t.get(ctx, entry, "/issues", q)
		if err != nil {
			return nil, err
		}
		var raw []restIssue
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("onedev tracker: decode list: %w", err)
		}
		for _, r := range raw {
			issue, err := t.buildIssue(ctx, entry, projectPath, r)
			if err != nil {
				return nil, err
			}
			// Label matching is client-side: OneDev has no issue labels, and
			// the custom fields they are synthesised from are named per
			// instance, so a server-side criterion would risk the
			// "Field not found" 500 on every poll.
			if !matchesLabels(issue.Labels, filter.Labels) {
				continue
			}
			out = append(out, issue)
			if filter.Limit > 0 && len(out) >= filter.Limit {
				return out, nil
			}
		}
		if len(raw) < count {
			return out, nil
		}
		offset += len(raw)
	}
	return nil, fmt.Errorf("onedev tracker: list pagination exceeded %d pages", maxListPages)
}

// matchesLabels reports whether an issue's synthesised labels satisfy every
// requested label. A request matches either the whole "Field: Value" label or
// just its value, case-insensitively, so a caller can ask for "bug" without
// knowing the instance calls the field "Type".
func matchesLabels(have, want []string) bool {
	for _, w := range want {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if !hasLabel(have, w) {
			return false
		}
	}
	return true
}

func hasLabel(have []string, want string) bool {
	for _, label := range have {
		if strings.EqualFold(strings.TrimSpace(label), want) {
			return true
		}
		if _, value, ok := strings.Cut(label, ": "); ok && strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Issue projection
// ---------------------------------------------------------------------------

// buildIssue projects a raw OneDev issue plus its custom fields onto the
// normalized domain.Issue.
//
// The custom-field fetch is not best-effort. Issue.Assignees decides intake
// eligibility, and a swallowed failure would present an assigned issue as
// unassigned — which, for a project configured with assignee "none", would
// spawn a session for every issue on the board.
func (t *Tracker) buildIssue(ctx context.Context, entry hostEntry, projectPath string, raw restIssue) (domain.Issue, error) {
	fields, err := t.issueFields(ctx, entry, raw.ID)
	if err != nil {
		return domain.Issue{}, err
	}
	assignees, labels := t.projectFields(fields)
	state, _ := t.states.Normalize(raw.State)
	return domain.Issue{
		ID: domain.TrackerID{
			Provider: domain.TrackerProviderOneDev,
			Native:   fmt.Sprintf("%s#%d", projectPath, raw.Number),
			Host:     entry.host.authority,
		},
		Title:     raw.Title,
		Body:      raw.Description,
		State:     state,
		URL:       issueURL(entry.host, projectPath, raw.Number),
		Labels:    labels,
		Assignees: assignees,
	}, nil
}

// issueURL builds the browser URL for an issue. OneDev reserves the "~" prefix
// for its own routes, so a project path can never shadow "~issues".
func issueURL(h allowedHost, projectPath string, number int) string {
	return h.webBase() + "/" + projectPath + "/~issues/" + strconv.Itoa(number)
}

// issueFields fetches an issue's custom fields. Values are a string, a list of
// strings, or null depending on the field's type, so they are decoded loosely
// and flattened.
func (t *Tracker) issueFields(ctx context.Context, entry hostEntry, issueID int64) (map[string]any, error) {
	body, err := t.get(ctx, entry, "/issues/"+strconv.FormatInt(issueID, 10)+"/fields", nil)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("onedev tracker: decode issue fields: %w", err)
	}
	return fields, nil
}

// projectFields splits an issue's custom fields into assignees and labels.
// Field order is sorted so two calls on the same issue produce the same
// Labels slice.
func (t *Tracker) projectFields(fields map[string]any) (assignees, labels []string) {
	for _, name := range sortedKeys(fields) {
		values := fieldValues(fields[name])
		if len(values) == 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), t.assigneeField) {
			assignees = append(assignees, values...)
			continue
		}
		for _, v := range values {
			labels = append(labels, name+": "+v)
		}
	}
	return assignees, labels
}

// fieldValues flattens one custom-field value into zero or more strings.
// OneDev renders a single-valued field as a scalar and a multi-valued one as
// an array, and JSON numbers arrive as float64.
func fieldValues(v any) []string {
	switch val := v.(type) {
	case nil:
		return nil
	case string:
		if s := strings.TrimSpace(val); s != "" {
			return []string{s}
		}
		return nil
	case bool:
		return []string{strconv.FormatBool(val)}
	case float64:
		return []string{strconv.FormatFloat(val, 'f', -1, 64)}
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			out = append(out, fieldValues(item)...)
		}
		return out
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

// Preflight verifies the configured credential is accepted by every allowlisted
// instance, with one cheap authenticated issue query each. It does NOT prove
// the credential can see any particular project — those calls may still fail
// with ErrAuthFailed after a successful Preflight.
//
// Errors from all instances are joined so one broken instance's message is not
// hidden by another's. Success is cached; failures are not, so a transient
// startup glitch is recoverable on a later call.
func (t *Tracker) Preflight(ctx context.Context) error {
	return t.preflight.Run(ctx, func(ctx context.Context) error {
		var errs []error
		for _, authority := range t.order {
			q := url.Values{"offset": {"0"}, "count": {"1"}}
			if _, err := t.get(ctx, t.hosts[authority], "/issues", q); err != nil {
				errs = append(errs, fmt.Errorf("onedev tracker: preflight %s: %w", authority, err))
			}
		}
		return errors.Join(errs...)
	})
}

// ---------------------------------------------------------------------------
// HTTP plumbing
// ---------------------------------------------------------------------------

func (t *Tracker) get(ctx context.Context, entry hostEntry, path string, q url.Values) ([]byte, error) {
	u := entry.host.apiBase() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("onedev tracker: build %s request: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", t.userAgent)
	if entry.tokens == nil {
		return nil, ErrNoToken
	}
	cred, err := entry.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	if err := applyCredential(req, cred); err != nil {
		return nil, err
	}

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("onedev tracker: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyMaxBytes))
		return nil, t.noteAuthFailure(entry, classifyError(resp, body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("onedev tracker: read %s body: %w", path, err)
	}
	return body, nil
}

// noteAuthFailure drops the cached credential when OneDev rejects it, so a
// rotated token is picked up on the next call instead of failing until the
// daemon restarts.
func (t *Tracker) noteAuthFailure(entry hostEntry, err error) error {
	if !errors.Is(err, ErrAuthFailed) {
		return err
	}
	if inv, ok := entry.tokens.(tokenInvalidator); ok {
		inv.InvalidateToken()
	}
	return err
}

func classifyError(resp *http.Response, body []byte) error {
	msg := errorMessage(body)
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrAuthFailed, msg)
	case http.StatusTooManyRequests:
		return httpkit.BuildRateLimitError(resp, msg, ErrRateLimited)
	}
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("onedev tracker: %d %s", resp.StatusCode, msg)
}

// errorMessage extracts a short human-readable message from an error body.
// OneDev answers most failures in plain text ("Invalid query") but some in
// JSON, so both are handled; the result is truncated so a stray HTML page
// cannot widen a log line without bound.
func errorMessage(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var v struct {
		ErrorMessage string `json:"errorMessage"`
		Message      string `json:"message"`
		Error        string `json:"error"`
	}
	if json.Unmarshal(body, &v) == nil {
		for _, cand := range []string{v.ErrorMessage, v.Message, v.Error} {
			if cand = strings.TrimSpace(cand); cand != "" {
				return truncate(cand)
			}
		}
	}
	return truncate(strings.Join(strings.Fields(trimmed), " "))
}

func truncate(s string) string {
	runes := []rune(s)
	if len(runes) <= messageMaxRunes {
		return s
	}
	return string(runes[:messageMaxRunes]) + "..."
}

// ---------------------------------------------------------------------------
// ID parsing
// ---------------------------------------------------------------------------

// parseNativeID accepts "<project path>#<number>". Unlike GitLab's
// owner/project pair a OneDev project path may be a single segment
// ("productone") or nested to any depth ("Homelab/tools/curatarr"), because
// OneDev projects form a tree.
func parseNativeID(native string) (projectPath string, number int, err error) {
	hash := strings.IndexByte(native, '#')
	if hash < 0 {
		return "", 0, fmt.Errorf("%w: missing #number", ErrBadID)
	}
	projectPath, err = parseProjectPath(native[:hash])
	if err != nil {
		return "", 0, err
	}
	raw := native[hash+1:]
	n, parseErr := strconv.Atoi(raw)
	if parseErr != nil || n <= 0 {
		return "", 0, fmt.Errorf("%w: bad issue number %q", ErrBadID, raw)
	}
	return projectPath, n, nil
}

// parseProjectPath validates a OneDev project path. Empty, "." and ".."
// segments are rejected so a traversal-shaped path cannot reach a query or a
// browser URL.
func parseProjectPath(raw string) (string, error) {
	p := strings.Trim(strings.TrimSpace(raw), "/")
	if p == "" {
		return "", fmt.Errorf("%w: empty project path", ErrBadID)
	}
	if strings.ContainsAny(p, " \t\n\r#?") {
		return "", fmt.Errorf("%w: invalid project path %q", ErrBadID, raw)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("%w: invalid project path %q", ErrBadID, raw)
		}
	}
	return p, nil
}
