package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type responseRoundTripper func(*http.Request) (*http.Response, error)

func (f responseRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingResponseBody struct {
	err    error
	closed bool
}

func (b *failingResponseBody) Read(p []byte) (int, error) {
	return copy(p, `{"id":42}`), b.err
}

func (b *failingResponseBody) Close() error {
	b.closed = true
	return nil
}

func testResponse(status int, etag, body string) *http.Response {
	header := make(http.Header)
	if etag != "" {
		header.Set("ETag", etag)
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// Even syntactically complete JSON is unusable when the transport reports
// that its response body was not read successfully.
func TestResponseReadFailure(t *testing.T) {
	for _, operation := range []string{"external_etag", "merge", "get", "pagination"} {
		for _, status := range []int{200, 304, 401, 403, 404, 409, 429, 500} {
			t.Run(fmt.Sprintf("%s/%d", operation, status), func(t *testing.T) {
				readErr := errors.New("response interrupted")
				body := &failingResponseBody{err: readErr}
				c := NewClient(ClientOptions{HTTPClient: &http.Client{Transport: responseRoundTripper(func(req *http.Request) (*http.Response, error) {
					wantMethod := http.MethodGet
					if operation == "merge" {
						wantMethod = http.MethodPut
					}
					if req.Method != wantMethod {
						t.Errorf("request method = %s, want %s", req.Method, wantMethod)
					}
					resp := testResponse(status, `"new"`, "")
					resp.Header.Set("Retry-After", "60")
					resp.Body = body
					return resp, nil
				})}})
				const path = "/projects/1/merge_requests/42"
				c.storeCacheEntry(c.restURL(path, nil), `"old"`, []byte(`{"id":1}`))
				var resp RESTResponse
				var err error
				handlerCalls := 0
				switch operation {
				case "external_etag":
					resp, err = c.doRESTWithETag(context.Background(), path, nil, `"old"`)
				case "merge":
					resp, err = c.doMERGE(context.Background(), path, nil, nil)
				case "get":
					resp, err = c.doGET(context.Background(), path, nil)
				case "pagination":
					_, err = c.doGETPaginated(context.Background(), path, nil, func([]byte) error {
						handlerCalls++
						return nil
					})
				}
				if !errors.Is(err, readErr) {
					t.Errorf("error = %v, want wrapped response read failure", err)
				}
				if operation != "pagination" && resp.StatusCode != status {
					t.Errorf("status = %d, want %d", resp.StatusCode, status)
				}
				if len(resp.Body) != 0 || resp.ETag != "" || resp.NotModified {
					t.Errorf("failed read exposed a usable response: %+v", resp)
				}
				if !body.closed {
					t.Error("response body was not closed")
				}
				if handlerCalls != 0 {
					t.Errorf("pagination handler called %d times after failed read", handlerCalls)
				}
				assertResponseErrorClass(t, status, err)
				c.http.Transport = responseRoundTripper(func(req *http.Request) (*http.Response, error) {
					if got := req.Header.Get("If-None-Match"); got != `"old"` {
						t.Errorf("validator after failed read = %q, want old", got)
					}
					return testResponse(304, `"old"`, ""), nil
				})
				cached, err := c.doGET(context.Background(), path, nil)
				if err != nil || string(cached.Body) != `{"id":1}` {
					t.Errorf("cached response after failed read = %+v, %v", cached, err)
				}
			})
		}
	}
}

func assertResponseErrorClass(t *testing.T, status int, err error) {
	t.Helper()
	var want error
	switch status {
	case 401, 403:
		want = ErrAuthFailed
	case 404:
		want = ErrNotFound
	case 429:
		want = ErrRateLimited
		var rateLimit *RateLimitError
		if !errors.As(err, &rateLimit) || rateLimit.RetryAfter != time.Minute {
			t.Errorf("rate limit hint lost: %v", err)
		}
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("error = %v, want status classification %v", err, want)
	}
}

func TestDoGETNotModifiedUsesRequestSnapshot(t *testing.T) {
	for _, interleave := range []string{"replace", "evict"} {
		t.Run(interleave, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started := make(chan struct{})
			release := make(chan struct{})
			const path = "/projects/1/merge_requests/42"
			c := NewClient(ClientOptions{HTTPClient: &http.Client{Transport: responseRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.Context().Value(snapshotRequestKey{}) != nil {
					if got := req.Header.Get("If-None-Match"); got != `"old"` {
						return nil, fmt.Errorf("conditional validator = %q, want old", got)
					}
					close(started)
					select {
					case <-release:
						return testResponse(304, "", ""), nil
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				}
				return testResponse(200, `"new"`, `{"id":2}`), nil
			})}})
			c.storeCacheEntry(c.restURL(path, nil), `"old"`, []byte(`{"id":1}`))
			type result struct {
				response RESTResponse
				err      error
			}
			done := make(chan result, 1)
			go func() {
				resp, err := c.doGET(context.WithValue(ctx, snapshotRequestKey{}, true), path, nil)
				done <- result{resp, err}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("conditional request did not start")
			}
			if interleave == "replace" {
				if _, err := c.doGET(ctx, path, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				for i := 0; i < cacheMaxEntries; i++ {
					if _, err := c.doGET(ctx, fmt.Sprintf("/projects/%d", i), nil); err != nil {
						t.Fatal(err)
					}
				}
			}
			close(release)
			select {
			case got := <-done:
				if got.err != nil {
					t.Fatal(got.err)
				}
				if got.response.StatusCode != http.StatusOK || !got.response.NotModified || got.response.ETag != `"old"` || string(got.response.Body) != `{"id":1}` {
					t.Errorf("304 response = %+v, want original conditional body and validator", got.response)
				}
			case <-ctx.Done():
				t.Fatal("conditional request did not finish")
			}
			c.http.Transport = responseRoundTripper(func(req *http.Request) (*http.Response, error) {
				want := `"new"`
				if interleave == "evict" {
					want = ""
				}
				if got := req.Header.Get("If-None-Match"); got != want {
					t.Errorf("validator after interleaved 304 = %q, want %q", got, want)
				}
				return testResponse(200, `"new"`, `{"id":2}`), nil
			})
			if _, err := c.doGET(ctx, path, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type snapshotRequestKey struct{}

func TestDoGETNotModifiedWithoutCachedBody(t *testing.T) {
	c := NewClient(ClientOptions{HTTPClient: &http.Client{Transport: responseRoundTripper(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("If-None-Match"); got != "" {
			t.Errorf("uncached request sent validator %q", got)
		}
		return testResponse(304, `"unknown"`, ""), nil
	})}})
	resp, err := c.doGET(context.Background(), "/projects/1", nil)
	if err == nil || resp.NotModified || len(resp.Body) != 0 {
		t.Errorf("uncached 304 = %+v, %v; want error", resp, err)
	}
}

func TestDoGETCacheBodyOwnership(t *testing.T) {
	calls := 0
	c := NewClient(ClientOptions{HTTPClient: &http.Client{Transport: responseRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return testResponse(200, `"old"`, `{"id":1}`), nil
		}
		return testResponse(304, `"old"`, ""), nil
	})}})
	for i := 0; i < 3; i++ {
		resp, err := c.doGET(context.Background(), "/projects/1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(resp.Body) != `{"id":1}` {
			t.Fatalf("response %d body = %q, cache was changed by caller", i, resp.Body)
		}
		resp.Body[0] = '!'
	}
}

func TestDoGETPaginatedReadFailureStopsNextPage(t *testing.T) {
	readErr := errors.New("second page interrupted")
	calls, handled := 0, 0
	c := NewClient(ClientOptions{HTTPClient: &http.Client{Transport: responseRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		resp := testResponse(200, "", `[{"id":1}]`)
		resp.Header.Set("Link", `<https://gitlab.com/api/v4/projects/1?page=2>; rel="next"`)
		if calls == 2 {
			resp.Body = &failingResponseBody{err: readErr}
		}
		return resp, nil
	})}})
	truncated, err := c.doGETPaginated(context.Background(), "/projects/1", url.Values{}, func([]byte) error {
		handled++
		return nil
	})
	if !errors.Is(err, readErr) || truncated || calls != 2 || handled != 1 {
		t.Errorf("pagination = truncated %v, error %v, requests %d, handled %d; want read error after first page", truncated, err, calls, handled)
	}
}
