package controllers_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/linkpreview"
)

// linkPreviewRig pairs the daemon-under-test with the origin it unfurls.
type linkPreviewRig struct {
	daemon    *httptest.Server
	origin    *httptest.Server
	originHit *atomic.Int32
}

// newLinkPreviewRig serves originBody (with contentType) from a fake origin
// and mounts the real router with a linkpreview.Service pointed at it.
func newLinkPreviewRig(t *testing.T, contentType, originBody string, originStatus int) linkPreviewRig {
	t.Helper()
	return newLinkPreviewRigWithHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if originStatus != 0 {
			w.WriteHeader(originStatus)
		}
		_, _ = io.WriteString(w, originBody)
	})
}

func newLinkPreviewRigWithHandler(t *testing.T, handler http.HandlerFunc) linkPreviewRig {
	t.Helper()
	hits := &atomic.Int32{}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(origin.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	daemon := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		LinkPreview: linkpreview.New(origin.Client()),
	}, httpd.ControlDeps{}))
	t.Cleanup(daemon.Close)
	return linkPreviewRig{daemon: daemon, origin: origin, originHit: hits}
}

func (rig linkPreviewRig) get(t *testing.T, rawURL string) *http.Response {
	t.Helper()
	res, err := rig.daemon.Client().Get(rig.daemon.URL + "/api/v1/link-preview?url=" + url.QueryEscape(rawURL))
	if err != nil {
		t.Fatalf("GET link-preview: %v", err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func decodeBody(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode %d body: %v", res.StatusCode, err)
	}
	return got
}

const ogPage = `<!DOCTYPE html>
<html><head>
<meta property="og:title" content="The OG Title">
<meta property="og:description" content="An OG description.">
<meta property="og:image" content="/static/og.png">
<meta property="og:site_name" content="Example Site">
<link rel="icon" href="/icon.svg">
<title>Fallback Title</title>
</head><body>content</body></html>`

func TestLinkPreviewReadsOpenGraphTags(t *testing.T) {
	rig := newLinkPreviewRig(t, "text/html; charset=utf-8", ogPage, 0)

	res := rig.get(t, rig.origin.URL+"/article")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", res.StatusCode)
	}
	got := decodeBody(t, res)
	want := map[string]string{
		"url":         rig.origin.URL + "/article",
		"title":       "The OG Title",
		"description": "An OG description.",
		"imageUrl":    rig.origin.URL + "/static/og.png", // relative og:image resolved against the page URL
		"siteName":    "Example Site",
		"faviconUrl":  rig.origin.URL + "/icon.svg",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %q", k, got[k], v)
		}
	}
}

func TestLinkPreviewFallsBackToTitleAndMetaDescription(t *testing.T) {
	page := `<html><head>
<meta name="description" content="Plain description.">
<title>Plain Title</title>
</head><body></body></html>`
	rig := newLinkPreviewRig(t, "text/html", page, 0)

	res := rig.get(t, rig.origin.URL+"/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", res.StatusCode)
	}
	got := decodeBody(t, res)
	if got["title"] != "Plain Title" {
		t.Errorf("title = %v", got["title"])
	}
	if got["description"] != "Plain description." {
		t.Errorf("description = %v", got["description"])
	}
	if _, ok := got["imageUrl"]; ok {
		t.Errorf("imageUrl should be omitted when the page has none, got %v", got["imageUrl"])
	}
	// No <link rel="icon">: the origin-root /favicon.ico fallback applies.
	if got["faviconUrl"] != rig.origin.URL+"/favicon.ico" {
		t.Errorf("faviconUrl = %v", got["faviconUrl"])
	}
}

