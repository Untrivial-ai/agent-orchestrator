package preview

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func writeEntryFile(t *testing.T, path, contents string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if !mod.IsZero() {
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
}

func TestDiscoverEntryPrefersIndexOverNewerFile(t *testing.T) {
	ws := t.TempDir()
	base := time.Now()
	writeEntryFile(t, filepath.Join(ws, "index.html"), "<main>app</main>", base)
	// A newer report must not win against the conventional index.html anchor.
	writeEntryFile(t, filepath.Join(ws, "report.html"), "<main>report</main>", base.Add(time.Hour))

	entry, ok := DiscoverEntry(ws)
	if !ok {
		t.Fatal("DiscoverEntry: ok=false, want entry")
	}
	if entry.Path != "index.html" {
		t.Fatalf("entry.Path = %q, want index.html", entry.Path)
	}
}

func TestDiscoverEntryFallsBackToMostRecentPreviewable(t *testing.T) {
	ws := t.TempDir()
	base := time.Now()
	writeEntryFile(t, filepath.Join(ws, "old.html"), "<main>old</main>", base)
	writeEntryFile(t, filepath.Join(ws, "docs", "notes.md"), "# notes", base.Add(30*time.Minute))
	writeEntryFile(t, filepath.Join(ws, "fresh.html"), "<main>fresh</main>", base.Add(time.Hour))

	entry, ok := DiscoverEntry(ws)
	if !ok {
		t.Fatal("DiscoverEntry: ok=false, want fallback entry")
	}
	if entry.Path != "fresh.html" {
		t.Fatalf("entry.Path = %q, want fresh.html", entry.Path)
	}
}

func TestDiscoverEntryFallsBackToMarkdown(t *testing.T) {
	ws := t.TempDir()
	writeEntryFile(t, filepath.Join(ws, "REPORT.md"), "# report", time.Now())

	entry, ok := DiscoverEntry(ws)
	if !ok {
		t.Fatal("DiscoverEntry: ok=false, want markdown fallback")
	}
	if entry.Path != "REPORT.md" {
		t.Fatalf("entry.Path = %q, want REPORT.md", entry.Path)
	}
}

func TestDiscoverEntrySkipsHiddenAndNodeModules(t *testing.T) {
	ws := t.TempDir()
	base := time.Now()
	// Newest files live in skipped dirs; the visible one must win.
	writeEntryFile(t, filepath.Join(ws, "node_modules", "pkg", "index.html"), "x", base.Add(time.Hour))
	writeEntryFile(t, filepath.Join(ws, ".cache", "cached.html"), "x", base.Add(2*time.Hour))
	writeEntryFile(t, filepath.Join(ws, "visible.html"), "ok", base)

	entry, ok := DiscoverEntry(ws)
	if !ok {
		t.Fatal("DiscoverEntry: ok=false, want visible entry")
	}
	if entry.Path != "visible.html" {
		t.Fatalf("entry.Path = %q, want visible.html", entry.Path)
	}
}

func TestDiscoverEntryTieBreaksOnPath(t *testing.T) {
	ws := t.TempDir()
	mod := time.Now()
	writeEntryFile(t, filepath.Join(ws, "b.html"), "b", mod)
	writeEntryFile(t, filepath.Join(ws, "a.html"), "a", mod)

	entry, ok := DiscoverEntry(ws)
	if !ok {
		t.Fatal("DiscoverEntry: ok=false, want tie-break entry")
	}
	if entry.Path != "a.html" {
		t.Fatalf("entry.Path = %q, want a.html (lexical tie-break)", entry.Path)
	}
}

func TestDiscoverEntryEmptyWorkspace(t *testing.T) {
	if _, ok := DiscoverEntry(t.TempDir()); ok {
		t.Fatal("DiscoverEntry: ok=true for empty workspace, want false")
	}
}

func TestCleanWorkspacePathRejectsTraversal(t *testing.T) {
	for _, raw := range []string{"../secret.html", "assets/../../secret.html", `assets\..\..\secret.html`} {
		if got, ok := CleanWorkspacePath(raw); ok {
			t.Errorf("CleanWorkspacePath(%q) = %q, true; want false", raw, got)
		}
	}
}

func TestIsMarkdownPath(t *testing.T) {
	cases := map[string]bool{
		"a.md":       true,
		"A.MARKDOWN": true,
		"dir/x.md":   true,
		"index.html": false,
		"notes.txt":  false,
		"noext":      false,
	}
	for in, want := range cases {
		if got := IsMarkdownPath(in); got != want {
			t.Errorf("IsMarkdownPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFileURLUsesIsolatedLocalhostOrigin(t *testing.T) {
	id := domain.SessionID("ao-1")
	raw := mustFileURL(t, "http://127.0.0.1:3001", id, "dist/index.html")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse FileURL: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Port() != "3001" {
		t.Fatalf("FileURL = %q, want http on daemon port", raw)
	}
	if !strings.HasSuffix(parsed.Hostname(), ".localhost") {
		t.Fatalf("FileURL host = %q, want isolated .localhost origin", parsed.Hostname())
	}
	if parsed.Path != "/dist/index.html" {
		t.Fatalf("FileURL path = %q, want /dist/index.html", parsed.Path)
	}
	decoded, ok := SessionIDFromHost(parsed.Host)
	if !ok || decoded != id {
		t.Fatalf("SessionIDFromHost(%q) = %q, %v; want %q, true", parsed.Host, decoded, ok, id)
	}
}

func TestArtifactFileURLUsesADistinctHostFromFileURL(t *testing.T) {
	id := domain.SessionID("ao-1")
	workspaceURL := mustFileURL(t, "http://127.0.0.1:3001", id, "__ao_artifacts__/site/index.html")
	artifactURL, err := ArtifactFileURL("http://127.0.0.1:3001", id, "site/index.html")
	if err != nil {
		t.Fatalf("ArtifactFileURL: %v", err)
	}
	parsedWorkspace, err := url.Parse(workspaceURL)
	if err != nil {
		t.Fatalf("parse FileURL: %v", err)
	}
	parsedArtifact, err := url.Parse(artifactURL)
	if err != nil {
		t.Fatalf("parse ArtifactFileURL: %v", err)
	}
	// The artifact path carries no __ao_artifacts__ marker: scope lives in
	// the host, so the path can never collide with a real workspace path.
	if parsedArtifact.Path != "/site/index.html" {
		t.Fatalf("ArtifactFileURL path = %q, want /site/index.html", parsedArtifact.Path)
	}
	if parsedArtifact.Hostname() == parsedWorkspace.Hostname() {
		t.Fatalf("ArtifactFileURL host = %q, want distinct from FileURL host %q", parsedArtifact.Hostname(), parsedWorkspace.Hostname())
	}
	if !strings.HasPrefix(parsedArtifact.Hostname(), previewArtifactHostLabel+".") {
		t.Fatalf("ArtifactFileURL host = %q, want %q label", parsedArtifact.Hostname(), previewArtifactHostLabel)
	}

	// Each host decodes only under its own function: an artifact host is not
	// a workspace preview host and vice versa.
	if decoded, ok := SessionIDFromHost(parsedArtifact.Host); ok {
		t.Fatalf("SessionIDFromHost(artifact host) = %q, true; want false", decoded)
	}
	if decoded, ok := SessionIDFromArtifactHost(parsedWorkspace.Host); ok {
		t.Fatalf("SessionIDFromArtifactHost(workspace host) = %q, true; want false", decoded)
	}
	decoded, ok := SessionIDFromArtifactHost(parsedArtifact.Host)
	if !ok || decoded != id {
		t.Fatalf("SessionIDFromArtifactHost(%q) = %q, %v; want %q, true", parsedArtifact.Host, decoded, ok, id)
	}
}

func TestSessionIDFromHostSupportsLongUnicodeIDs(t *testing.T) {
	id := domain.SessionID(strings.Repeat("worker-", 12) + "雪")
	raw := mustFileURL(t, "http://localhost:4321", id, "index.html")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse FileURL: %v", err)
	}
	for _, label := range strings.Split(parsed.Hostname(), ".") {
		if len(label) > 63 {
			t.Fatalf("hostname label length = %d, want <= 63", len(label))
		}
	}
	decoded, ok := SessionIDFromHost(parsed.Host)
	if !ok || decoded != id {
		t.Fatalf("round trip = %q, %v; want %q, true", decoded, ok, id)
	}
}

func TestFileURLPreservesSpecialCharactersInEntryPath(t *testing.T) {
	raw := mustFileURL(t, "http://127.0.0.1:3001", "ao-1", "dist/my report #1.html")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse FileURL: %v", err)
	}
	if parsed.Path != "/dist/my report #1.html" {
		t.Fatalf("FileURL path = %q, want decoded workspace path", parsed.Path)
	}
	if strings.Contains(raw, "%2520") || strings.Contains(raw, "%2523") {
		t.Fatalf("FileURL = %q, path was double-escaped", raw)
	}
}

func TestFileURLPreservesLeadingAndTrailingSpacesInEntryPath(t *testing.T) {
	raw := mustFileURL(t, "http://127.0.0.1:3001", "ao-1", "dist/ report.html ")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse FileURL: %v", err)
	}
	if parsed.Path != "/dist/ report.html " {
		t.Fatalf("FileURL path = %q, want exact filename spaces preserved", parsed.Path)
	}
}

func TestFileURLRejectsHostnameOverDNSLimit(t *testing.T) {
	accepted := domain.SessionID(strings.Repeat("x", 142))
	raw := mustFileURL(t, "http://127.0.0.1:3001", accepted, "index.html")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse boundary FileURL: %v", err)
	}
	if len(parsed.Hostname()) != 253 {
		t.Fatalf("boundary hostname length = %d, want 253", len(parsed.Hostname()))
	}
	if decoded, ok := SessionIDFromHost(parsed.Host); !ok || decoded != accepted {
		t.Fatalf("boundary hostname round trip = %q, %v; want %q, true", decoded, ok, accepted)
	}

	_, err = FileURL("http://127.0.0.1:3001", domain.SessionID(strings.Repeat("x", 143)), "index.html")
	if !errors.Is(err, ErrPreviewHostUnsupported) {
		t.Fatalf("FileURL error = %v, want ErrPreviewHostUnsupported", err)
	}
}

