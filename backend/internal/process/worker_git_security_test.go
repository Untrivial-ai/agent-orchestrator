package process

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWorkerLocalGitWorksButAuthenticatedPushIsDenied(t *testing.T) {
	root := t.TempDir()
	remoteRoot := filepath.Join(root, "remotes")
	remote := filepath.Join(remoteRoot, "repo.git")
	repo := filepath.Join(root, "work")
	runHostGit(t, root, "init", "--bare", remote)
	runHostGit(t, remote, "config", "http.receivepack", "true")
	backend := filepath.Join(strings.TrimSpace(runHostGit(t, root, "--exec-path")), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		backend += ".exe"
	}
	var authorizedRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("writer:host-secret"))
		if r.Header.Get("Authorization") != want {
			w.Header().Set("WWW-Authenticate", `Basic realm="AO test remote"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authorizedRequests.Add(1)
		serveGitHTTPBackend(t, w, r, backend, remoteRoot)
	}))
	defer server.Close()

	runHostGit(t, root, "init", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("secure\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerEnv := WorkerEnvironment(os.Environ(), map[string]string{
		"AO_SESSION_ID": "worker-security", "AO_DATA_DIR": filepath.Join(root, "ao-data"),
		"GH_TOKEN": "must-not-leak", "GITHUB_TOKEN": "must-not-leak", "SSH_AUTH_SOCK": "host-agent",
	})
	runWorkerGit(t, repo, workerEnv, true, "status", "--short")
	runWorkerGit(t, repo, workerEnv, true, "diff", "--", "file.txt")
	runWorkerGit(t, repo, workerEnv, true, "add", "file.txt")
	runWorkerGit(t, repo, workerEnv, true, "commit", "-m", "worker local commit")
	runWorkerGit(t, repo, workerEnv, true, "remote", "add", "origin", server.URL+"/repo.git")
	out := runWorkerGit(t, repo, workerEnv, false, "push", "-u", "origin", "main")
	if !strings.Contains(strings.ToLower(out), "authentication") && !strings.Contains(strings.ToLower(out), "could not read username") {
		t.Fatalf("push failed for unexpected reason:\n%s", out)
	}
	if authorizedRequests.Load() != 0 {
		t.Fatal("worker reached authenticated receive-pack")
	}

	// The same valid smart-HTTP repository accepts the exact push with explicit
	// host-side credentials, proving the worker failure was authorization, not a
	// missing or invalid remote.
	authURL := strings.Replace(server.URL, "http://", "http://writer:host-secret@", 1) + "/repo.git"
	runHostGit(t, repo, "push", authURL, "HEAD:refs/heads/main")
	if authorizedRequests.Load() == 0 {
		t.Fatal("authenticated control push never reached git-http-backend")
	}
}

func TestWorkerGitCredentialFillCannotResolveHostCredential(t *testing.T) {
	workerEnv := WorkerEnvironment(os.Environ(), map[string]string{
		"AO_SESSION_ID": "worker-credential-fill", "AO_DATA_DIR": filepath.Join(t.TempDir(), "ao-data"),
		"GH_TOKEN": "must-not-leak", "GITHUB_TOKEN": "must-not-leak", "SSH_AUTH_SOCK": "host-agent",
	})
	cmd := exec.Command("git", "credential", "fill")
	cmd.Env = workerEnv
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("git credential fill unexpectedly resolved a credential: %s", out)
	}
	if bytes.Contains(out, []byte("must-not-leak")) {
		t.Fatal("host SCM credential leaked through git credential fill")
	}
}

func runHostGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("host git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
func runWorkerGit(t *testing.T, dir string, env []string, wantSuccess bool, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("worker git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("worker git %s unexpectedly succeeded\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func serveGitHTTPBackend(t *testing.T, w http.ResponseWriter, r *http.Request, backend, projectRoot string) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
		w.WriteHeader(500)
		return
	}
	cmd := exec.Command(backend)
	cmd.Env = append(os.Environ(),
		"GIT_PROJECT_ROOT="+projectRoot, "GIT_HTTP_EXPORT_ALL=1", "REQUEST_METHOD="+r.Method,
		"PATH_INFO="+r.URL.Path, "QUERY_STRING="+r.URL.RawQuery, "CONTENT_TYPE="+r.Header.Get("Content-Type"),
		"CONTENT_LENGTH="+strconv.Itoa(len(body)), "REMOTE_USER=writer",
	)
	cmd.Stdin = bytes.NewReader(body)
	out, err := cmd.Output()
	if err != nil {
		t.Errorf("git-http-backend: %v", err)
		w.WriteHeader(500)
		return
	}
	reader := bufio.NewReader(bytes.NewReader(out))
	status := http.StatusOK
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Error(readErr)
			w.WriteHeader(500)
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(name, "Status") {
			if code, parseErr := strconv.Atoi(strings.Fields(value)[0]); parseErr == nil {
				status = code
			}
		} else {
			w.Header().Add(name, strings.TrimSpace(value))
		}
	}
	w.WriteHeader(status)
	if _, err := io.Copy(w, reader); err != nil {
		t.Error(fmt.Errorf("copy git response: %w", err))
	}
}