func TestLinkPreviewRejectsInvalidURLs(t *testing.T) {
	rig := newLinkPreviewRig(t, "text/html", ogPage, 0)

	for _, raw := range []string{"", "not-a-url", "ftp://example.com/x", "example.com/relative"} {
		res := rig.get(t, raw)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("url %q: got %d, want 400", raw, res.StatusCode)
			continue
		}
		got := decodeBody(t, res)
		if got["error"] != "bad_request" {
			t.Errorf("url %q: error = %v", raw, got["error"])
		}
	}
	if rig.originHit.Load() != 0 {
		t.Errorf("invalid URLs must never reach the origin, got %d hits", rig.originHit.Load())
	}
}

func TestLinkPreviewRejectsNonHTML(t *testing.T) {
	rig := newLinkPreviewRig(t, "application/json", `{"ok":true}`, 0)

	res := rig.get(t, rig.origin.URL+"/data.json")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d, want 404", res.StatusCode)
	}
	if got := decodeBody(t, res); got["code"] != "LINK_PREVIEW_NOT_FOUND" {
		t.Errorf("code = %v", got["code"])
	}
}

func TestLinkPreviewReportsUpstreamFailureAsBadGateway(t *testing.T) {
	rig := newLinkPreviewRig(t, "text/html", "boom", http.StatusInternalServerError)

	res := rig.get(t, rig.origin.URL+"/")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("got %d, want 502", res.StatusCode)
	}
	if got := decodeBody(t, res); got["code"] != "LINK_PREVIEW_FETCH_FAILED" {
		t.Errorf("code = %v", got["code"])
	}
}

func TestLinkPreviewCachesSuccessAndFailure(t *testing.T) {
	rig := newLinkPreviewRigWithHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"error":"gone"}`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, ogPage)
	})

	// Two requests for the same URL hit the origin once.
	for i := 0; i < 2; i++ {
		if res := rig.get(t, rig.origin.URL+"/article"); res.StatusCode != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i, res.StatusCode)
		}
	}
	if got := rig.originHit.Load(); got != 1 {
		t.Fatalf("origin hits = %d, want 1 (second request served from cache)", got)
	}

	// Failures are cached too, under the shorter TTL.
	for i := 0; i < 2; i++ {
		if res := rig.get(t, rig.origin.URL+"/missing"); res.StatusCode != http.StatusNotFound {
			t.Fatalf("request %d: got %d, want 404", i, res.StatusCode)
		}
	}
	if got := rig.originHit.Load(); got != 2 {
		t.Fatalf("origin hits = %d, want 2 (cached failure replayed)", got)
	}
}

func TestLinkPreviewWithoutServiceAnswers501(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	res, err := srv.Client().Get(srv.URL + "/api/v1/link-preview?url=" + url.QueryEscape("https://example.com"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("got %d, want 501", res.StatusCode)
	}
}

func TestLinkPreviewFollowsRedirectAndReportsFinalURL(t *testing.T) {
	rig := newLinkPreviewRigWithHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/short" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, ogPage)
	})

	res := rig.get(t, rig.origin.URL+"/short")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", res.StatusCode)
	}
	got := decodeBody(t, res)
	if got["url"] != rig.origin.URL+"/final" {
		t.Errorf("url = %v, want the final post-redirect URL", got["url"])
	}
	// Assets resolve against the final URL too.
	if got["imageUrl"] != rig.origin.URL+"/static/og.png" {
		t.Errorf("imageUrl = %v", got["imageUrl"])
	}
}

func TestLinkPreviewRejectsMissingContentType(t *testing.T) {
	// Header()["Content-Type"] = nil suppresses net/http's sniffing, so the
	// response really carries no Content-Type even though the body looks like HTML.
	rig := newLinkPreviewRigWithHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Content-Type"] = nil
		_, _ = io.WriteString(w, ogPage)
	})

	res := rig.get(t, rig.origin.URL+"/")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d, want 404", res.StatusCode)
	}
	if got := decodeBody(t, res); got["code"] != "LINK_PREVIEW_NOT_FOUND" {
		t.Errorf("code = %v", got["code"])
	}
}
