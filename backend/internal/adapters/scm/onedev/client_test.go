package onedev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, apiBase string, tokens TokenSource) *Client {
	t.Helper()
	c, err := NewClient(ClientOptions{APIBase: apiBase, Token: tokens})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClientRequiresAPIBase(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		wantErr error
		want    string
	}{
		{name: "empty", base: "", wantErr: ErrNoAPIBase},
		{name: "whitespace", base: "   ", wantErr: ErrNoAPIBase},
		{name: "trailing slash trimmed", base: "http://od.test:6610/~api/", want: "http://od.test:6610/~api"},
		{name: "kept as given", base: "http://od.test:6610/~api", want: "http://od.test:6610/~api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(ClientOptions{APIBase: tt.base})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewClient err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if c.APIBase() != tt.want {
				t.Fatalf("APIBase() = %q, want %q", c.APIBase(), tt.want)
			}
		})
	}
}

// TestPreflightSendsAuthenticatedProjectsRequest pins the preflight contract:
// GET against the /~api root (not /api), with the credential attached.
func TestPreflightSendsAuthenticatedProjectsRequest(t *testing.T) {
	tests := []struct {
		name     string
		tokens   TokenSource
		wantAuth func(t *testing.T, r *http.Request)
	}{
		{
			name:   "bearer token",
			tokens: StaticTokenSource("od-token"),
			wantAuth: func(t *testing.T, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer od-token" {
					t.Errorf("Authorization = %q, want Bearer od-token", got)
				}
			},
		},
		{
			name:   "basic auth",
			tokens: StaticBasicAuthSource{Username: "svc", Password: "pw"},
			wantAuth: func(t *testing.T, r *http.Request) {
				user, pass, ok := r.BasicAuth()
				if !ok || user != "svc" || pass != "pw" {
					t.Errorf("BasicAuth = (%q, %q, %v), want (svc, pw, true)", user, pass, ok)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotQuery string
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
				tt.wantAuth(t, r)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"id":1,"path":"Homelab"}]`))
			})
			c := newTestClient(t, srv.URL+APIBasePath, tt.tokens)
			if err := c.Preflight(context.Background()); err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			if gotPath != "/~api/projects" {
				t.Errorf("path = %q, want /~api/projects", gotPath)
			}
			q, _ := url.ParseQuery(gotQuery)
			if q.Get("count") != "1" || q.Get("offset") != "0" {
				t.Errorf("query = %q, want offset=0&count=1", gotQuery)
			}
		})
	}
}

func TestPreflightErrorClassification(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		tokens     TokenSource
		wantErr    error
		wantSubstr string
	}{
		{
			// Verified against a live instance: OneDev answers a bad token with
			// 401 and a plain-text body.
			name:   "bad credential is ErrAuthFailed",
			status: http.StatusUnauthorized, body: "Invalid account or incorrect credentials",
			tokens: StaticTokenSource("od-bad"), wantErr: ErrAuthFailed,
			wantSubstr: "Invalid account or incorrect credentials",
		},
		{
			name:   "forbidden is ErrAuthFailed",
			status: http.StatusForbidden, body: "", tokens: StaticTokenSource("od-token"),
			wantErr: ErrAuthFailed,
		},
		{
			name:   "not found",
			status: http.StatusNotFound, body: "", tokens: StaticTokenSource("od-token"),
			wantErr: ErrNotFound,
		},
		{
			name:   "rate limited",
			status: http.StatusTooManyRequests, body: "slow down", tokens: StaticTokenSource("od-token"),
			wantErr: ErrRateLimited,
		},
		{
			name:   "other errors carry the server message",
			status: http.StatusNotAcceptable, body: "Count should not be greater than 100",
			tokens: StaticTokenSource("od-token"), wantSubstr: "Count should not be greater than 100",
		},
		{
			name:   "no credential configured",
			status: http.StatusOK, body: "[]", tokens: nil, wantErr: ErrNoToken,
		},
		{
			name:   "token source yields nothing",
			status: http.StatusOK, body: "[]", tokens: StaticTokenSource(""), wantErr: ErrNoToken,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			c := newTestClient(t, srv.URL+APIBasePath, tt.tokens)
			err := c.Preflight(context.Background())
			if err == nil {
				t.Fatal("Preflight succeeded, want error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantSubstr)
			}
			// Every preflight failure names the instance it failed against.
			if !strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("err = %v, want it to name the API base %q", err, srv.URL)
			}
		})
	}
}

// TestAuthFailureInvalidatesCachedCredential covers the rotated-token case: a
// 401 must drop the cached credential so the next call re-reads it, rather
// than failing until the daemon restarts.
func TestAuthFailureInvalidatesCachedCredential(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer good" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Invalid account or incorrect credentials"))
	})

	secrets := []string{"stale", "good"}
	calls := 0
	src := &CommandTokenSource{
		Command:  []string{"helper"},
		TokenTTL: time.Hour,
		Run: func(context.Context, []string) (string, error) {
			s := secrets[calls]
			calls++
			return s, nil
		},
	}
	c := newTestClient(t, srv.URL+APIBasePath, src)

	if err := c.Preflight(context.Background()); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("first Preflight err = %v, want ErrAuthFailed", err)
	}
	if err := c.Preflight(context.Background()); err != nil {
		t.Fatalf("second Preflight: %v", err)
	}
	if calls != 2 {
		t.Fatalf("helper ran %d times, want 2 (credential re-read after 401)", calls)
	}
}

// TestNoConditionalRequestHeaders pins the verified fact that OneDev sends no
// ETag or Last-Modified: the client must not send validators the server will
// ignore, and must not grow a 304 path that can never fire.
func TestNoConditionalRequestHeaders(t *testing.T) {
	var got http.Header
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte("[]"))
	})
	c := newTestClient(t, srv.URL+APIBasePath, StaticTokenSource("od-token"))
	if err := c.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	for _, h := range []string{"If-None-Match", "If-Modified-Since"} {
		if v := got.Get(h); v != "" {
			t.Errorf("request carried %s = %q; OneDev supports no conditional requests", h, v)
		}
	}
	if got.Get("User-Agent") != defaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", got.Get("User-Agent"), defaultUserAgent)
	}
}

// projectPage renders a page of n synthetic project records.
func projectPage(offset, n int) []byte {
	items := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, map[string]any{"id": offset + i, "path": fmt.Sprintf("p%d", offset+i)})
	}
	b, _ := json.Marshal(items)
	return b
}

func TestDoGETPaginated(t *testing.T) {
	tests := []struct {
		name          string
		total         int
		query         url.Values
		wantItems     int
		wantPages     int
		wantCount     string
		wantTruncated bool
	}{
		{
			name: "single short page ends the walk", total: 7,
			wantItems: 7, wantPages: 1, wantCount: "100",
		},
		{
			name: "exact page boundary needs one more request", total: 100,
			wantItems: 100, wantPages: 2, wantCount: "100",
		},
		{
			name: "multiple pages", total: 250,
			wantItems: 250, wantPages: 3, wantCount: "100",
		},
		{
			name: "empty result", total: 0,
			wantItems: 0, wantPages: 1, wantCount: "100",
		},
		{
			// OneDev rejects count > 100 with HTTP 406, so the client clamps
			// rather than letting the server refuse the request.
			name: "oversized count is clamped", total: 5, query: url.Values{"count": {"5000"}},
			wantItems: 5, wantPages: 1, wantCount: "100",
		},
		{
			name: "smaller count is respected", total: 5, query: url.Values{"count": {"2"}},
			wantItems: 5, wantPages: 3, wantCount: "2",
		},
		{
			name: "page cap marks the result truncated", total: 100 * (maxPaginationPages + 2),
			wantItems: 100 * maxPaginationPages, wantPages: maxPaginationPages,
			wantCount: "100", wantTruncated: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pages, counts := 0, map[string]bool{}
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				pages++
				q := r.URL.Query()
				counts[q.Get("count")] = true
				offset, _ := strconv.Atoi(q.Get("offset"))
				count, _ := strconv.Atoi(q.Get("count"))
				n := tt.total - offset
				if n > count {
					n = count
				}
				if n < 0 {
					n = 0
				}
				_, _ = w.Write(projectPage(offset, n))
			})
			c := newTestClient(t, srv.URL+APIBasePath, StaticTokenSource("od-token"))

			items := 0
			truncated, err := c.doGETPaginated(context.Background(), "/projects", tt.query, func(body []byte) (int, error) {
				var page []map[string]any
				if err := json.Unmarshal(body, &page); err != nil {
					return 0, err
				}
				items += len(page)
				return len(page), nil
			})
			if err != nil {
				t.Fatalf("doGETPaginated: %v", err)
			}
			if truncated != tt.wantTruncated {
				t.Errorf("truncated = %v, want %v", truncated, tt.wantTruncated)
			}
			if items != tt.wantItems {
				t.Errorf("items = %d, want %d", items, tt.wantItems)
			}
			if pages != tt.wantPages {
				t.Errorf("pages = %d, want %d", pages, tt.wantPages)
			}
			if len(counts) != 1 || !counts[tt.wantCount] {
				t.Errorf("count params = %v, want only %q", counts, tt.wantCount)
			}
		})
	}
}

func TestDoGETPaginatedPropagatesErrors(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		c := newTestClient(t, srv.URL+APIBasePath, StaticTokenSource("od-token"))
		_, err := c.doGETPaginated(context.Background(), "/projects", nil, func([]byte) (int, error) {
			t.Fatal("handler must not run on an error response")
			return 0, nil
		})
		if !errors.Is(err, ErrAuthFailed) {
			t.Fatalf("err = %v, want ErrAuthFailed", err)
		}
	})

	t.Run("handler error", func(t *testing.T) {
		boom := errors.New("decode failed")
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(projectPage(0, 100))
		})
		c := newTestClient(t, srv.URL+APIBasePath, StaticTokenSource("od-token"))
		_, err := c.doGETPaginated(context.Background(), "/projects", nil, func([]byte) (int, error) {
			return 0, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	})
}

func TestRateLimitErrorHints(t *testing.T) {
	reset := time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		retryAfter     string
		wantRetryAfter time.Duration
		wantResetAt    time.Time
	}{
		{name: "no header", retryAfter: ""},
		{name: "seconds", retryAfter: "30", wantRetryAfter: 30 * time.Second},
		{name: "zero seconds", retryAfter: "0", wantRetryAfter: 0},
		{name: "http date", retryAfter: reset.Format(http.TimeFormat), wantResetAt: reset},
		{name: "junk is ignored", retryAfter: "soon"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte("too many requests"))
			})
			c := newTestClient(t, srv.URL+APIBasePath, StaticTokenSource("od-token"))
			err := c.Preflight(context.Background())
			if !errors.Is(err, ErrRateLimited) {
				t.Fatalf("err = %v, want ErrRateLimited", err)
			}
			var rl *RateLimitError
			if !errors.As(err, &rl) {
				t.Fatalf("err = %v, want a *RateLimitError", err)
			}
			if rl.GetRetryAfter() != tt.wantRetryAfter {
				t.Errorf("RetryAfter = %v, want %v", rl.GetRetryAfter(), tt.wantRetryAfter)
			}
			if !rl.GetResetAt().Equal(tt.wantResetAt) {
				t.Errorf("ResetAt = %v, want %v", rl.GetResetAt(), tt.wantResetAt)
			}
		})
	}
}

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "empty", body: "", want: ""},
		{name: "whitespace", body: "  \n ", want: ""},
		{name: "plain text", body: "Invalid account or incorrect credentials", want: "Invalid account or incorrect credentials"},
		{name: "plain text is collapsed", body: "Count should not\n  be greater than 100", want: "Count should not be greater than 100"},
		{name: "json errorMessage", body: `{"errorMessage":"no such project"}`, want: "no such project"},
		{name: "json message", body: `{"message":"nope"}`, want: "nope"},
		{name: "json error", body: `{"error":"nope"}`, want: "nope"},
		{name: "json without a known field falls back to raw", body: `{"x":1}`, want: `{"x":1}`},
		{name: "long body is truncated", body: strings.Repeat("a", messageMaxRunes+50), want: strings.Repeat("a", messageMaxRunes) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorMessage([]byte(tt.body)); got != tt.want {
				t.Fatalf("errorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}
