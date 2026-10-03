package preview

import (
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const previewHostLabel = "ao-preview"

// previewArtifactHostLabel is the reserved first label for an artifact
// preview origin. Using a distinct host label — rather than a path prefix
// shared with workspace asset paths — gives an artifact link an unambiguous
// source identity: scope is decided by which origin the browser is talking
// to, not by a string marker that a real workspace path could also contain.
const previewArtifactHostLabel = "ao-preview-artifact"

// ErrPreviewHostUnsupported indicates that a session ID cannot be represented
// by a standards-compliant localhost hostname.
var ErrPreviewHostUnsupported = errors.New("session ID is too long for a preview hostname")

var entryCandidates = []string{"index.html", "public/index.html", "dist/index.html", "build/index.html"}

// previewableExts are the file extensions the browser panel can render: HTML
// verbatim and Markdown converted to HTML by the preview/files route.
var previewableExts = map[string]struct{}{
	".html":     {},
	".htm":      {},
	".md":       {},
	".markdown": {},
}

// maxPreviewWalkFiles bounds the most-recent fallback scan so a pathological
// workspace cannot stall the preview poller.
const maxPreviewWalkFiles = 5000

// Entry is a workspace-local static frontend entrypoint.
type Entry struct {
	Path    string
	AbsPath string
	ModTime time.Time
	Size    int64
}

// DiscoverEntry returns the entry the browser panel should preview for a
// workspace. A conventional index.html (or its public/dist/build variants)
// always wins; when none exists it falls back to the most-recently-modified
// previewable file (.html/.htm/.md/.markdown) anywhere in the workspace, so a
// freshly generated report or document shows up automatically.
func DiscoverEntry(workspacePath string) (Entry, bool) {
	if entry, ok := DiscoverWebEntrypoint(workspacePath); ok {
		return entry, true
	}
	return mostRecentPreviewable(workspacePath)
}

// DiscoverWebEntrypoint returns a conventional static-frontend entrypoint
// (index.html or its public/dist/build variants) if one exists in the
// workspace. Unlike DiscoverEntry it does NOT fall back to the most-recent
// previewable document, so it is the right choice when auto-previewing a
// brand-new session the user has never explicitly previewed: surfacing an
// arbitrary repo README or generated report as the initial browser target is
// noise, not the intended preview. See issue #2859.
func DiscoverWebEntrypoint(workspacePath string) (Entry, bool) {
	if strings.TrimSpace(workspacePath) == "" {
		return Entry{}, false
	}
	for _, candidate := range entryCandidates {
		if entry, ok := EntryAtPath(workspacePath, candidate); ok {
			return entry, true
		}
	}
	return Entry{}, false
}

// mostRecentPreviewable walks the workspace and returns the newest previewable
// file. Ties (equal mod times) break on the slash path so the result is
// deterministic. Hidden directories and node_modules are skipped, and the scan
// is bounded by maxPreviewWalkFiles.
func mostRecentPreviewable(workspacePath string) (Entry, bool) {
	root, err := filepath.Abs(workspacePath)
	if err != nil {
		return Entry{}, false
	}
	var best Entry
	found := false
	seen := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			//nolint:nilerr // skip unreadable entries rather than aborting the whole scan
			return nil
		}
		if d.IsDir() {
			if p != root && skipPreviewDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := previewableExts[strings.ToLower(filepath.Ext(d.Name()))]; !ok {
			return nil
		}
		seen++
		if seen > maxPreviewWalkFiles {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			//nolint:nilerr // skip this file, keep scanning the rest of the workspace
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		entry, ok := EntryAtPath(root, relSlash)
		if !ok {
			return nil
		}
		if !found || newerPreviewable(entry, relSlash, best) {
			best = entry
			found = true
		}
		return nil
	})
	return best, found
}

func newerPreviewable(entry Entry, relSlash string, best Entry) bool {
	mod := entry.ModTime
	if mod.After(best.ModTime) {
		return true
	}
	if mod.Equal(best.ModTime) {
		return relSlash < best.Path
	}
	return false
}

func skipPreviewDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

