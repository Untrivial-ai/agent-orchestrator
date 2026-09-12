package gitcode

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

const (
	defaultRESTBase  = "https://gitcode.com/api/v5"
	defaultUserAgent = "ao-gitcode-scm"
	defaultPageSize  = 100
	maxPages         = 1000
	requestTimeout   = 30 * time.Second
	maxResponseBytes = 32 << 20
)

// HTTPClient is the HTTP client type the adapter accepts; declared as a
// distinct pointer type so provider options read clearly and tests can
// substitute a stub transport.
type HTTPClient = http.Client

// ErrNotFound is returned when a GitCode API resource does not exist. It
// aliases the provider-neutral sentinel so the observer treats a missing PR
// as a terminal miss rather than a transient failure.
var ErrNotFound = ports.ErrSCMNotFound

// ErrRateLimited is the sentinel matched by errors.Is for HTTP 429 responses.
// Callers needing the exact backoff use errors.As with *RateLimitError.
var ErrRateLimited = fmt.Errorf("gitcode scm: rate limited")

// RateLimitError carries structured retry hints parsed from a 429 response.
// It satisfies the observer's rateLimitedError optional capability.
type RateLimitError struct {
	Message    string
	RetryAfter time.Duration
	ResetAt    time.Time
}

func (e *RateLimitError) Error() string {
	if e.Message != "" {
		return "gitcode scm: rate limited: " + e.Message
	}
	return ErrRateLimited.Error()
}

// Is lets errors.Is match a *RateLimitError against ErrRateLimited.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// GetRetryAfter reports the parsed Retry-After hint, zero when absent.
func (e *RateLimitError) GetRetryAfter() time.Duration { return e.RetryAfter }

// GetResetAt reports the parsed reset epoch, zero when absent.
func (e *RateLimitError) GetResetAt() time.Time { return e.ResetAt }

// ClientOptions configures a Client.
type ClientOptions struct {
	HTTPClient *http.Client
	Token      TokenSource
	RESTBase   string
	UserAgent  string
}

// RESTResponse is a successful API response body plus its response headers.
// Pagination on GitCode is signaled through the total_count / total_page
// response headers, so callers that paginate need the header set.
type RESTResponse struct {
	Body    []byte
	Headers http.Header
}

// Client is a minimal GitCode v5 REST client. It adds the Bearer token to
// every request, classifies API errors into the provider error set, and
// exposes page-aware GET helpers.
type Client struct {
	httpClient *http.Client
	tokens     TokenSource
	restBase   string
	userAgent  string
}

// NewClient builds a Client with defaults for omitted options.
func NewClient(opts ClientOptions) *Client {
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	base := strings.TrimSpace(opts.RESTBase)
	if base == "" {
		base = defaultRESTBase
	}
	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = defaultUserAgent
	}
	return &Client{httpClient: hc, tokens: opts.Token, restBase: strings.TrimRight(base, "/"), userAgent: ua}
}

// doGET performs an authenticated GET against path with query q.
func (c *Client) doGET(ctx context.Context, path string, q url.Values) (RESTResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.restURL(path, q), nil)
	if err != nil {
		return RESTResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if err := c.authorize(ctx, req); err != nil {
		return RESTResponse{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return RESTResponse{}, err
	}
	defer resp.Body.Close()
	body, err := readResponseBody(resp)
	if err != nil {
		return RESTResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RESTResponse{}, classifyError(resp, body)
	}
	return RESTResponse{Body: body, Headers: resp.Header}, nil
}

// doGETPaged walks list endpoints page by page. GitCode signals the page
// count through the total_page response header; pagination stops on an empty
// page, when pageFn returns stop, when total_page is reached, or at a hard
// page cap. Each page is decoded as a JSON array and handed to pageFn as raw
// elements so callers unmarshal into their own payload types.
func (c *Client) doGETPaged(ctx context.Context, path string, q url.Values, perPage int, pageFn func(elems []json.RawMessage) (stop bool, err error)) error {
	for page := 1; page <= maxPages; page++ {
		qq := url.Values{}
		for k, vs := range q {
			qq[k] = vs
		}
		qq.Set("page", strconv.Itoa(page))
		if perPage > 0 {
			qq.Set("per_page", strconv.Itoa(perPage))
		}
		resp, err := c.doGET(ctx, path, qq)
		if err != nil {
			return err
		}
		elems, err := decodeArrayBody(resp.Body)
		if err != nil {
			return err
		}
		stop, err := pageFn(elems)
		if err != nil {
			return err
		}
		if stop || len(elems) == 0 {
			return nil
		}
		if totalPage := headerInt(resp.Headers, "total_page"); totalPage > 0 && page >= totalPage {
			return nil
		}
	}
	return nil
}

func decodeArrayBody(body []byte) ([]json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(body, &elems); err != nil {
		return nil, fmt.Errorf("gitcode scm: unmarshal list response: %w", err)
	}
	return elems, nil
}

func headerInt(h http.Header, name string) int {
	if h == nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(h.Get(name)))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	// Anonymous mode: GitCode v5 serves public repository data without a
	// token, so a missing token source (or one that yields no token) falls
	// back to an unauthenticated request rather than failing the call.
	if c.tokens == nil {
		return nil
	}
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		if errors.Is(err, ErrNoToken) {
			return nil
		}
		return err
	}
	if tok == "" {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

func (c *Client) restURL(path string, q url.Values) string {
	u := c.restBase + "/" + strings.TrimLeft(path, "/")
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func readResponseBody(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("gitcode scm: read response: %w", err)
	}
	return body, nil
}

// classifyError maps an HTTP error status onto the provider error set: 404 to
// ErrNotFound, 401/403 to ErrAuthFailed, 429 to a *RateLimitError, and
// anything else to a plain error carrying GitCode's error_message.
func classifyError(resp *http.Response, body []byte) error {
	switch resp.StatusCode {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuthFailed
	case http.StatusTooManyRequests:
		return rateLimited(resp, body)
	default:
		msg := gitcodeMessage(body)
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("gitcode scm: %s", msg)
	}
}

// rateLimited builds a *RateLimitError from Retry-After / RateLimit-Reset
// response headers when present.
func rateLimited(resp *http.Response, body []byte) error {
	e := &RateLimitError{Message: gitcodeMessage(body)}
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

func gitcodeMessage(body []byte) string {
	var v struct {
		Message      string `json:"message"`
		Error        string `json:"error"`
		ErrorMessage string `json:"error_message"`
	}
	if json.Unmarshal(body, &v) == nil {
		for _, s := range []string{v.ErrorMessage, v.Message, v.Error} {
			if s != "" {
				return s
			}
		}
	}
	return ""
}
