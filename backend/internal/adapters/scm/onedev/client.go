package onedev

import (
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

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	// ErrNotFound is returned when a OneDev API resource does not exist.
	ErrNotFound = ports.ErrSCMNotFound
	// ErrRateLimited is returned when OneDev responds with HTTP 429. Callers
	// needing structured retry hints should use errors.As to extract a
	// *RateLimitError.
	ErrRateLimited = errors.New("onedev scm: rate limited")
	// ErrNoAPIBase is returned by NewClient when no API base URL was supplied.
	// OneDev is always self-hosted, so there is no default to fall back on.
	ErrNoAPIBase = errors.New("onedev scm: no API base URL configured")
)

const (
	// defaultUserAgent identifies AO's OneDev traffic in instance logs.
	defaultUserAgent = "ao-onedev-scm/1"
	// defaultHTTPTimeout bounds every API call so a hung OneDev instance
	// cannot block the observer's polling goroutine indefinitely.
	defaultHTTPTimeout = 30 * time.Second
	// maxPageCount is OneDev's per-page ceiling. A larger count is rejected
	// with HTTP 406 ("Count should not be greater than 100"), so the client
	// clamps rather than letting the server reject the request.
	maxPageCount = 100
	// maxPaginationPages bounds a single paginated walk so a pathological
	// result set cannot spin forever. Hitting it marks the result truncated.
	maxPaginationPages = 10
	// errorBodyMaxBytes caps how much of an error response body is read
	// before classification. OneDev's error bodies are short plain-text
	// strings, but a proxy in front of it may return a large HTML page.
	errorBodyMaxBytes = 4096
	// messageMaxRunes bounds the error text carried into a wrapped error, so
	// one malformed response cannot produce a log line of unbounded width.
	messageMaxRunes = 200
)

// RateLimitError carries the backoff hints from a 429 response. Callers that
// only need the category use errors.Is(err, ErrRateLimited); callers needing
// the exact backoff use errors.As. The getters match the shape the
// provider-neutral observer's cooldown helper expects.
type RateLimitError struct {
	ResetAt    time.Time
	RetryAfter time.Duration
	Message    string
}

// Error formats the rate-limit error for logs.
func (e *RateLimitError) Error() string {
	if e == nil {
		return ErrRateLimited.Error()
	}
	if e.Message != "" {
		return "onedev scm: rate limited: " + e.Message
	}
	return ErrRateLimited.Error()
}

// Is lets errors.Is match a *RateLimitError against ErrRateLimited.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// GetRetryAfter exposes the Retry-After hint.
func (e *RateLimitError) GetRetryAfter() time.Duration {
	if e == nil {
		return 0
	}
	return e.RetryAfter
}

// GetResetAt exposes the reset-time hint.
func (e *RateLimitError) GetResetAt() time.Time {
	if e == nil {
		return time.Time{}
	}
	return e.ResetAt
}

// APIResponse is the normalised result of a OneDev API call.
//
// Unlike the GitHub and GitLab adapters there is no ETag or NotModified
// field: OneDev sends neither ETag nor Last-Modified, so there is no
// transport-level validator to carry. See the package doc.
type APIResponse struct {
	StatusCode int
	Body       []byte
}

// ClientOptions configures the OneDev HTTP client.
type ClientOptions struct {
	HTTPClient *http.Client
	Token      TokenSource
	// APIBase is the full REST root of one instance, including the "/~api"
	// suffix — e.g. "http://10.0.0.30:6610/~api". Required.
	APIBase   string
	UserAgent string
}

// Client wraps one OneDev instance's REST API. It handles auth and error
// classification. Each instance gets its own Client so a credential is never
// carried across hosts.
type Client struct {
	http      *http.Client
	tokens    TokenSource
	apiBase   string
	userAgent string
}

// NewClient creates a OneDev API client. APIBase is required: OneDev has no
// public instance, so there is no sensible default and guessing one would
// point traffic at a host the operator never configured.
func NewClient(opts ClientOptions) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.APIBase), "/")
	if base == "" {
		return nil, ErrNoAPIBase
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultHTTPTimeout}
	}
	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = defaultUserAgent
	}
	return &Client{http: hc, tokens: opts.Token, apiBase: base, userAgent: ua}, nil
}

// APIBase returns the REST root this client talks to.
func (c *Client) APIBase() string { return c.apiBase }

// Preflight verifies the configured credential against this instance by
// listing a single project. GET /~api/projects is cheap, requires
// authentication, and is available to every account, which makes it a good
// liveness-plus-auth check.
//
// The returned error is classified: ErrNoToken when nothing is configured,
// ErrAuthFailed when OneDev rejects the credential, and a wrapped transport
// error otherwise — so the daemon's wiring can distinguish "not configured"
// from "configured wrongly" from "instance unreachable".
func (c *Client) Preflight(ctx context.Context) error {
	q := url.Values{"offset": {"0"}, "count": {"1"}}
	if _, err := c.doGET(ctx, "/projects", q); err != nil {
		return fmt.Errorf("onedev scm: preflight %s/projects: %w", c.apiBase, err)
	}
	return nil
}

