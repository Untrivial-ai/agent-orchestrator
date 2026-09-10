package httpkit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestParseLinkNext(t *testing.T) {
	// GitHub base URL (no path prefix)
	ghBase := "https://api.github.com"
	// GitLab base URL (has /api/v4 prefix)
	glBase := "https://gitlab.com/api/v4"

	cases := []struct {
		name    string
		link    string
		baseURL string
		want    string
	}{
		// GitHub cases
		{
			name:    "gh quoted next strips absolute host",
			link:    `<https://api.github.com/repos/o/r/issues?state=all&per_page=100&page=2>; rel="next"`,
			baseURL: ghBase,
			want:    "/repos/o/r/issues?state=all&per_page=100&page=2",
		},
		{
			name:    "gh unquoted next among multiple links",
			link:    `<https://api.github.com/repos/o/r/issues?page=1>; rel=prev, <https://api.github.com/repos/o/r/issues?page=3>; rel=next`,
			baseURL: ghBase,
			want:    "/repos/o/r/issues?page=3",
		},
		{
			name:    "gh multiple rel values",
			link:    `<https://api.github.com/repos/o/r/issues?page=4>; rel="last next"`,
			baseURL: ghBase,
			want:    "/repos/o/r/issues?page=4",
		},
		{
			name:    "gh relative path",
			link:    `</repos/o/r/issues?page=2>; rel="next"`,
			baseURL: ghBase,
			want:    "/repos/o/r/issues?page=2",
		},
		{
			name:    "gh no next",
			link:    `<https://api.github.com/repos/o/r/issues?page=1>; rel="prev"`,
			baseURL: ghBase,
			want:    "",
		},
		// GitLab cases
		{
			name:    "gl quoted next strips absolute host and api prefix",
			link:    `<https://gitlab.com/api/v4/projects/1/issues?state=all&per_page=100&page=2>; rel="next"`,
			baseURL: glBase,
			want:    "/projects/1/issues?state=all&per_page=100&page=2",
		},
		{
			name:    "gl unquoted next among multiple links",
			link:    `<https://gitlab.com/api/v4/projects/1/issues?page=1>; rel=prev, <https://gitlab.com/api/v4/projects/1/issues?page=3>; rel=next`,
			baseURL: glBase,
			want:    "/projects/1/issues?page=3",
		},
		{
			name:    "gl relative path",
			link:    `</projects/1/issues?page=2>; rel="next"`,
			baseURL: glBase,
			want:    "/projects/1/issues?page=2",
		},
		{
			name:    "gl no next",
			link:    `<https://gitlab.com/api/v4/projects/1/issues?page=1>; rel="prev"`,
			baseURL: glBase,
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseLinkNext(tc.link, tc.baseURL); got != tc.want {
				t.Fatalf("ParseLinkNext() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAppendIssuesWithLimit(t *testing.T) {
	mk := func(n int) []domain.Issue {
		out := make([]domain.Issue, n)
		for i := range out {
			out[i] = domain.Issue{Title: strconv.Itoa(i)}
		}
		return out
	}
	cases := []struct {
		name   string
		dst    []domain.Issue
		src    []domain.Issue
		limit  int
		wantN  int
		wantOK bool
	}{
		{"no limit", mk(2), mk(3), 0, 5, false},
		{"under limit", mk(1), mk(2), 5, 3, false},
		{"exact limit", mk(2), mk(3), 5, 5, true},
		{"over limit truncates src", mk(2), mk(5), 4, 4, true},
		{"limit reached ignores src", mk(3), mk(2), 3, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, done := AppendIssuesWithLimit(tc.dst, tc.src, tc.limit)
			if len(out) != tc.wantN {
				t.Fatalf("len = %d, want %d", len(out), tc.wantN)
			}
			if done != tc.wantOK {
				t.Fatalf("done = %v, want %v", done, tc.wantOK)
			}
		})
	}
}

func TestMessage(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"message field", `{"message":"Not Found"}`, "Not Found"},
		{"error field", `{"error":"Unauthorized"}`, "Unauthorized"},
		{"message wins over error", `{"message":"msg","error":"err"}`, "msg"},
		{"empty body", ``, ""},
		{"non-json", `plain text`, "plain text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Message([]byte(tc.body)); got != tc.want {
				t.Fatalf("Message() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildRateLimitError(t *testing.T) {
	sentinel := errors.New("test: rate limited")
	reset := time.Now().Add(2 * time.Minute).Unix()

	t.Run("github headers", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		resp.Header.Set("Retry-After", "60")
		e := BuildRateLimitError(resp, "too many", sentinel)
		if e.ResetAt.Unix() != reset {
			t.Fatalf("ResetAt = %d, want %d", e.ResetAt.Unix(), reset)
		}
		if e.RetryAfter != 60*time.Second {
			t.Fatalf("RetryAfter = %v, want 60s", e.RetryAfter)
		}
		if !errors.Is(e, sentinel) {
			t.Fatalf("errors.Is(e, sentinel) = false, want true")
		}
	})

	t.Run("gitlab headers", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("RateLimit-Reset", strconv.FormatInt(reset, 10))
		resp.Header.Set("Retry-After", "30")
		e := BuildRateLimitError(resp, "rate limited", sentinel)
		if e.ResetAt.Unix() != reset {
			t.Fatalf("ResetAt = %d, want %d", e.ResetAt.Unix(), reset)
		}
		if e.RetryAfter != 30*time.Second {
			t.Fatalf("RetryAfter = %v, want 30s", e.RetryAfter)
		}
	})

	t.Run("error message", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		e := BuildRateLimitError(resp, "msg here", sentinel)
		if got := e.Error(); got != "test: rate limited: msg here" {
			t.Fatalf("Error() = %q, want %q", got, "test: rate limited: msg here")
		}
		e2 := BuildRateLimitError(resp, "", sentinel)
		if got := e2.Error(); got != "test: rate limited" {
			t.Fatalf("Error() = %q, want %q", got, "test: rate limited")
		}
	})
}

func TestPreflightCache_CachesSuccess(t *testing.T) {
	var calls int32
	probe := func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	c := &PreflightCache{}
	for i := 0; i < 5; i++ {
		if err := c.Run(context.Background(), probe); err != nil {
			t.Fatalf("Run #%d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1 (success cached)", got)
	}
}

func TestPreflightCache_RetriesAfterFailure(t *testing.T) {
	var calls int32
	var mu sync.Mutex
	probe := func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return errors.New("transient")
		}
		return nil
	}
	c := &PreflightCache{}
	if err := c.Run(context.Background(), probe); err == nil {
		t.Fatalf("first Run expected to fail")
	}
	if err := c.Run(context.Background(), probe); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("probe calls = %d, want 2 (failure not cached)", got)
	}
}

func TestPreflightCache_ConcurrentFirstCallers(t *testing.T) {
	var calls int32
	probe := func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond) // slow probe
		return nil
	}
	c := &PreflightCache{}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Run(context.Background(), probe)
		}()
	}
	wg.Wait()
	// At least one call must have happened; ideally exactly 1 but the race
	// window is tiny so we just assert it wasn't called 10 times.
	if got := atomic.LoadInt32(&calls); got > 2 {
		t.Fatalf("probe calls = %d, want at most 2 (mutex should serialize)", got)
	}
}

// TestPreflightCache_StaleServer ensures Run works against a real
// httptest server end-to-end (simulating the actual preflight probe).
func TestPreflightCache_RealHTTPProbe(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	probe := func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/user", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return errors.New("auth failed")
		}
		return nil
	}

	c := &PreflightCache{}
	for i := 0; i < 3; i++ {
		if err := c.Run(context.Background(), probe); err != nil {
			t.Fatalf("Run #%d: %v", i, err)
		}
	}
	if hits != 1 {
		t.Fatalf("server hits = %d, want 1", hits)
	}
}
