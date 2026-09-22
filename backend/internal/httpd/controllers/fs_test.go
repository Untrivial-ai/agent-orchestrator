package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	fsbrowsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/fsbrowser"
)

func newFSRig(t *testing.T) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	(&FSController{Svc: fsbrowsersvc.New()}).Register(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func listDirs(t *testing.T, srv *httptest.Server, path string) (int, ListDirsResponse, envelope.APIError) {
	t.Helper()
	q := ""
	if path != "" {
		q = "?path=" + url.QueryEscape(path)
	}
	resp, err := http.Get(srv.URL + "/fs/dirs" + q)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body ListDirsResponse
	var apiErr envelope.APIError
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
	} else if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body, apiErr
}

// padName zero-pads so the cap test's directory names sort deterministically.
func padName(i int) string { return fmt.Sprintf("%04d", i) }

func TestListDirsShowsDirectoriesOnlyWithGitDetection(t *testing.T) {
	home := t.TempDir()
	// A plain dir, a git repo (.git directory), a worktree checkout (.git FILE),
	// a dotdir that must be skipped, and a plain file that must not appear.
	for _, d := range []string{"plain", "repo/.git", "worktree", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Worktrees carry `.git` as a FILE ("gitdir: ..."), not a directory. Both count.
	if err := os.WriteFile(filepath.Join(home, "worktree", ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "loose.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	status, body, _ := listDirs(t, newFSRig(t), home)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	got := map[string]bool{}
	for _, e := range body.Entries {
		got[e.Name] = e.GitRepo
		if e.Path != filepath.Join(home, e.Name) {
			t.Errorf("entry %q path = %q, want %q", e.Name, e.Path, filepath.Join(home, e.Name))
		}
	}
	want := map[string]bool{"plain": false, "repo": true, "worktree": true}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want exactly %v (no dotdirs, no files)", got, want)
	}
	for name, gitRepo := range want {
		if got[name] != gitRepo {
			t.Errorf("entry %q gitRepo = %v, want %v", name, got[name], gitRepo)
		}
	}
}

func TestListDirsDefaultsToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	status, body, _ := listDirs(t, newFSRig(t), "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body.Path != filepath.Clean(home) {
		t.Errorf("path = %q, want home %q", body.Path, home)
	}
	if body.Parent != filepath.Dir(home) {
		t.Errorf("parent = %q, want %q", body.Parent, filepath.Dir(home))
	}
}

func TestListDirsRejectsRelativeAndMissingPaths(t *testing.T) {
	home := t.TempDir()
	srv := newFSRig(t)
	// A file is not browsable.
	f := filepath.Join(home, "f.txt")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		path       string
		wantStatus int
		wantCode   string
	}{
		{name: "relative", path: "relative/path", wantStatus: http.StatusBadRequest, wantCode: "FS_PATH_NOT_ABSOLUTE"},
		{name: "missing", path: filepath.Join(home, "nope"), wantStatus: http.StatusNotFound, wantCode: "FS_NOT_FOUND"},
		{name: "file", path: f, wantStatus: http.StatusBadRequest, wantCode: "FS_NOT_A_DIRECTORY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, apiErr := listDirs(t, srv, tc.path)
			if status != tc.wantStatus || apiErr.Code != tc.wantCode {
				t.Fatalf("status/code = %d/%q, want %d/%q", status, apiErr.Code, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

func TestListDirsRejectsPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce Unix directory mode bits")
	}
	dir := filepath.Join(t.TempDir(), "forbidden")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	status, _, apiErr := listDirs(t, newFSRig(t), dir)
	if status != http.StatusForbidden || apiErr.Code != "FS_FORBIDDEN" {
		t.Fatalf("status/code = %d/%q, want 403/FS_FORBIDDEN", status, apiErr.Code)
	}
}

func TestListDirsCapsEntries(t *testing.T) {
	const maxDirEntries = 500
	home := t.TempDir()
	for i := 0; i < maxDirEntries+10; i++ {
		if err := os.Mkdir(filepath.Join(home, "d"+padName(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	status, body, _ := listDirs(t, newFSRig(t), home)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(body.Entries) != maxDirEntries || !body.Truncated {
		t.Errorf("entries = %d truncated = %v, want %d/true", len(body.Entries), body.Truncated, maxDirEntries)
	}
}
