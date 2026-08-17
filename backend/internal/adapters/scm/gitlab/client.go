package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	// ErrNotFound is returned when a GitLab API resource does not exist.
	ErrNotFound = ports.ErrSCMNotFound
	// ErrRateLimited is returned when GitLab responds with HTTP 429.
	// Callers needing structured retry hints (Retry-After / RateLimit-Reset)
	// should use errors.As to extract a *RateLimitError.
	ErrRateLimited = fmt.Errorf("gitlab scm: rate limited")
)

// RateLimitError carries the structured backoff hints from a GitLab 429
// response. GitLab sends Retry-After (seconds) and/or RateLimit-Reset (Unix
// epoch seconds) headers; AO uses these to apply a provider-level cooldown so
// the observer does not keep polling every 30s while rate-limited (review
// finding #4). Callers that only need the category use errors.Is(err,
// ErrRateLimited); callers needing the exact backoff use errors.As.
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
		return "gitlab scm: rate limited: " + e.Message
	}
	return ErrRateLimited.Error()
}

// Is lets errors.Is match a *RateLimitError against ErrRateLimited.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// GetRetryAfter exposes the Retry-After hint for the provider-neutral observer's
// rateLimitCooldown helper.
func (e *RateLimitError) GetRetryAfter() time.Duration {
	if e == nil {
		return 0
	}
	return e.RetryAfter
}

// GetResetAt exposes the RateLimit-Reset hint for the provider-neutral
// observer's rateLimitCooldown helper.
func (e *RateLimitError) GetResetAt() time.Time {
	if e == nil {
		return time.Time{}
	}
	return e.ResetAt
}

const (
	defaultRESTBaseURL = "https://gitlab.com/api/v4"
	cacheMaxEntries    = 512
	defaultUserAgent   = "ao-gitlab-scm/1"
	// defaultHTTPTimeout bounds every REST call so a hung GitLab API endpoint
	// does not block the observer's polling goroutine indefinitely (review
	// finding #4). Matches the observer's DefaultTickInterval.
	defaultHTTPTimeout = 30 * time.Second
	// jobLogTailMaxBytes bounds how much of a job trace body doGETRaw retains.
	// Job traces (CI logs) can be very large, but callers only keep the last
	// ciFailureLogTailLines lines — streaming through a fixed-size rolling tail
	// means one pathological trace cannot create unbounded memory pressure
	// (review Item 12). 64 KB gives ~6-30x headroom over 20 typical CI log
	// lines (2-10 KB). Matches the existing tail-lines-constant pattern; no
	// env surface (the bound rarely needs tuning).
	jobLogTailMaxBytes = 65536
	// errorBodyMaxBytes caps how much of an error response body (status >= 400)
	// is read before classification. Error payloads are only used to extract a
	// short message; a multi-MB HTML error page must not be buffered in full.
	errorBodyMaxBytes = 4096
)

// RESTResponse is the normalised result of a GitLab REST call.
type RESTResponse struct {
	StatusCode  int
	NotModified bool
	ETag        string
	Body        []byte
}

// ClientOptions configures the GitLab HTTP client.
type ClientOptions struct {
	HTTPClient *http.Client
	Token      TokenSource
	RESTBase   string
	UserAgent  string
}

// Client wraps the GitLab REST API v4. It handles auth, ETag caching, and
// error classification.
type Client struct {
	http      *http.Client
	tokens    TokenSource
	restBase  string
	userAgent string

	mu       sync.Mutex
	etagOut  map[string]string
	bodyOut  map[string][]byte
	cacheLRU []string
}

// NewClient creates a new GitLab REST client.
func NewClient(opts ClientOptions) *Client {
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultHTTPTimeout}
	}
	base := opts.RESTBase
	if base == "" {
		base = defaultRESTBaseURL
	}
	base = strings.TrimRight(base, "/")
	ua := opts.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	return &Client{
		http:      hc,
		tokens:    opts.Token,
		restBase:  base,
		userAgent: ua,
		etagOut:   make(map[string]string, cacheMaxEntries),
		bodyOut:   make(map[string][]byte, cacheMaxEntries),
	}
}

