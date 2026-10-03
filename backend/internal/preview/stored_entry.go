package preview

import (
	"net"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const artifactEntryPrefix = "__ao_artifacts__"

// StoredEntryScope identifies which session-owned root a stored preview target
// is resolved against.
type StoredEntryScope string

const (
	// StoredEntryScopeWorkspace resolves the entry under the session workspace.
	StoredEntryScopeWorkspace StoredEntryScope = "workspace"
	// StoredEntryScopeArtifact resolves the entry under the session artifact directory.
	StoredEntryScopeArtifact StoredEntryScope = "artifact"
)

// StoredEntry is a normalized persisted preview entry plus the root it belongs
// to.
type StoredEntry struct {
	Scope StoredEntryScope
	Path  string
}

// ArtifactEntryPath encodes an artifact-relative path as a stored preview
// entry.
func ArtifactEntryPath(rel string) (string, bool) {
	clean, ok := CleanWorkspacePath(rel)
	if !ok {
		return "", false
	}
	return path.Join(artifactEntryPrefix, clean), true
}

// ArtifactEntryRelative decodes an artifact-relative path from a stored preview
// entry.
func ArtifactEntryRelative(raw string) (string, bool) {
	clean, ok := CleanWorkspacePath(raw)
	if !ok || clean == artifactEntryPrefix {
		return "", false
	}
	prefix := artifactEntryPrefix + "/"
	if !strings.HasPrefix(clean, prefix) {
		return "", false
	}
	return strings.TrimPrefix(clean, prefix), true
}

// StoredEntryFromPreview extracts a stored preview entry and its source scope.
func StoredEntryFromPreview(raw string, id domain.SessionID) (StoredEntry, bool) {
	entry, ok := storedPreviewPath(raw, id)
	if !ok {
		return StoredEntry{}, false
	}
	if rel, artifact := ArtifactEntryRelative(entry); artifact {
		return StoredEntry{Scope: StoredEntryScopeArtifact, Path: rel}, true
	}
	return StoredEntry{Scope: StoredEntryScopeWorkspace, Path: entry}, true
}

// StoredWorkspaceEntry extracts a workspace-relative entry from every preview
// format persisted by released versions: isolated-origin URLs, legacy API
// URLs, and plain relative paths.
func StoredWorkspaceEntry(raw string, id domain.SessionID) (string, bool) {
	entry, ok := StoredEntryFromPreview(raw, id)
	if !ok || entry.Scope != StoredEntryScopeWorkspace {
		return "", false
	}
	return entry.Path, true
}

func storedPreviewPath(raw string, id domain.SessionID) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err == nil {
		if originID, ok := SessionIDFromHost(parsed.Host); ok {
			if originID != id {
				return "", false
			}
			entry, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
			if err != nil {
				return "", false
			}
			return CleanWorkspacePath(entry)
		}

		prefix := "/api/v1/sessions/" + url.PathEscape(string(id)) + "/preview/files/"
		if isLegacyPreviewHost(parsed.Hostname()) && strings.HasPrefix(parsed.EscapedPath(), prefix) {
			entry, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), prefix))
			if err != nil {
				return "", false
			}
			return CleanWorkspacePath(entry)
		}
	}

	if strings.Contains(raw, "://") || filepath.IsAbs(raw) || isWindowsAbsolute(raw) || strings.Contains(raw, ":") {
		return "", false
	}
	return CleanWorkspacePath(raw)
}

func isLegacyPreviewHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isWindowsAbsolute(raw string) bool {
	return len(raw) >= 3 && ((raw[0] >= 'a' && raw[0] <= 'z') || (raw[0] >= 'A' && raw[0] <= 'Z')) && raw[1] == ':' && (raw[2] == '\\' || raw[2] == '/')
}
