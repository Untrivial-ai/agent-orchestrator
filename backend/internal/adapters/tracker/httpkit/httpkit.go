// Package httpkit provides shared HTTP plumbing for tracker adapters:
// Link-header pagination parsing, rate-limit error construction, error-body
// message extraction, an issue-append-with-limit helper, and a preflight
// cache using double-checked locking. Both the GitHub and GitLab tracker
// adapters import this package to avoid duplicating these patterns.
package httpkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// RateLimitError carries structured backoff hints from a rate-limit
// response. Each adapter constructs it with its own sentinel error so
// errors.Is(err, <adapter>.ErrRateLimited) continues to work.
type RateLimitError struct {
	ResetAt    time.Time
	RetryAfter time.Duration
	Message    string
	// Sentinel is the adapter-specific error that errors.Is matches
	// against (e.g. github.ErrRateLimited or gitlab.ErrRateLimited).
	Sentinel error
}

// Error formats the rate-limit error for logs.
func (e *RateLimitError) Error() string {
	if e == nil || e.Sentinel == nil {
		return "rate limited"
	}
	if e.Message != "" {
		return e.Sentinel.Error() + ": " + e.Message
	}
	return e.Sentinel.Error()
}

// Is lets errors.Is match a *RateLimitError against the adapter's
// sentinel error.
func (e *RateLimitError) Is(target error) bool {
	return e.Sentinel != nil && target == e.Sentinel
}

// BuildRateLimitError constructs a *RateLimitError from HTTP response
// headers. It checks both GitHub's X-RateLimit-Reset and GitLab's
// RateLimit-Reset headers, plus Retry-After. Each provider only sends
// one set of headers, so checking both is harmless.
func BuildRateLimitError(resp *http.Response, msg string, sentinel error) *RateLimitError {
	e := &RateLimitError{Message: msg, Sentinel: sentinel}
	// GitHub uses X-RateLimit-Reset; GitLab uses RateLimit-Reset.
	for _, h := range []string{"X-RateLimit-Reset", "RateLimit-Reset"} {
		if reset := resp.Header.Get(h); reset != "" {
			if sec, err := strconv.ParseInt(reset, 10, 64); err == nil && sec > 0 {
				e.ResetAt = time.Unix(sec, 0)
				break
			}
		}
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if sec, err := strconv.Atoi(ra); err == nil && sec >= 0 {
			e.RetryAfter = time.Duration(sec) * time.Second
		}
	}
	return e
}

// ParseLinkNext extracts the next-page relative path from a Link header.
// It handles both quoted and unquoted rel="next" values, multiple rel
// values, and absolute/relative URLs. The returned path is relative to
// baseURL (the base URL's path prefix, e.g. /api/v4, is stripped).
func ParseLinkNext(linkHeader, baseURL string) string {
	for _, part := range strings.Split(linkHeader, ",") {
		part = strings.TrimSpace(part)
		if part == "" || !strings.HasPrefix(part, "<") {
			continue
		}
		urlEnd := strings.Index(part, ">")
		if urlEnd < 0 {
			continue
		}
		rawURL := part[1:urlEnd]
		if !linkHasRelNext(part[urlEnd+1:]) {
			continue
		}
		return relativePathForLink(rawURL, baseURL)
	}
	return ""
}

func linkHasRelNext(params string) bool {
	for _, param := range strings.Split(params, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
		if !ok || !strings.EqualFold(name, "rel") {
			continue
		}
		value = strings.Trim(value, `"`)
		for _, rel := range strings.Fields(value) {
			if strings.EqualFold(rel, "next") {
				return true
			}
		}
	}
	return false
}

func relativePathForLink(rawLink, baseURL string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	u, err := url.Parse(rawLink)
	if err != nil {
		return ""
	}
	if !u.IsAbs() {
		u = base.ResolveReference(u)
	}
	if u.Path == "" {
		return ""
	}
	path := u.EscapedPath()
	// Strip the base URL's path prefix (e.g. /api/v4 for GitLab) so the
	// returned path is relative to the base URL and can be appended
	// directly. For GitHub (base path is ""), this is a no-op.
	if base.Path != "" && strings.HasPrefix(path, base.Path) {
		path = strings.TrimPrefix(path, base.Path)
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}

// AppendIssuesWithLimit appends src to dst, respecting an optional total
// limit. Returns the updated slice and a done flag indicating the limit
// was reached.
func AppendIssuesWithLimit(dst, src []domain.Issue, limit int) ([]domain.Issue, bool) {
	if limit <= 0 {
		return append(dst, src...), false
	}
	remaining := limit - len(dst)
	if remaining <= 0 {
		return dst, true
	}
	if len(src) > remaining {
		return append(dst, src[:remaining]...), true
	}
	dst = append(dst, src...)
	return dst, len(dst) >= limit
}

// Message extracts a human-readable message from an error response body.
// It checks both the "message" field (used by GitHub and GitLab) and the
// "error" field (used by GitLab). If neither is present, it returns the
// trimmed body text.
func Message(body []byte) string {
	var p struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &p) == nil {
		if p.Message != "" {
			return p.Message
		}
		return p.Error
	}
	return strings.TrimSpace(string(body))
}

// PreflightCache caches a successful preflight check using double-checked
// locking (atomic.Bool + sync.Mutex). The hot path is one atomic.Load
// with no contention; concurrent first-callers serialize on the mutex so
// only one probe is in flight. Failures are NOT cached so a transient
// startup glitch is recoverable on a subsequent call.
type PreflightCache struct {
	ok atomic.Bool
	mu sync.Mutex
}

// Run executes probe if no prior probe has succeeded. If probe returns
// nil, success is cached and all subsequent calls return nil immediately.
// If probe returns an error, it is returned uncached.
func (c *PreflightCache) Run(ctx context.Context, probe func(context.Context) error) error {
	if c.ok.Load() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Re-check after acquiring the lock — another goroutine may have raced
	// us through the probe and stored success while we were waiting.
	if c.ok.Load() {
		return nil
	}
	if err := probe(ctx); err != nil {
		return err
	}
	c.ok.Store(true)
	return nil
}
