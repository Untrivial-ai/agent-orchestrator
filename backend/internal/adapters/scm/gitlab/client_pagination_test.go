package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// TestPaginationNextURLDifferentHostRejected verifies that a Link: rel="next"
// URL pointing at a different host is rejected before it is followed — the
// GitLab token is never attached to the different-host URL (review Item 6).
// This is also the cross-item integration test for ticket 01: a host not in
// the allowlist (the different host in the Link header) must be rejected
// before the URL is followed.
func TestPaginationNextURLDifferentHostRejected(t *testing.T) {
	var attackerHits int32
	attackerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attackerHits, 1)
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(attackerSrv.Close)

	var mainHits int32
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mainHits, 1)
		// Serve one page with a Link header pointing at the attacker server.
		attackerURL := attackerSrv.URL + "/api/v4/projects/1/merge_requests?page=2"
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, attackerURL))
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(mainSrv.Close)

	c := NewClient(ClientOptions{
		Token:    StaticTokenSource("secret-token"),
		RESTBase: mainSrv.URL + "/api/v4",
	})

	_, err := c.doGETPaginated(context.Background(), "/projects/1/merge_requests", nil, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("doGETPaginated: error = nil, want error for Link: rel=next pointing at a different host")
	}
	if !strings.Contains(err.Error(), "pagination") && !strings.Contains(err.Error(), "host") {
		t.Errorf("doGETPaginated error = %v, want a pagination/host-validation error", err)
	}
	if got := atomic.LoadInt32(&attackerHits); got != 0 {
		t.Errorf("attacker server received %d requests; want 0 — token must not be attached to a different-host pagination URL", got)
	}
	if got := atomic.LoadInt32(&mainHits); got != 1 {
		t.Errorf("main server received %d requests; want exactly 1 (the initial page, not the rejected next page)", got)
	}
}

// TestPaginationHTTPSDowngradeRejected verifies that a Link: rel="next" URL
// that downgrades from HTTPS (the configured REST base) to HTTP is rejected
// (review Item 6).
func TestPaginationHTTPSDowngradeRejected(t *testing.T) {
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve a next link that swaps https:// for http:// on the same host.
		downgraded := strings.Replace(r.Host, "https://", "http://", 1)
		_ = downgraded
		// Build the downgraded URL from the request's own host.
		host := r.Host
		nextURL := "http://" + host + r.URL.Path + "?page=2"
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, nextURL))
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(mainSrv.Close)

	// Construct an https REST base using the test server's host. The test
	// server itself is http, but we never actually follow the next URL — the
	// validation rejects it before any request is made. We point the client's
	// HTTPClient at the test server via a rewrite so the first page succeeds.
	restBase := strings.Replace(mainSrv.URL, "http://", "https://", 1) + "/api/v4"

	c := NewClient(ClientOptions{
		Token:    StaticTokenSource("secret-token"),
		RESTBase: restBase,
		HTTPClient: &http.Client{Transport: &schemeRewriteTransport{
			target: mainSrv.URL,
		}},
	})

	_, err := c.doGETPaginated(context.Background(), "/projects/1/merge_requests", nil, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("doGETPaginated: error = nil, want error for HTTPS→HTTP downgrade in Link: rel=next")
	}
}

// TestPaginationPortMismatchRejected verifies that a Link: rel="next" URL on
// the same host but a different port is rejected (review Item 6).
func TestPaginationPortMismatchRejected(t *testing.T) {
	var mainHits int32
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mainHits, 1)
		// Point the next link at the same host but a different port.
		host := r.Host
		// Strip the existing port (if any) and append a bogus one.
		if idx := strings.LastIndex(host, ":"); idx >= 0 {
			host = host[:idx]
		}
		nextURL := "http://" + host + ":9999/api/v4/projects/1/merge_requests?page=2"
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, nextURL))
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(mainSrv.Close)

	restBase := mainSrv.URL + "/api/v4"

	c := NewClient(ClientOptions{
		Token:    StaticTokenSource("secret-token"),
		RESTBase: restBase,
	})

	_, err := c.doGETPaginated(context.Background(), "/projects/1/merge_requests", nil, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("doGETPaginated: error = nil, want error for port mismatch in Link: rel=next")
	}
	if got := atomic.LoadInt32(&mainHits); got != 1 {
		t.Errorf("main server received %d requests; want exactly 1 (the initial page)", got)
	}
}

// TestPaginationSameHostHappyPath verifies that a Link: rel="next" URL on the
// same scheme, host, port, and API base is followed correctly (review Item 6).
func TestPaginationSameHostHappyPath(t *testing.T) {
	var pages []string
	var mainSrv *httptest.Server
	mainSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		if r.URL.Query().Get("page") == "" || r.URL.Query().Get("page") == "1" {
			// First page — serve a next link pointing at the same host.
			nextURL := mainSrv.URL + "/api/v4/projects/1/merge_requests?page=2"
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, nextURL))
			fmt.Fprint(w, `[{"iid":1}]`)
			return
		}
		// Second page — no more next links.
		fmt.Fprint(w, `[{"iid":2}]`)
	}))
	t.Cleanup(mainSrv.Close)

	c := NewClient(ClientOptions{
		Token:    StaticTokenSource("secret-token"),
		RESTBase: mainSrv.URL + "/api/v4",
	})

	var totalItems int
	_, err := c.doGETPaginated(context.Background(), "/projects/1/merge_requests", nil, func(body []byte) error {
		var items []struct {
			IID int `json:"iid"`
		}
		if err := json.Unmarshal(body, &items); err != nil {
			return err
		}
		totalItems += len(items)
		return nil
	})
	if err != nil {
		t.Fatalf("doGETPaginated happy path: %v", err)
	}
	if totalItems != 2 {
		t.Errorf("total items = %d, want 2 (both pages should be fetched)", totalItems)
	}
	if len(pages) != 2 {
		t.Errorf("pages fetched = %v, want [1 2] (two pages)", pages)
	}
}

// TestPaginationDifferentAPIBaseRejected verifies that a Link: rel="next" URL
// on the same host but a different API base path is rejected (review Item 6).
func TestPaginationDifferentAPIBaseRejected(t *testing.T) {
	var mainSrv *httptest.Server
	mainSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Next link points at the same host but a different API base path.
		nextURL := mainSrv.URL + "/api/v5/projects/1/merge_requests?page=2"
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, nextURL))
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(mainSrv.Close)

	c := NewClient(ClientOptions{
		Token:    StaticTokenSource("secret-token"),
		RESTBase: mainSrv.URL + "/api/v4",
	})

	_, err := c.doGETPaginated(context.Background(), "/projects/1/merge_requests", nil, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("doGETPaginated: error = nil, want error for different API base path in Link: rel=next")
	}
}

// schemeRewriteTransport rewrites the scheme of every request to match a target
// server URL. This lets a test present an https REST base to the client while
// routing the actual HTTP traffic to an httptest.NewServer (which is http).
type schemeRewriteTransport struct {
	target string // e.g. http://127.0.0.1:12345
}

func (t *schemeRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(req.Context())
	// Replace scheme+host with the test server's, keep the path/query.
	req2.URL.Scheme = target.Scheme
	req2.URL.Host = target.Host
	req2.Host = target.Host
	return http.DefaultTransport.RoundTrip(req2)
}