// doRESTWithETag performs a GET with an externally-managed ETag. It does NOT
// use the client's internal cache (the observer manages its own ETags).
func (c *Client) doRESTWithETag(ctx context.Context, path string, q url.Values, etag string) (RESTResponse, error) {
	u := c.restURL(path, q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return RESTResponse{}, err
	}
	if err := c.authorize(ctx, req); err != nil {
		return RESTResponse{}, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return RESTResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotModified {
		return RESTResponse{StatusCode: 304, NotModified: true, ETag: etag}, nil
	}
	if resp.StatusCode >= 400 {
		return RESTResponse{StatusCode: resp.StatusCode}, classifyError(resp, body)
	}
	return RESTResponse{
		StatusCode: resp.StatusCode,
		ETag:       resp.Header.Get("ETag"),
		Body:       body,
	}, nil
}

// doMERGE performs a PUT request with an optional JSON body and query
// parameters. Unlike doGET it does not participate in the ETag cache (mutating
// methods must not replay cached responses). The response body and status code
// are returned for caller-side interpretation.
func (c *Client) doMERGE(ctx context.Context, path string, q url.Values, body any) (RESTResponse, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return RESTResponse{}, fmt.Errorf("gitlab scm: encode %s body: %w", path, err)
		}
		rdr = bytes.NewReader(b)
	}

	u := c.restURL(path, q)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, rdr)
	if err != nil {
		return RESTResponse{}, fmt.Errorf("gitlab scm: build %s request: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", c.userAgent)
	if err := c.authorize(ctx, req); err != nil {
		return RESTResponse{}, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return RESTResponse{}, fmt.Errorf("gitlab scm: PUT %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return RESTResponse{StatusCode: resp.StatusCode, Body: b}, classifyError(resp, b)
	}
	return RESTResponse{StatusCode: resp.StatusCode, Body: b}, nil
}

// doGET performs a GET request using the client's internal ETag cache.
func (c *Client) doGET(ctx context.Context, path string, q url.Values) (RESTResponse, error) {
	u := c.restURL(path, q)
	cacheKey := u
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return RESTResponse{}, err
	}
	if err := c.authorize(ctx, req); err != nil {
		return RESTResponse{}, err
	}
	req.Header.Set("User-Agent", c.userAgent)

	c.mu.Lock()
	if etag, ok := c.etagOut[cacheKey]; ok {
		req.Header.Set("If-None-Match", etag)
	}
	c.mu.Unlock()

	resp, err := c.http.Do(req)
	if err != nil {
		return RESTResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotModified {
		c.mu.Lock()
		cached := c.bodyOut[cacheKey]
		c.mu.Unlock()
		return RESTResponse{StatusCode: 200, NotModified: true, ETag: resp.Header.Get("ETag"), Body: cached}, nil
	}
	if resp.StatusCode >= 400 {
		return RESTResponse{StatusCode: resp.StatusCode}, classifyError(resp, body)
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		c.storeCacheEntry(cacheKey, etag, body)
	}
	return RESTResponse{
		StatusCode: resp.StatusCode,
		ETag:       resp.Header.Get("ETag"),
		Body:       body,
	}, nil
}

// doGETRaw performs a GET and returns the raw body bytes (e.g. job trace).
//
// The response body is streamed through a fixed-size rolling tail of
// jobLogTailMaxBytes (64 KB) so one pathological trace cannot create
// unbounded memory pressure (review Item 12). Callers that retain only the
// last N lines (see FetchFailedCheckLogTail) operate on this bounded buffer
// afterward — graceful degradation: if the last 20 lines exceed 64 KB (very
// long lines), the tail contains fewer than 20 lines (whatever fits). Error
// response bodies (status >= 400) are capped at errorBodyMaxBytes before
// classification, since only the short message is needed.
func (c *Client) doGETRaw(ctx context.Context, path string) ([]byte, error) {
	u := c.restURL(path, nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		// Error bodies are only used to extract a short message for
		// classification; cap the read so a multi-MB HTML error page does not
		// get buffered in full.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyMaxBytes))
		return nil, classifyError(resp, body)
	}
	return readTail(resp.Body, jobLogTailMaxBytes)
}

// readTail reads from r and returns the last at-most-maxBytes bytes as a
// rolling tail. For bodies smaller than maxBytes the entire body is returned
// unchanged; for larger bodies only the final maxBytes are retained, so the
// caller (e.g. tailLines) operates on a bounded buffer regardless of input
// size. The read is streaming — the prefix is discarded as it arrives, so peak
// memory is maxBytes + a scratch buffer, not the full body length.
func readTail(r io.Reader, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, nil
	}
	buf := make([]byte, 0, maxBytes)
	chunk := make([]byte, 32*1024)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			// Trim the head down to the last maxBytes so peak memory stays bounded.
			if len(buf) > maxBytes {
				buf = append(buf[:0:0], buf[len(buf)-maxBytes:]...)
			}
		}
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return buf, err
		}
	}
}

