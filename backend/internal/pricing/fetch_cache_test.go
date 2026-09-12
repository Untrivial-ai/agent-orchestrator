package pricing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetcherUsesETagAndRetriesCacheless304WithoutValidator(t *testing.T) {
	fixture := testCandidate(t, testBaseModels("0.1"))
	var manifestRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/manifest.json") {
			request := manifestRequests.Add(1)
			if request == 1 {
				if got := r.Header.Get("If-None-Match"); got != `"catalog-v1"` {
					t.Errorf("If-None-Match = %q", got)
				}
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if got := r.Header.Get("If-None-Match"); got != "" {
				t.Errorf("cacheless retry If-None-Match = %q, want empty", got)
			}
			w.Header().Set("ETag", `"catalog-v2"`)
			_, _ = w.Write(fixture.manifest)
			return
		}
		relative := strings.TrimPrefix(r.URL.Path, "/catalog/v1/")
		contents, ok := fixture.providers[relative]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(contents)
	}))
	defer server.Close()

	fetcher, err := NewFetcher(server.Client(), server.URL+"/catalog/v1/manifest.json")
	if err != nil {
		t.Fatalf("NewFetcher: %v", err)
	}
	result, err := fetcher.Fetch(context.Background(), `"catalog-v1"`, false)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.NotModified || result.Catalog == nil || result.ETag != `"catalog-v2"` || manifestRequests.Load() != 2 {
		t.Fatalf("Fetch result = %#v, requests=%d", result, manifestRequests.Load())
	}
}

func TestFetcherReturns304WhenCacheExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != `"same"` {
			t.Errorf("If-None-Match = %q", got)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()
	fetcher, err := NewFetcher(server.Client(), server.URL+"/catalog/v1/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	result, err := fetcher.Fetch(context.Background(), `"same"`, true)
	if err != nil || !result.NotModified || result.Catalog != nil {
		t.Fatalf("Fetch = %#v, %v", result, err)
	}
}

func TestFetcherRejectsMismatchedOrUnconditional304(t *testing.T) {
	t.Run("response validator differs", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("ETag", `"other"`)
			w.WriteHeader(http.StatusNotModified)
		}))
		defer server.Close()
		fetcher, err := NewFetcher(server.Client(), server.URL+"/catalog/v1/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fetcher.Fetch(context.Background(), `"expected"`, true); err == nil {
			t.Fatal("mismatched 304 ETag error = nil")
		}
	})

	t.Run("cacheless retry is also 304", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusNotModified)
		}))
		defer server.Close()
		fetcher, err := NewFetcher(server.Client(), server.URL+"/catalog/v1/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fetcher.Fetch(context.Background(), `"stale"`, false); err == nil {
			t.Fatal("second cacheless 304 error = nil")
		}
		if got := requests.Load(); got != 2 {
			t.Fatalf("manifest requests = %d, want 2", got)
		}
	})
}

func TestFetcherRejectsOversizeAndCrossOriginRedirect(t *testing.T) {
	t.Run("manifest exceeds one MiB", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(make([]byte, ManifestMaxBytes+1))
		}))
		defer server.Close()
		fetcher, err := NewFetcher(server.Client(), server.URL+"/catalog/v1/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fetcher.Fetch(context.Background(), "", false); err == nil {
			t.Fatal("oversize Fetch error = nil")
		}
	})

	t.Run("provider exceeds eight MiB", func(t *testing.T) {
		fixture := testCandidate(t, testBaseModels("0.1"))
		var providerRequests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/manifest.json") {
				_, _ = w.Write(fixture.manifest)
				return
			}
			providerRequests.Add(1)
			_, _ = w.Write(make([]byte, ProviderMaxBytes+1))
		}))
		defer server.Close()
		fetcher, err := NewFetcher(server.Client(), server.URL+"/catalog/v1/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fetcher.Fetch(context.Background(), "", false); err == nil {
			t.Fatal("oversize provider Fetch error = nil")
		}
		if got := providerRequests.Load(); got != 1 {
			t.Fatalf("provider requests = %d, want fetch to stop after first oversize body", got)
		}
	})

	t.Run("redirect changes origin", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/catalog/v1/manifest.json", http.StatusFound)
		}))
		defer server.Close()
		fetcher, err := NewFetcher(server.Client(), server.URL+"/catalog/v1/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fetcher.Fetch(context.Background(), "", false); err == nil {
			t.Fatal("cross-origin redirect error = nil")
		}
	})
}

