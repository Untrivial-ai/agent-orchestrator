package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type cacheTestTransport func(*http.Request) (*http.Response, error)

func (f cacheTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type cacheRequestKey struct{}

func cacheTestRequest(ctx context.Context, client *Client, stage, path string) (RESTResponse, error) {
	return client.doREST(context.WithValue(ctx, cacheRequestKey{}, stage), http.MethodGet, path, nil, nil)
}

func cacheTestResponse(status int, etag, body string) *http.Response {
	header := make(http.Header)
	if etag != "" {
		header.Set("ETag", etag)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestRESTCacheRotatedETagDoesNotReplaceCurrentEntry(t *testing.T) {
	for _, interleave := range []string{"replacement", "eviction", "eviction_then_reinsert", "replacement_then_original"} {
		t.Run(interleave, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started, release := make(chan struct{}), make(chan struct{})
			const path = "/repos/example/project/pulls/7"
			const oldETag, oldBody = `W/"original"`, `{"state":"open"}`
			const nextETag, nextBody = `W/"replacement"`, `{"state":"closed"}`
			const rotatedETag = `W/"original-revalidated"`
			wantETag, wantBody := nextETag, nextBody
			switch interleave {
			case "eviction":
				wantETag = ""
			case "eviction_then_reinsert", "replacement_then_original":
				wantETag, wantBody = oldETag, oldBody
			}
			client := NewClient(ClientOptions{HTTPClient: &http.Client{Transport: cacheTestTransport(func(req *http.Request) (*http.Response, error) {
				switch req.Context().Value(cacheRequestKey{}) {
				case "seed", "reinsert":
					return cacheTestResponse(http.StatusOK, oldETag, oldBody), nil
				case "conditional":
					if got := req.Header.Get("If-None-Match"); got != oldETag {
						return nil, fmt.Errorf("conditional validator = %q, want %q", got, oldETag)
					}
					close(started)
					select {
					case <-release:
						return cacheTestResponse(http.StatusNotModified, rotatedETag, ""), nil
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				case "replace", "fill":
					return cacheTestResponse(http.StatusOK, nextETag, nextBody), nil
				case "probe":
					if got := req.Header.Get("If-None-Match"); got != wantETag {
						return nil, fmt.Errorf("subsequent validator = %q, want %q after %s", got, wantETag, interleave)
					}
					if wantETag == "" {
						return cacheTestResponse(http.StatusOK, nextETag, nextBody), nil
					}
					return cacheTestResponse(http.StatusNotModified, wantETag, ""), nil
				default:
					return nil, fmt.Errorf("unexpected request stage: %v", req.Context().Value(cacheRequestKey{}))
				}
			})}})
			if _, err := cacheTestRequest(ctx, client, "seed", path); err != nil {
				t.Fatal(err)
			}
			type result struct {
				response RESTResponse
				err      error
			}
			done := make(chan result, 1)
			go func() {
				response, err := cacheTestRequest(ctx, client, "conditional", path)
				done <- result{response, err}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("conditional request did not start")
			}
			if interleave == "replacement" || interleave == "replacement_then_original" {
				if _, err := cacheTestRequest(ctx, client, "replace", path); err != nil {
					t.Fatal(err)
				}
			} else {
				for i := 0; i < cacheMaxEntries; i++ {
					if _, err := cacheTestRequest(ctx, client, "fill", fmt.Sprintf("/repos/example/project/pulls/%d", i+100)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if interleave == "eviction_then_reinsert" || interleave == "replacement_then_original" {
				if _, err := cacheTestRequest(ctx, client, "reinsert", path); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			select {
			case got := <-done:
				if got.err != nil {
					t.Fatal(got.err)
				}
				if got.response.StatusCode != http.StatusNotModified || !got.response.NotModified || got.response.ETag != rotatedETag || string(got.response.Body) != oldBody {
					t.Fatalf("revalidated snapshot = %+v, want original body with rotated validator", got.response)
				}
			case <-ctx.Done():
				t.Fatal("conditional request did not finish")
			}
			response, err := cacheTestRequest(ctx, client, "probe", path)
			if err != nil {
				t.Fatal(err)
			}
			if string(response.Body) != wantBody {
				t.Fatalf("subsequent body = %q, want %q", response.Body, wantBody)
			}
		})
	}
}

func TestRESTCacheRevalidationAndBodyOwnership(t *testing.T) {
	for _, body := range []string{`{"state":"open"}`, ""} {
		for _, responseETag := range []string{`W/"rotated"`, `W/"original"`, ""} {
			t.Run(fmt.Sprintf("body=%q/etag=%q", body, responseETag), func(t *testing.T) {
				const originalETag = `W/"original"`
				wantETag := originalETag
				if responseETag != "" {
					wantETag = responseETag
				}
				calls := 0
				client := NewClient(ClientOptions{
					Token: StaticTokenSource("test-token"),
					HTTPClient: &http.Client{Transport: cacheTestTransport(func(req *http.Request) (*http.Response, error) {
						calls++
						if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer test-token" || req.Header.Get("Accept") != "application/vnd.github+json" || req.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
							return nil, fmt.Errorf("unexpected REST request contract: %s %+v", req.Method, req.Header)
						}
						expected := ""
						if calls == 2 {
							expected = originalETag
						} else if calls > 2 {
							expected = wantETag
						}
						if got := req.Header.Get("If-None-Match"); got != expected {
							return nil, fmt.Errorf("request %d validator = %q, want %q", calls, got, expected)
						}
						if calls == 1 {
							return cacheTestResponse(http.StatusOK, originalETag, body), nil
						}
						return cacheTestResponse(http.StatusNotModified, responseETag, ""), nil
					})},
				})
				for i := 0; i < 4; i++ {
					response, err := cacheTestRequest(context.Background(), client, "", "/repos/example/project/pulls/7")
					if err != nil {
						t.Fatal(err)
					}
					if string(response.Body) != body {
						t.Fatalf("response %d body = %q, want %q", i, response.Body, body)
					}
					if i > 0 && (response.StatusCode != http.StatusNotModified || !response.NotModified || response.ETag != wantETag) {
						t.Fatalf("revalidated response = %+v, want ETag %q", response, wantETag)
					}
					if len(response.Body) > 0 {
						response.Body[0] = '!'
					}
				}
			})
		}
	}
}

func TestRESTCacheNotModifiedWithoutEntry(t *testing.T) {
	calls := 0
	client := NewClient(ClientOptions{HTTPClient: &http.Client{Transport: cacheTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if got := req.Header.Get("If-None-Match"); got != "" {
			return nil, fmt.Errorf("uncached request sent validator %q", got)
		}
		return cacheTestResponse(http.StatusNotModified, `W/"unsolicited"`, ""), nil
	})}})
	for i := 0; i < 2; i++ {
		response, err := cacheTestRequest(context.Background(), client, "", "/repos/example/project/pulls/7")
		if err == nil || response.StatusCode != http.StatusNotModified || response.NotModified || response.ETag != "" || len(response.Body) != 0 {
			t.Fatalf("uncached 304 = %+v, %v; want an error without a usable cached response", response, err)
		}
	}
	if calls != 2 {
		t.Fatalf("requests = %d, want 2", calls)
	}
}

func TestRESTCacheOverlappingRotationsKeepFirstCompletedEntry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan string, 2)
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	client := NewClient(ClientOptions{HTTPClient: &http.Client{Transport: cacheTestTransport(func(req *http.Request) (*http.Response, error) {
		stage, _ := req.Context().Value(cacheRequestKey{}).(string)
		switch stage {
		case "seed":
			return cacheTestResponse(http.StatusOK, `W/"original"`, "original body"), nil
		case "first", "second":
			if got := req.Header.Get("If-None-Match"); got != `W/"original"` {
				return nil, fmt.Errorf("%s validator = %q, want original", stage, got)
			}
			started <- stage
			release := firstRelease
			if stage == "second" {
				release = secondRelease
			}
			select {
			case <-release:
				return cacheTestResponse(http.StatusNotModified, fmt.Sprintf(`W/"%s"`, stage), ""), nil
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		case "probe":
			if got := req.Header.Get("If-None-Match"); got != `W/"first"` {
				return nil, fmt.Errorf("completed rotation was overwritten: validator = %q, want first", got)
			}
			return cacheTestResponse(http.StatusNotModified, "", ""), nil
		default:
			return nil, fmt.Errorf("unexpected request stage %q", stage)
		}
	})}})
	const path = "/repos/example/project/pulls/7"
	if _, err := cacheTestRequest(ctx, client, "seed", path); err != nil {
		t.Fatal(err)
	}
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	for _, request := range []struct {
		stage string
		done  chan error
	}{{"first", firstDone}, {"second", secondDone}} {
		go func() {
			response, err := cacheTestRequest(ctx, client, request.stage, path)
			if err == nil && (response.ETag != fmt.Sprintf(`W/"%s"`, request.stage) || string(response.Body) != "original body") {
				err = fmt.Errorf("%s response = %+v, want its revalidated snapshot", request.stage, response)
			}
			request.done <- err
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("both conditional requests did not start")
		}
	}
	for _, step := range []struct {
		release chan struct{}
		done    chan error
	}{{firstRelease, firstDone}, {secondRelease, secondDone}} {
		close(step.release)
		select {
		case err := <-step.done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("conditional request did not finish")
		}
	}
	if _, err := cacheTestRequest(ctx, client, "probe", path); err != nil {
		t.Fatal(err)
	}
}

func TestRESTCacheConcurrentResponseOwnership(t *testing.T) {
	client := NewClient(ClientOptions{HTTPClient: &http.Client{Transport: cacheTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("If-None-Match") == "" {
			return cacheTestResponse(http.StatusOK, `W/"original"`, "original body"), nil
		}
		return cacheTestResponse(http.StatusNotModified, `W/"original"`, ""), nil
	})}})
	const path = "/repos/example/project/pulls/7"
	if _, err := cacheTestRequest(context.Background(), client, "", path); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			response, err := cacheTestRequest(context.Background(), client, "", path)
			if err != nil {
				t.Error(err)
				return
			}
			if string(response.Body) != "original body" {
				t.Errorf("body = %q, want original body", response.Body)
				return
			}
			response.Body[0] = '!'
		})
	}
	wg.Wait()
	response, err := cacheTestRequest(context.Background(), client, "", path)
	if err != nil || string(response.Body) != "original body" {
		t.Fatalf("cached response after caller mutations = %+v, %v", response, err)
	}
}