// doGETPaginated performs a GET and follows GitLab's Link: <...>; rel="next"
// header to fetch all pages, calling handler for each page's body. It caps at
// maxPaginationPages to prevent runaway pagination on pathological repos
// . The handler is invoked once per page and receives the
// raw JSON body of that page.
const maxPaginationPages = 10

func (c *Client) doGETPaginated(ctx context.Context, path string, q url.Values, handler func(body []byte) error) (bool, error) {
	if q == nil {
		q = url.Values{}
	}
	if q.Get("per_page") == "" {
		q.Set("per_page", "100")
	}
	nextURL := c.restURL(path, q)
	for page := 0; page < maxPaginationPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, http.NoBody)
		if err != nil {
			return false, err
		}
		if err := c.authorize(ctx, req); err != nil {
			return false, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		resp, err := c.http.Do(req)
		if err != nil {
			return false, err
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotModified {
			// 304 on the first page means nothing changed; stop.
			return false, nil
		}
		if resp.StatusCode >= 400 {
			return false, classifyError(resp, body)
		}
		if err := handler(body); err != nil {
			return false, err
		}
		next := parseNextLink(resp.Header.Get("Link"))
		if next == "" {
			return false, nil
		}
		// Validate the next-page URL against the configured REST base before
		// following it. An absolute Link: <https://other-host/...>; rel="next"
		// would otherwise be fetched on the next iteration, re-attaching the
		// Authorization header (Bearer token) to a different host. Require the
		// same scheme, host, port, and API base path as restBase; reject
		// HTTPS-to-HTTP downgrades and hosts not in the allowlist (the per-host
		// trust boundary from ticket 01 — a client's restBase is already scoped to
		// an allowlisted host, so any host mismatch is implicitly non-allowlisted).
		// On rejection the pagination loop returns an error without advancing any
		// ETag or sync cursor (the cross-cutting durable-state rule).
		if err := c.validatePaginationURL(next); err != nil {
			return false, err
		}
		nextURL = next
	}
	// Hit the page cap — response is truncated.
	return true, nil
}

// parseNextLink extracts the next-page URL from a GitLab Link header like:
//
//	<https://gitlab.com/api/v4/...?page=2>; rel="next", <...>; rel="first"
func parseNextLink(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	for _, part := range strings.Split(linkHeader, ",") {
		part = strings.TrimSpace(part)
		// Each part is <url>; rel="next"
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		lt := strings.Index(part, "<")
		gt := strings.Index(part, ">")
		if lt < 0 || gt < 0 || gt <= lt {
			continue
		}
		return part[lt+1 : gt]
	}
	return ""
}

// ErrPaginationURLRejected is returned when a Link: rel="next" pagination URL
// fails validation against the configured REST base. The URL is not followed,
// so the GitLab token is never attached to a different host (review Item 6).
// The error is transient: callers must not advance any ETag or sync cursor on
// this failure (the cross-cutting durable-state rule).
var ErrPaginationURLRejected = fmt.Errorf("gitlab scm: pagination URL rejected")