func TestFetcherCancellationStopsRequest(t *testing.T) {
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(requestCanceled)
	}))
	defer server.Close()
	fetcher, err := NewFetcher(server.Client(), server.URL+"/catalog/v1/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetcher.Fetch(ctx, "", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("Fetch error = %v, want context canceled", err)
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		// A request canceled before transport admission is equally valid.
	}
}

func TestCacheInstallsPrivateManifestLastAndLoadsLKG(t *testing.T) {
	dataDir := t.TempDir()
	fixture := testCandidate(t, testBaseModels("0.1"))
	catalog, err := ParseCatalog(fixture.manifest, fixture.providers)
	if err != nil {
		t.Fatal(err)
	}
	cache := NewCache(dataDir)
	if err := cache.Install(t.Context(), catalog); err != nil {
		t.Fatalf("Install: %v", err)
	}
	loaded, err := cache.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.Snapshot().ProviderVersion("openai"); got != fixture.versions["openai"] {
		t.Fatalf("loaded openai version = %q", got)
	}
	for _, file := range append([]string{filepath.Join(cache.Root(), "manifest.json")}, providerFilePaths(t, cache.Root(), fixture)...) {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %o, want 600", file, got)
		}
	}
	if info, err := os.Stat(cache.Root()); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("cache root mode = %v, %v", info.Mode().Perm(), err)
	}
}