func TestSessionIDFromHostRejectsHostnameOverDNSLimit(t *testing.T) {
	host := "ao-preview." + strings.Repeat("a.", 120) + "localhost:3001"
	if id, ok := SessionIDFromHost(host); ok {
		t.Fatalf("SessionIDFromHost(overlong) = %q, true; want false", id)
	}
}

func TestStoredWorkspaceEntryPreservesLegacyAndRelativeTargets(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{raw: "http://127.0.0.1:3001/api/v1/sessions/ao-1/preview/files/docs/report.html", want: "docs/report.html"},
		{raw: "docs/report.html", want: "docs/report.html"},
		{raw: " docs/report.html ", want: " docs/report.html "},
	} {
		got, ok := StoredWorkspaceEntry(tc.raw, "ao-1")
		if !ok || got != tc.want {
			t.Errorf("StoredWorkspaceEntry(%q) = %q, %v; want %q, true", tc.raw, got, ok, tc.want)
		}
	}
	if got, ok := StoredWorkspaceEntry("https://example.com/api/v1/sessions/ao-1/preview/files/docs/report.html", "ao-1"); ok {
		t.Fatalf("external lookalike URL = %q, true; want false", got)
	}
}

func mustFileURL(t *testing.T, baseURL string, id domain.SessionID, entry string) string {
	t.Helper()
	raw, err := FileURL(baseURL, id, entry)
	if err != nil {
		t.Fatalf("FileURL: %v", err)
	}
	return raw
}

func TestSessionIDFromHostRejectsOrdinaryHosts(t *testing.T) {
	for _, host := range []string{"127.0.0.1:3001", "localhost:3001", "ao-preview.invalid.localhost:3001", "example.com"} {
		if id, ok := SessionIDFromHost(host); ok {
			t.Errorf("SessionIDFromHost(%q) = %q, true; want false", host, id)
		}
	}
}