// doGET performs an authenticated GET and returns the decoded-ready body.
//
// There is no conditional-request path: OneDev emits no ETag or
// Last-Modified, so every call is a full fetch.
func (c *Client) doGET(ctx context.Context, path string, q url.Values) (APIResponse, error) {
	req, err := c.newRequest(ctx, path, q)
	if err != nil {
		return APIResponse{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return APIResponse{}, fmt.Errorf("onedev scm: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyMaxBytes))
		return APIResponse{StatusCode: resp.StatusCode}, c.noteAuthFailure(classifyError(resp, body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return APIResponse{StatusCode: resp.StatusCode}, fmt.Errorf("onedev scm: read %s body: %w", path, err)
	}
	return APIResponse{StatusCode: resp.StatusCode, Body: body}, nil
}

// doGETStream performs an authenticated GET and hands the caller the still-open
// response body.
//
// It exists for /~api/streaming/build-logs/{buildId}, whose response is an
// unbounded length-prefixed frame stream rather than a document. doGET would
// buffer the whole thing before the caller saw a byte; here the caller decodes
// incrementally and closes the body when it has what it needs. The caller owns
// the returned ReadCloser and must close it.
func (c *Client) doGETStream(ctx context.Context, path string, q url.Values) (io.ReadCloser, error) {
	req, err := c.newRequest(ctx, path, q)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("onedev scm: GET %s: %w", path, err)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyMaxBytes))
		_ = resp.Body.Close()
		return nil, c.noteAuthFailure(classifyError(resp, body))
	}
	return resp.Body, nil
}

// doGETPaginated walks a OneDev listing endpoint with offset/count paging,
// invoking handler once per page with that page's raw JSON body. The handler
// returns how many items the page held; a page shorter than the requested
// count ends the walk, since OneDev sends no Link header to follow.
//
// count is clamped to maxPageCount because OneDev rejects a larger value with
// HTTP 406. The bool result reports that maxPaginationPages was reached and
// the result is therefore truncated.
func (c *Client) doGETPaginated(ctx context.Context, path string, q url.Values, handler func(body []byte) (int, error)) (bool, error) {
	page := url.Values{}
	for k, vs := range q {
		page[k] = append([]string(nil), vs...)
	}
	count := maxPageCount
	if raw := page.Get("count"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < maxPageCount {
			count = n
		}
	}
	offset := 0
	if raw := page.Get("offset"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			offset = n
		}
	}
	page.Set("count", strconv.Itoa(count))

	for i := 0; i < maxPaginationPages; i++ {
		page.Set("offset", strconv.Itoa(offset))
		resp, err := c.doGET(ctx, path, page)
		if err != nil {
			return false, err
		}
		n, err := handler(resp.Body)
		if err != nil {
			return false, err
		}
		if n < count {
			return false, nil
		}
		offset += n
	}
	return true, nil
}

func (c *Client) newRequest(ctx context.Context, path string, q url.Values) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(path, q), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("onedev scm: build %s request: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}
	return req, nil
}

// authorize resolves and attaches the credential. A client with no token
// source is an error rather than an anonymous request: OneDev's API requires
// authentication, so an unauthenticated call only produces a confusing 401.
func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	if c.tokens == nil {
		return ErrNoToken
	}
	cred, err := c.tokens.Token(ctx)
	if err != nil {
		return err
	}
	return cred.apply(req)
}

// noteAuthFailure drops the cached credential when OneDev rejects it, so a
// rotated token is picked up on the next call instead of failing until
// restart.
func (c *Client) noteAuthFailure(err error) error {
	if !errors.Is(err, ErrAuthFailed) {
		return err
	}
	if inv, ok := c.tokens.(tokenInvalidator); ok {
		inv.InvalidateToken()
	}
	return err
}

func (c *Client) apiURL(path string, q url.Values) string {
	u := c.apiBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// classifyError maps a OneDev error response onto this package's sentinels.
func classifyError(resp *http.Response, body []byte) error {
	switch resp.StatusCode {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		if msg := errorMessage(body); msg != "" {
			return fmt.Errorf("%w: %s", ErrAuthFailed, msg)
		}
		return ErrAuthFailed
	case http.StatusTooManyRequests:
		return rateLimited(resp, body)
	default:
		msg := errorMessage(body)
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("onedev scm: %s", msg)
	}
}

// rateLimited builds a *RateLimitError from a 429, parsing Retry-After
// (seconds or an HTTP date) so a caller can apply a cooldown instead of
// hammering the instance.
func rateLimited(resp *http.Response, body []byte) error {
	e := &RateLimitError{Message: errorMessage(body)}
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if sec, err := strconv.Atoi(ra); err == nil && sec >= 0 {
			e.RetryAfter = time.Duration(sec) * time.Second
		} else if when, err := http.ParseTime(ra); err == nil {
			e.ResetAt = when
		}
	}
	return e
}

// errorMessage extracts a short human-readable message from an error body.
// OneDev returns plain text for most failures (for example "Invalid account
// or incorrect credentials") but JSON for some, so both are handled. The
// result is truncated so a stray HTML page cannot widen a log line without
// bound.
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