// Break caught: shutdown cancellation was dropped at the cache boundary, so a
// refresh kept walking provider files and could still commit its manifest.
func TestCacheHonorsCancellationBeforeLoadAndBetweenProviderInstalls(t *testing.T) {
	fixture := testCandidate(t, testBaseModels("0.1"))
	catalog, err := ParseCatalog(fixture.manifest, fixture.providers)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("load already canceled", func(t *testing.T) {
		cache := NewCache(t.TempDir())
		if err := cache.Install(context.Background(), catalog); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := cache.Load(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Load error = %v, want context canceled", err)
		}
	})

	t.Run("install canceled between providers", func(t *testing.T) {
		cache := NewCache(t.TempDir())
		ctx, cancel := context.WithCancel(context.Background())
		realRename := cache.rename
		providerRenames := 0
		cache.rename = func(oldPath, newPath string) error {
			if err := realRename(oldPath, newPath); err != nil {
				return err
			}
			if filepath.Base(newPath) != "manifest.json" {
				providerRenames++
				if providerRenames == 1 {
					cancel()
				}
			}
			return nil
		}
		if err := cache.Install(ctx, catalog); !errors.Is(err, context.Canceled) {
			t.Fatalf("Install error = %v, want context canceled", err)
		}
		if providerRenames != 1 {
			t.Fatalf("provider installs after cancellation = %d, want 1", providerRenames)
		}
		if _, err := os.Stat(filepath.Join(cache.Root(), "manifest.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("manifest committed after cancellation: %v", err)
		}
	})
}

func TestCacheManifestReplaceFailurePreservesOldLKG(t *testing.T) {
	dataDir := t.TempDir()
	cache := NewCache(dataDir)
	oldFixture := testCandidate(t, testBaseModels("0.1"))
	oldCatalog, err := ParseCatalog(oldFixture.manifest, oldFixture.providers)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Install(t.Context(), oldCatalog); err != nil {
		t.Fatal(err)
	}
	newFixture := testCandidate(t, testBaseModels("0.2"))
	newCatalog, err := ParseCatalog(newFixture.manifest, newFixture.providers)
	if err != nil {
		t.Fatal(err)
	}
	realRename := cache.rename
	cache.rename = func(oldPath, newPath string) error {
		if filepath.Base(newPath) == "manifest.json" {
			return errors.New("injected manifest replace failure")
		}
		return realRename(oldPath, newPath)
	}
	if err := cache.Install(t.Context(), newCatalog); err == nil {
		t.Fatal("Install error = nil")
	}
	cache.rename = realRename
	loaded, err := cache.Load(t.Context())
	if err != nil {
		t.Fatalf("Load old LKG: %v", err)
	}
	if got := loaded.Snapshot().ProviderVersion("anthropic"); got != oldFixture.versions["anthropic"] {
		t.Fatalf("version after failed install = %q, want old %q", got, oldFixture.versions["anthropic"])
	}
}

func TestCacheMissingOrCorruptLKGIsUnavailable(t *testing.T) {
	cache := NewCache(t.TempDir())
	if _, err := cache.Load(t.Context()); !errors.Is(err, ErrNoCachedCatalog) {
		t.Fatalf("missing Load error = %v", err)
	}
	if err := os.MkdirAll(cache.Root(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache.Root(), "manifest.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Load(t.Context()); err == nil || errors.Is(err, ErrNoCachedCatalog) {
		t.Fatalf("corrupt Load error = %v", err)
	}
}

// A content-addressed path whose bytes no longer hash to it is corrupt, and the
// refresher's only route back is to reinstall the validated remote copy. If
// Install refused that write, every retry would download the same valid bytes
// and fail again, leaving pricing permanently unavailable until the user
// deleted AO state by hand.
func TestCacheInstallRepairsCorruptProviderBlob(t *testing.T) {
	dataDir := t.TempDir()
	fixture := testCandidate(t, testBaseModels("0.1"))
	catalog, err := ParseCatalog(fixture.manifest, fixture.providers)
	if err != nil {
		t.Fatal(err)
	}
	cache := NewCache(dataDir)
	if err := cache.Install(t.Context(), catalog); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	corrupted := providerFilePaths(t, cache.Root(), fixture)[0]
	if err := os.WriteFile(corrupted, []byte(`{"providerId":"openai","models":[]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Load(t.Context()); err == nil {
		t.Fatal("corrupt provider blob loaded successfully")
	}

	if err := cache.Install(t.Context(), catalog); err != nil {
		t.Fatalf("reinstall over corrupt blob: %v", err)
	}
	loaded, err := cache.Load(t.Context())
	if err != nil {
		t.Fatalf("Load after repair: %v", err)
	}
	if got := loaded.Snapshot().ProviderVersion("openai"); got != fixture.versions["openai"] {
		t.Fatalf("repaired openai version = %q, want %q", got, fixture.versions["openai"])
	}
	if info, err := os.Stat(corrupted); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("repaired blob mode = %v, %v", info.Mode().Perm(), err)
	}
}

func providerFilePaths(t *testing.T, root string, fixture testCatalog) []string {
	t.Helper()
	paths := make([]string, 0, len(fixture.providers))
	for relative := range fixture.providers {
		paths = append(paths, filepath.Join(root, filepath.FromSlash(relative)))
	}
	return paths
}

func TestProductionManifestURLIsReviewedAOCatalog(t *testing.T) {
	if RemoteManifestURL != "https://raw.githubusercontent.com/Untrivial-ai/agent-orchestrator/main/pricing/catalog/v1/manifest.json" {
		t.Fatalf("RemoteManifestURL = %q", RemoteManifestURL)
	}
	if _, err := NewFetcher(http.DefaultClient, "ftp://example.com/manifest.json"); err == nil {
		t.Fatal("unsupported manifest scheme accepted")
	}
	if fmt.Sprint(HTTPTimeout) != "20s" {
		t.Fatalf("HTTPTimeout = %s", HTTPTimeout)
	}
}
