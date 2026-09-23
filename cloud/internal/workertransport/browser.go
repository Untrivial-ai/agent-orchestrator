package workertransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

const (
	maxBrowserRequestBody  = 1 << 20
	maxBrowserResponseBody = 1 << 20
)

var browserHTTPClient = newBrowserHTTPClient()

func newBrowserHTTPClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Timeout: 8 * time.Second, Jar: jar}
}

func fetchBrowser(ctx context.Context, input worker.BrowserFetchRequest) (worker.BrowserFetchResponse, error) {
	target, err := url.Parse(strings.TrimSpace(input.URL))
	if err != nil || target.Scheme == "" || target.Host == "" {
		return worker.BrowserFetchResponse{}, errors.New("browser URL is invalid")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return worker.BrowserFetchResponse{}, errors.New("browser URL must use http or https")
	}
	if len(input.Body) > maxBrowserRequestBody {
		return worker.BrowserFetchResponse{}, errors.New("browser request body exceeds 1 MiB")
	}
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = http.MethodGet
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(input.Body))
	if err != nil {
		return worker.BrowserFetchResponse{}, fmt.Errorf("build browser request: %w", err)
	}
	for _, name := range []string{"Accept", "Accept-Language", "Content-Type"} {
		if value := strings.TrimSpace(input.Headers[name]); value != "" {
			request.Header.Set(name, value)
		}
	}
	response, err := browserHTTPClient.Do(request)
	if err != nil {
		return worker.BrowserFetchResponse{}, fmt.Errorf("fetch browser URL: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBrowserResponseBody+1))
	if err != nil {
		return worker.BrowserFetchResponse{}, fmt.Errorf("read browser response: %w", err)
	}
	if len(body) > maxBrowserResponseBody {
		return worker.BrowserFetchResponse{}, errors.New("browser response exceeds 1 MiB")
	}
	return worker.BrowserFetchResponse{
		URL:          response.Request.URL.String(),
		Status:       response.StatusCode,
		ContentType:  response.Header.Get("Content-Type"),
		CacheControl: response.Header.Get("Cache-Control"),
		Body:         body,
	}, nil
}
