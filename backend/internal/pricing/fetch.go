package pricing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// RemoteManifestURL is the only production catalog origin AO contacts.
	RemoteManifestURL = "https://raw.githubusercontent.com/Untrivial-ai/agent-orchestrator/main/pricing/catalog/v1/manifest.json"
	// ManifestMaxBytes bounds the untrusted remote manifest response.
	ManifestMaxBytes int64 = 1 << 20
	// ProviderMaxBytes bounds each untrusted provider response.
	ProviderMaxBytes int64 = 8 << 20
	// HTTPTimeout bounds one complete catalog refresh attempt.
	HTTPTimeout = 20 * time.Second
)

// FetchResult reports either a complete validated catalog or a valid 304.
type FetchResult struct {
	Catalog     *Catalog
	ETag        string
	NotModified bool
}

// Fetcher retrieves a complete catalog from one fixed manifest origin.
type Fetcher struct {
	client      *http.Client
	manifestURL *url.URL
	origin      string
}

// NewProductionFetcher creates a fetcher for AO's reviewed GitHub catalog.
func NewProductionFetcher(client *http.Client) (*Fetcher, error) {
	return NewFetcher(client, RemoteManifestURL)
}

// NewFetcher fixes all requests and redirects to the supplied manifest origin.
// The URL parameter exists so offline tests can use httptest.
func NewFetcher(client *http.Client, manifestURL string) (*Fetcher, error) {
	if client == nil {
		return nil, errors.New("pricing HTTP client is nil")
	}
	parsed, err := url.Parse(manifestURL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("pricing manifest URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if !strings.HasSuffix(parsed.Path, "/manifest.json") {
		return nil, errors.New("pricing manifest URL must end in /manifest.json")
	}
	origin := parsed.Scheme + "://" + parsed.Host
	clone := *client
	priorRedirect := client.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme+"://"+request.URL.Host != origin {
			return errors.New("pricing redirect changed origin")
		}
		if priorRedirect != nil {
			return priorRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &Fetcher{client: &clone, manifestURL: parsed, origin: origin}, nil
}

// Fetch retrieves and validates the complete catalog. A cacheless 304 is
// invalid and is retried exactly once without If-None-Match.
func (f *Fetcher) Fetch(ctx context.Context, etag string, cacheAvailable bool) (FetchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, HTTPTimeout)
	defer cancel()
	result, err := f.fetch(ctx, etag)
	if err != nil {
		return FetchResult{}, err
	}
	if result.NotModified && !cacheAvailable {
		retry, retryErr := f.fetch(ctx, "")
		if retryErr != nil {
			return FetchResult{}, retryErr
		}
		if retry.NotModified {
			return FetchResult{}, errors.New("pricing server returned 304 without a usable cache validator")
		}
		return retry, nil
	}
	if result.NotModified && etag == "" {
		return FetchResult{}, errors.New("pricing server returned an unconditional 304")
	}
	return result, nil
}

func (f *Fetcher) fetch(ctx context.Context, etag string) (FetchResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.manifestURL.String(), http.NoBody)
	if err != nil {
		return FetchResult{}, err
	}
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := f.client.Do(request) //nolint:bodyclose // readHTTPResponse owns and closes every returned body.
	if err != nil {
		return FetchResult{}, normalizeContextError(ctx, err)
	}
	manifestBytes, status, responseETag, err := readHTTPResponse(response, ManifestMaxBytes)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch pricing manifest: %w", err)
	}
	if status == http.StatusNotModified {
		if responseETag != "" && etag != "" && responseETag != etag {
			return FetchResult{}, errors.New("pricing 304 ETag does not match request validator")
		}
		return FetchResult{ETag: etag, NotModified: true}, nil
	}
	if status != http.StatusOK {
		return FetchResult{}, fmt.Errorf("fetch pricing manifest: unexpected HTTP status %d", status)
	}
	var manifest catalogManifest
	if err := decodeStrictJSON(manifestBytes, &manifest); err != nil {
		return FetchResult{}, fmt.Errorf("decode pricing manifest: %w", err)
	}
	if err := validateRuntimeManifest(manifest); err != nil {
		return FetchResult{}, err
	}
	providerBytes := make(map[string][]byte, len(manifest.Providers))
	for _, ref := range manifest.Providers {
		providerURL, err := f.providerURL(ref.Path)
		if err != nil {
			return FetchResult{}, err
		}
		providerRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, providerURL, http.NoBody)
		if err != nil {
			return FetchResult{}, err
		}
		providerResponse, err := f.client.Do(providerRequest) //nolint:bodyclose // readHTTPResponse owns and closes every returned body.
		if err != nil {
			return FetchResult{}, normalizeContextError(ctx, err)
		}
		contents, providerStatus, _, err := readHTTPResponse(providerResponse, ProviderMaxBytes)
		if err != nil {
			return FetchResult{}, fmt.Errorf("fetch provider %q: %w", ref.ProviderID, err)
		}
		if providerStatus != http.StatusOK {
			return FetchResult{}, fmt.Errorf("fetch provider %q: unexpected HTTP status %d", ref.ProviderID, providerStatus)
		}
		providerBytes[ref.Path] = contents
	}
	catalog, err := ParseCatalog(manifestBytes, providerBytes)
	if err != nil {
		return FetchResult{}, fmt.Errorf("validate fetched pricing catalog: %w", err)
	}
	return FetchResult{Catalog: catalog, ETag: responseETag}, nil
}

func (f *Fetcher) providerURL(relativePath string) (string, error) {
	if !safeProviderPath(relativePath) {
		return "", fmt.Errorf("unsafe provider path %q", relativePath)
	}
	reference, err := url.Parse(relativePath)
	if err != nil || reference.IsAbs() || reference.Host != "" {
		return "", fmt.Errorf("provider path %q is not relative", relativePath)
	}
	base := *f.manifestURL
	base.Path = strings.TrimSuffix(base.Path, "manifest.json")
	resolved := base.ResolveReference(reference)
	if resolved.Scheme+"://"+resolved.Host != f.origin {
		return "", fmt.Errorf("provider path %q changed origin", relativePath)
	}
	return resolved.String(), nil
}

func readHTTPResponse(response *http.Response, limit int64) (contents []byte, status int, etag string, err error) {
	defer func() {
		if closeErr := response.Body.Close(); err == nil {
			err = closeErr
		}
	}()
	contents, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, 0, "", err
	}
	if int64(len(contents)) > limit {
		return nil, 0, "", fmt.Errorf("response exceeds %d-byte limit", limit)
	}
	return contents, response.StatusCode, response.Header.Get("ETag"), nil
}

func normalizeContextError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return err
}
