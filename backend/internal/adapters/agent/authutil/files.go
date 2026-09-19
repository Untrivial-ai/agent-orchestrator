package authutil

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// MaxFileSize bounds credential files and captured command output to 1 MiB.
const MaxFileSize = 1 << 20

// ReadFile rejects symlinks/non-regular files and never includes a path or
// underlying I/O error in its errors. An injected reader must itself honor
// context-independent resource limits; returned data is checked again here.
func ReadFile(ctx context.Context, d Dependencies, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := d.lstat(path)
	if err != nil {
		return nil, errors.New("credential file unavailable")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("credential file is not regular")
	}
	if info.Size() > MaxFileSize {
		return nil, errors.New("credential file exceeds limit")
	}
	var data []byte
	if d.ReadFile != nil {
		data, err = d.ReadFile(path)
	} else {
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil, errors.New("credential file unavailable")
		}
		defer func() { _ = file.Close() }()
		opened, statErr := file.Stat()
		if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			return nil, errors.New("credential file changed while opening")
		}
		data, err = io.ReadAll(io.LimitReader(file, MaxFileSize+1))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, errors.New("credential file unreadable")
	}
	if len(data) > MaxFileSize {
		return nil, errors.New("credential file exceeds limit")
	}
	return data, nil
}

// ReadJSON decodes one complete JSON value into an adapter-owned schema.
func ReadJSON(ctx context.Context, d Dependencies, path string, dst any) error {
	data, err := ReadFile(ctx, d, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return errors.New("invalid credential JSON")
	}
	return nil
}

// ReadTOML decodes into an adapter-owned schema without inspecting arbitrary keys.
func ReadTOML(ctx context.Context, d Dependencies, path string, dst any) error {
	data, err := ReadFile(ctx, d, path)
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(data, dst); err != nil {
		return errors.New("invalid credential TOML")
	}
	return nil
}

// ReadYAML decodes into an adapter-owned schema without inspecting arbitrary keys.
func ReadYAML(ctx context.Context, d Dependencies, path string, dst any) error {
	data, err := ReadFile(ctx, d, path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, dst); err != nil {
		return errors.New("invalid credential YAML")
	}
	return nil
}

// FindUpward returns regular files nearest-directory first, in names order
// within each directory. The starting path must be an absolute directory.
// It stops at the filesystem root and ignores missing/non-regular candidates.
func FindUpward(ctx context.Context, d Dependencies, start string, names ...string) ([]string, error) {
	if !filepath.IsAbs(start) {
		return nil, errors.New("project search requires an absolute directory")
	}
	for _, name := range names {
		if !filepath.IsLocal(name) || filepath.Clean(name) == "." {
			return nil, errors.New("project search requires local relative filenames")
		}
	}
	var found []string
	for dir := filepath.Clean(start); ; dir = filepath.Dir(dir) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, name := range names {
			path := filepath.Join(dir, name)
			if info, err := d.lstat(path); err == nil && info.Mode().IsRegular() {
				found = append(found, path)
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return found, nil
}
