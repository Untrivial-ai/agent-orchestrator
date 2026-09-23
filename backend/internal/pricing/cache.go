package pricing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrNoCachedCatalog indicates that no last-known-good manifest is installed.
var ErrNoCachedCatalog = errors.New("no cached pricing catalog")

// Cache stores the runtime last-known-good catalog below AO_DATA_DIR.
type Cache struct {
	root   string
	rename func(string, string) error
}

// NewCache returns a private v1 pricing cache rooted below dataDir.
func NewCache(dataDir string) *Cache {
	return &Cache{root: filepath.Join(dataDir, "pricing", "catalog", "v1"), rename: os.Rename}
}

// Root returns the resolved cache directory.
func (c *Cache) Root() string { return c.root }

// Load reads and validates the complete last-known-good catalog synchronously.
func (c *Cache) Load(ctx context.Context) (*Catalog, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(c.root, "manifest.json")
	manifestBytes, err := readBoundedFile(manifestPath, ManifestMaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoCachedCatalog
	}
	if err != nil {
		return nil, fmt.Errorf("read cached pricing manifest: %w", err)
	}
	var manifest catalogManifest
	if err := decodeStrictJSON(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("decode cached pricing manifest: %w", err)
	}
	if err := validateRuntimeManifest(manifest); err != nil {
		return nil, err
	}
	providers := make(map[string][]byte, len(manifest.Providers))
	for _, ref := range manifest.Providers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		contents, err := readBoundedFile(filepath.Join(c.root, filepath.FromSlash(ref.Path)), ProviderMaxBytes)
		if err != nil {
			return nil, fmt.Errorf("read cached provider %q: %w", ref.ProviderID, err)
		}
		providers[ref.Path] = contents
	}
	return ParseCatalog(manifestBytes, providers)
}

// Install commits a complete validated candidate with immutable blobs first
// and an atomic manifest replacement last.
func (c *Cache) Install(ctx context.Context, catalog *Catalog) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if catalog == nil || catalog.snapshot == nil {
		return errors.New("pricing catalog is nil")
	}
	if _, err := decodeCatalog(catalog.manifestBytes, catalog.providerBytes); err != nil {
		return fmt.Errorf("validate pricing cache candidate: %w", err)
	}
	if err := ensurePrivateDir(c.root); err != nil {
		return err
	}
	for _, ref := range catalog.manifest.Providers {
		if err := ctx.Err(); err != nil {
			return err
		}
		directory := filepath.Join(c.root, filepath.FromSlash(filepath.Dir(ref.Path)))
		if err := ensurePrivateDir(directory); err != nil {
			return err
		}
		providerPath := filepath.Join(c.root, filepath.FromSlash(ref.Path))
		if err := installImmutableFile(providerPath, catalog.providerBytes[ref.Path], c.rename); err != nil {
			return fmt.Errorf("install provider %q: %w", ref.ProviderID, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manifestPath := filepath.Join(c.root, "manifest.json")
	if err := atomicReplaceFile(manifestPath, catalog.manifestBytes, c.rename); err != nil {
		return fmt.Errorf("replace pricing manifest: %w", err)
	}
	return nil
}

func ensurePrivateDir(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create private pricing directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil { //nolint:gosec // Private directories require owner traversal.
		return fmt.Errorf("protect pricing directory: %w", err)
	}
	return nil
}

// installImmutableFile writes one content-addressed provider blob.
//
// A path whose contents already match is left untouched. A mismatch can only
// mean the local copy is corrupt: the path is the digest of the contents, so
// two different byte strings cannot legitimately share one. Install has already
// revalidated the candidate bytes against that digest, so overwriting is the
// only way a corrupt blob recovers — refusing would wedge every future refresh
// that references the same hash until the user deleted AO state by hand.
func installImmutableFile(target string, contents []byte, rename func(string, string) error) error {
	existing, err := os.ReadFile(target)
	switch {
	case err == nil && bytes.Equal(existing, contents):
		return os.Chmod(target, 0o600)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}
	return atomicReplaceFile(target, contents, rename)
}

func atomicReplaceFile(target string, contents []byte, rename func(string, string) error) (err error) {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".pricing-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() { _ = os.Remove(temporaryPath) }()
	defer func() {
		if closed {
			return
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	closed = true
	if err := rename(temporaryPath, target); err != nil {
		return err
	}
	return nil
}

func readBoundedFile(path string, limit int64) (contents []byte, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	contents, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, fmt.Errorf("file exceeds %d-byte limit", limit)
	}
	return contents, nil
}