// validatePaginationURL validates a Link: rel="next" URL against the client's
// configured REST base before the URL is followed. It requires the same scheme,
// host, port, and API base path as restBase, and rejects HTTPS-to-HTTP
// downgrades. The host check implicitly enforces the per-host trust boundary
// from ticket 01: a client's restBase is already scoped to an allowlisted host
// (gitlab.com or an entry in AO_GITLAB_ALLOWED_HOSTS), so any next URL whose
// host differs from restBase's host is, by construction, not in the allowlist
// for this client and is rejected before the Authorization header is attached.
//
// Relative next URLs (no scheme/host) are accepted — GitLab's own pagination
// sometimes returns relative references, and the base is already trusted.
func (c *Client) validatePaginationURL(next string) error {
	nextURL, err := url.Parse(next)
	if err != nil {
		return fmt.Errorf("gitlab scm: parse pagination URL: %w", ErrPaginationURLRejected)
	}
	// Relative next URLs (no host) are safe — they resolve against the trusted
	// restBase on the next request.
	if nextURL.Host == "" {
		return nil
	}
	base, err := url.Parse(c.restBase)
	if err != nil {
		return fmt.Errorf("gitlab scm: parse REST base: %w", ErrPaginationURLRejected)
	}
	// Reject HTTPS-to-HTTP downgrades explicitly (the reviewer's verbatim rule).
	if base.Scheme == "https" && nextURL.Scheme == "http" {
		return fmt.Errorf("gitlab scm: pagination URL downgrades https to http: %w", ErrPaginationURLRejected)
	}
	// Require same scheme, host, and port as the configured REST base.
	if !strings.EqualFold(nextURL.Scheme, base.Scheme) {
		return fmt.Errorf("gitlab scm: pagination URL scheme %q != base %q: %w", nextURL.Scheme, base.Scheme, ErrPaginationURLRejected)
	}
	if !strings.EqualFold(nextURL.Hostname(), base.Hostname()) {
		return fmt.Errorf("gitlab scm: pagination URL host %q != base %q: %w", nextURL.Hostname(), base.Hostname(), ErrPaginationURLRejected)
	}
	if nextURL.Port() != base.Port() {
		return fmt.Errorf("gitlab scm: pagination URL port %q != base %q: %w", nextURL.Port(), base.Port(), ErrPaginationURLRejected)
	}
	// Require the same API base path prefix (e.g. /api/v4). This prevents a
	// same-host URL pointing at a different API version (e.g. /api/v5) from
	// being followed.
	if !strings.HasPrefix(nextURL.Path, base.Path) {
		return fmt.Errorf("gitlab scm: pagination URL path %q does not start with API base %q: %w", nextURL.Path, base.Path, ErrPaginationURLRejected)
	}
	return nil
}

func (c *Client) storeCacheEntry(cacheKey, etag string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.etagOut[cacheKey] = etag
	c.bodyOut[cacheKey] = body
	c.cacheLRU = append(c.cacheLRU, cacheKey)
	for len(c.cacheLRU) > cacheMaxEntries {
		evict := c.cacheLRU[0]
		c.cacheLRU = c.cacheLRU[1:]
		delete(c.etagOut, evict)
		delete(c.bodyOut, evict)
	}
}

func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	if c.tokens == nil {
		return nil
	}
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return err
	}
	// Use Authorization: Bearer instead of PRIVATE-TOKEN because Bearer works
	// for both OAuth2 tokens (from `glab auth login` OAuth flow) and personal
	// access tokens, while PRIVATE-TOKEN only works with personal access tokens.
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

func (c *Client) restURL(path string, q url.Values) string {
	u := c.restBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func classifyError(resp *http.Response, body []byte) error {
	switch resp.StatusCode {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuthFailed
	case http.StatusTooManyRequests:
		return gitlabRateLimited(resp, body)
	default:
		msg := gitlabMessage(body)
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("gitlab scm: %s", msg)
	}
}

// gitlabRateLimited builds a *RateLimitError from a 429 response, parsing the
// Retry-After (seconds) and RateLimit-Reset (Unix epoch seconds) headers so the
// observer can apply a provider-level cooldown instead of polling every 30s
// . Both headers are optional; GitLab sends Retry-After on
// 429s and RateLimit-Reset/RateLimit-Remaining on all rate-limited responses.
func gitlabRateLimited(resp *http.Response, body []byte) error {
	e := &RateLimitError{Message: gitlabMessage(body)}
	if reset := resp.Header.Get("RateLimit-Reset"); reset != "" {
		if sec, err := strconv.ParseInt(reset, 10, 64); err == nil && sec > 0 {
			e.ResetAt = time.Unix(sec, 0)
		}
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if sec, err := strconv.Atoi(ra); err == nil && sec >= 0 {
			e.RetryAfter = time.Duration(sec) * time.Second
		}
	}
	return e
}

func gitlabMessage(body []byte) string {
	var v struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &v) == nil {
		if v.Message != "" {
			return v.Message
		}
		return v.Error
	}
	return ""
}

// projectPath encodes owner/name for GitLab API URL path segments.
func projectPath(owner, name string) string {
	return url.PathEscape(owner + "/" + name)
}