// IsMarkdownPath reports whether p names a Markdown file the preview/files
// route should render to HTML rather than serve verbatim.
func IsMarkdownPath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// ConfinedPath maps an asset path into workspacePath and rejects paths that
// escape the workspace root.
func ConfinedPath(workspacePath, assetPath string) (string, bool) {
	root, err := filepath.Abs(workspacePath)
	if err != nil || root == "" {
		return "", false
	}
	clean, ok := CleanWorkspacePath(assetPath)
	if !ok {
		if path.Clean("/"+filepath.ToSlash(assetPath)) != "/" {
			return "", false
		}
		clean = "index.html"
	}
	file := filepath.Join(root, filepath.FromSlash(clean))
	absFile, err := filepath.Abs(file)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, absFile)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", false
	}
	return absFile, true
}

// FileURL builds an isolated localhost origin for a workspace-local entry.
// Mounting the entry directory at the origin root makes both relative and
// root-relative browser requests resolve inside the preview instead of falling
// through to the daemon's API origin.
func FileURL(baseURL string, id domain.SessionID, entry string) (string, error) {
	return labeledFileURL(baseURL, previewHostLabel, id, entry)
}

// ArtifactFileURL builds an isolated localhost origin for a session's
// artifact-directory entry, on a distinct host from FileURL. The artifact
// entry path is never prefixed with a scope marker: the host alone says
// "serve from ArtifactDir," so a real workspace file can never collide with
// an artifact link the way a shared path-prefix marker could.
func ArtifactFileURL(baseURL string, id domain.SessionID, entry string) (string, error) {
	return labeledFileURL(baseURL, previewArtifactHostLabel, id, entry)
}

func labeledFileURL(baseURL, label string, id domain.SessionID, entry string) (string, error) {
	u := normalizedBaseURL(baseURL)
	host, err := previewHost(u, label, id)
	if err != nil {
		return "", err
	}
	u.Host = host
	// URL.String escapes Path exactly once. Supplying an already-escaped path
	// here would turn spaces into %2520 and make otherwise valid workspace files
	// impossible to resolve.
	u.Path = path.Clean("/" + entry)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// SessionIDFromHost decodes the session identity carried by a FileURL host.
// The labels use unpadded base32 so arbitrary session IDs remain DNS-safe.
func SessionIDFromHost(rawHost string) (domain.SessionID, bool) {
	return sessionIDFromLabeledHost(rawHost, previewHostLabel)
}

// SessionIDFromArtifactHost decodes the session identity carried by an
// ArtifactFileURL host.
func SessionIDFromArtifactHost(rawHost string) (domain.SessionID, bool) {
	return sessionIDFromLabeledHost(rawHost, previewArtifactHostLabel)
}

func sessionIDFromLabeledHost(rawHost, label string) (domain.SessionID, bool) {
	host := rawHost
	if parsedHost, _, err := net.SplitHostPort(rawHost); err == nil {
		host = parsedHost
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || len(host) > 253 {
		return "", false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 3 || labels[0] != label || labels[len(labels)-1] != "localhost" {
		return "", false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return "", false
		}
	}
	encoded := strings.Join(labels[1:len(labels)-1], "")
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(encoded))
	if err != nil || len(decoded) == 0 || !utf8.Valid(decoded) {
		return "", false
	}
	return domain.SessionID(decoded), true
}

func previewHost(u url.URL, label string, id domain.SessionID) (string, error) {
	if id == "" {
		return "", fmt.Errorf("%w: empty session ID", ErrPreviewHostUnsupported)
	}
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(id)))
	labels := []string{label}
	const maxChunk = 50
	for encoded != "" {
		n := min(len(encoded), maxChunk)
		labels = append(labels, encoded[:n])
		encoded = encoded[n:]
	}
	labels = append(labels, "localhost")
	host := strings.Join(labels, ".")
	if len(host) > 253 {
		return "", fmt.Errorf("%w: encoded hostname is %d characters", ErrPreviewHostUnsupported, len(host))
	}
	if port := u.Port(); port != "" {
		return host + ":" + port, nil
	}
	return host, nil
}

func normalizedBaseURL(raw string) url.URL {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		raw = "http://127.0.0.1:3001"
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return url.URL{Scheme: "http", Host: raw}
	}
	return *u
}
