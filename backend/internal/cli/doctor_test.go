package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDoctorChecksGitVersion(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/bin/git" || len(args) != 1 || args[0] != "--version" {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		return []byte("git version 2.43.0\n"), nil
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "git")
	if check.Level != doctorPass || !strings.Contains(check.Message, "2.43.0") || !strings.Contains(check.Message, "supports worktrees") {
		t.Fatalf("git check = %+v, want PASS with version", check)
	}
}

func TestDoctorWarnsOnUnsupportedGitVersion(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.24.9\n"), nil
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "git")
	if check.Level != doctorWarn || !strings.Contains(check.Message, ">= 2.25.0") {
		t.Fatalf("git check = %+v, want WARN with minimum version", check)
	}
}

func TestDoctorFailsWhenGitMissing(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{}, nil)

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "git")
	if check.Level != doctorFail {
		t.Fatalf("git check = %+v, want FAIL", check)
	}
}

func TestDoctorChecksTmuxVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ao doctor emits a conpty check on Windows, not tmux")
	}
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "tmux": "/bin/tmux"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "/bin/git":
			return []byte("git version 2.43.0\n"), nil
		case "/bin/tmux":
			if len(args) != 1 || args[0] != "-V" {
				t.Fatalf("unexpected tmux command: %s %v", name, args)
			}
			return []byte("tmux 3.3a\n"), nil
		default:
			t.Fatalf("unexpected command: %s %v", name, args)
			return nil, nil
		}
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "tmux")
	if check.Level != doctorPass || !strings.Contains(check.Message, "3.3a") {
		t.Fatalf("tmux check = %+v, want PASS with version", check)
	}
}

// TestDoctorChecksTmuxVersionFailsOnError covers the case where tmux is found
// but the version command fails.
func TestDoctorChecksTmuxVersionFailsOnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ao doctor emits a conpty check on Windows, not tmux")
	}
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "tmux": "/bin/tmux"}, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "/bin/git" {
			return []byte("git version 2.43.0\n"), nil
		}
		return nil, errors.New("exec: tmux: not found")
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "tmux")
	if check.Level != doctorFail {
		t.Fatalf("tmux check = %+v, want FAIL on version error", check)
	}
}

func TestDoctorWarnsWhenTmuxMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ao doctor emits a conpty check on Windows, not tmux")
	}
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "tmux")
	if check.Level != doctorWarn {
		t.Fatalf("tmux check = %+v, want WARN", check)
	}
}

func TestDoctorChecksHarnessVersions(t *testing.T) {
	setConfigEnv(t)
	cmdPath := map[string]string{
		"git":    "/bin/git",
		"claude": "/bin/claude",
		"codex":  "/bin/codex",
		"muse":   "/bin/muse",
	}
	c := doctorContext(t, cmdPath, func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "/bin/git":
			return []byte("git version 2.43.0\n"), nil
		case "/bin/claude", "/bin/codex", "/bin/muse":
			if len(args) == 1 && args[0] == "--version" {
				if name == "/bin/muse" {
					return []byte("Muse Code 0.1.0 (0.1.0-R708.1)\n"), nil
				}
				return []byte(strings.TrimPrefix(name, "/bin/") + " 1.2.3\n"), nil
			}
			// The codex launch-flag canary probes the same binary.
			if name == "/bin/codex" && len(args) > 0 && (args[0] == "--dangerously-bypass-hook-trust" || args[0] == "features") {
				return []byte("ok\n"), nil
			}
			t.Fatalf("unexpected harness command: %s %v", name, args)
			return nil, nil
		default:
			t.Fatalf("unexpected command: %s %v", name, args)
			return nil, nil
		}
	})

	checks := c.runDoctor(context.Background())
	for _, name := range []string{"claude-code", "codex", "muse"} {
		check := findDoctorCheck(t, checks, name)
		if check.Level != doctorPass || !strings.Contains(check.Message, "resolves to") {
			t.Fatalf("%s check = %+v, want PASS with path/version", name, check)
		}
	}
}

func TestDoctorRejectsUnrelatedMuseBinary(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "muse": "/bin/muse"}, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "/bin/git" {
			return []byte("git version 2.43.0\n"), nil
		}
		return []byte("unrelated muse 1.0\n"), nil
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "muse")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "does not identify the expected CLI") {
		t.Fatalf("muse check = %+v, want WARN for unrelated binary", check)
	}
}

func TestDoctorWarnsWhenHarnessMissing(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "codex")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "not found in PATH") {
		t.Fatalf("codex check = %+v, want WARN missing binary", check)
	}
}

func TestDoctorWarnsWhenHarnessVersionFails(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "codex": "/bin/codex"}, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "/bin/git" {
			return []byte("git version 2.43.0\n"), nil
		}
		return nil, errors.New("boom")
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "codex")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "failed") {
		t.Fatalf("codex check = %+v, want WARN version failure", check)
	}
}

func TestDoctorChecksGitHubTokenFromEnv(t *testing.T) {
	setConfigEnv(t)
	srv := githubDoctorServer(t, http.StatusOK, `{"login":"octocat"}`, "repo, read:org")
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITHUB_TOKEN", "env-token")
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitHubRESTBase = srv.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "github-token")
	if check.Level != doctorPass || !strings.Contains(check.Message, "AO_GITHUB_TOKEN") || !strings.Contains(check.Message, "repo, read:org") {
		t.Fatalf("github-token check = %+v, want PASS with source and scopes", check)
	}
}

func TestDoctorChecksGitHubTokenFromGHCLI(t *testing.T) {
	setConfigEnv(t)
	srv := githubDoctorServer(t, http.StatusOK, `{"login":"octocat"}`, "")
	c := doctorContext(t, map[string]string{"git": "/bin/git", "gh": "/bin/gh"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/gh" {
			if len(args) != 2 || args[0] != "auth" || args[1] != "token" {
				t.Fatalf("unexpected gh command: %s %v", name, args)
			}
			return []byte("gh-token\n"), nil
		}
		return []byte("git version 2.43.0\n"), nil
	})
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitHubRESTBase = srv.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "github-token")
	if check.Level != doctorPass || !strings.Contains(check.Message, "gh token valid") {
		t.Fatalf("github-token check = %+v, want PASS from gh", check)
	}
}

func TestDoctorWarnsWhenGitHubTokenMissing(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "github-token")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "no GitHub token found") {
		t.Fatalf("github-token check = %+v, want WARN missing token", check)
	}
}

func TestDoctorFailsExpiredGitHubToken(t *testing.T) {
	setConfigEnv(t)
	srv := githubDoctorServer(t, http.StatusUnauthorized, `{"message":"Bad credentials"}`, "")
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("GITHUB_TOKEN", "expired-token")
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitHubRESTBase = srv.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "github-token")
	if check.Level != doctorFail || !strings.Contains(check.Message, "HTTP 401") {
		t.Fatalf("github-token check = %+v, want FAIL rejected token", check)
	}
}

func TestDoctorChecksGitLabTokenFromEnv(t *testing.T) {
	setConfigEnv(t)
	srv := gitlabDoctorServer(t, http.StatusOK, `{"username":"gitlab-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "env-token")
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitLabRESTBase = srv.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token")
	if check.Level != doctorPass || !strings.Contains(check.Message, "AO_GITLAB_TOKEN") || !strings.Contains(check.Message, "gitlab-user") {
		t.Fatalf("gitlab-token check = %+v, want PASS with source and username", check)
	}
}

func TestDoctorChecksGitLabTokenFromEnvGitLabToken(t *testing.T) {
	setConfigEnv(t)
	srv := gitlabDoctorServer(t, http.StatusOK, `{"username":"gitlab-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("GITLAB_TOKEN", "env-token-2")
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitLabRESTBase = srv.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token")
	if check.Level != doctorPass || !strings.Contains(check.Message, "GITLAB_TOKEN") {
		t.Fatalf("gitlab-token check = %+v, want PASS from GITLAB_TOKEN", check)
	}
}

func TestDoctorChecksGitLabTokenFromGLab(t *testing.T) {
	setConfigEnv(t)
	srv := gitlabDoctorServer(t, http.StatusOK, `{"username":"glab-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "glab": "/bin/glab"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/glab" {
			if len(args) != 3 || args[0] != "auth" || args[1] != "status" || args[2] != "--show-token" {
				t.Fatalf("unexpected glab command: %s %v", name, args)
			}
			return []byte("Hostname: gitlab.com\n✓ Token found: glpat-token123\n"), nil
		}
		return []byte("git version 2.43.0\n"), nil
	})
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitLabRESTBase = srv.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token")
	if check.Level != doctorPass || !strings.Contains(check.Message, "glab token valid") || !strings.Contains(check.Message, "glab-user") {
		t.Fatalf("gitlab-token check = %+v, want PASS from glab", check)
	}
}

func TestDoctorWarnsWhenGitLabTokenMissing(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "no GitLab token found") {
		t.Fatalf("gitlab-token check = %+v, want WARN missing token", check)
	}
}

func TestDoctorFailsExpiredGitLabToken(t *testing.T) {
	setConfigEnv(t)
	srv := gitlabDoctorServer(t, http.StatusUnauthorized, `{"message":"401 Unauthorized"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("GITLAB_TOKEN", "expired-token")
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitLabRESTBase = srv.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token")
	if check.Level != doctorFail || !strings.Contains(check.Message, "HTTP 401") {
		t.Fatalf("gitlab-token check = %+v, want FAIL rejected token", check)
	}
}

func gitlabDoctorServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user" {
			t.Fatalf("unexpected gitlab probe: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("PRIVATE-TOKEN"); got == "" {
			t.Fatalf("missing PRIVATE-TOKEN auth header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func TestDoctorJSONOutputIsDecodable(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitHubEnv(t)
	clearDoctorGitLabEnv(t)
	out, errOut, err := executeCLI(t, Deps{
		LookPath: func(name string) (string, error) {
			switch name {
			case "git":
				return "/bin/git", nil
			case "tmux":
				return "/bin/tmux", nil
			}
			return "", errors.New("missing")
		},
		CommandOutput: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "/bin/tmux" {
				return []byte("tmux 3.3a\n"), nil
			}
			return []byte("git version 2.43.0\n"), nil
		},
		ProcessAlive: func(int) bool { return false },
	}, "doctor", "--json")
	if err != nil {
		t.Fatalf("doctor --json failed: %v\nstderr=%s\nstdout=%s", err, errOut, out)
	}
	var got doctorReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode doctor json: %v\nout=%s", err, out)
	}
	if !got.OK || len(got.Checks) == 0 {
		t.Fatalf("doctor json = %#v, want ok with checks", got)
	}
	if findDoctorCheck(t, got.Checks, "git").Section != doctorSectionTools {
		t.Fatalf("git json check missing section: %#v", findDoctorCheck(t, got.Checks, "git"))
	}
}

func TestDoctorTextOutputIsGrouped(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitHubEnv(t)
	clearDoctorGitLabEnv(t)
	out, errOut, err := executeCLI(t, Deps{
		LookPath: func(name string) (string, error) {
			switch name {
			case "git":
				return "/bin/git", nil
			case "tmux":
				return "/bin/tmux", nil
			}
			return "", errors.New("missing")
		},
		CommandOutput: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "/bin/tmux" {
				return []byte("tmux 3.3a\n"), nil
			}
			return []byte("git version 2.43.0\n"), nil
		},
		ProcessAlive: func(int) bool { return false },
	}, "doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\nstderr=%s\nstdout=%s", err, errOut, out)
	}
	for _, want := range []string{"Core:\nPASS config:", "Tools:\nPASS git:", "Agent harnesses:\nWARN claude-code:", "WARN codex:", "WARN muse:", "GitHub:\nWARN github-token:", "GitLab:\nWARN gitlab-token:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func clearDoctorGitHubEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AO_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
}

func clearDoctorGitLabEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AO_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
}

// TestDoctorChecksAOBinaryIdentity covers the `ao-binary` check: workspace
// hooks invoke a bare `ao hooks <agent> <event>`, so doctor must surface when
// the `ao` on PATH is not the running binary (e.g. a legacy CLI without the
// hooks command shadowing the Go one).
func TestDoctorChecksAOBinaryIdentity(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "ao")
	other := filepath.Join(dir, "ao-legacy")
	for _, p := range []string{self, other} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test fixture must be executable-shaped
			t.Fatal(err)
		}
	}
	selfExe := func() (string, error) { return self, nil }

	cases := []struct {
		name       string
		executable func() (string, error)
		paths      map[string]string
		wantLevel  doctorLevel
		wantIn     string
	}{
		{"ao in PATH is this binary", selfExe, map[string]string{"ao": self}, doctorPass, "this binary"},
		{"ao in PATH is a different binary", selfExe, map[string]string{"ao": other}, doctorWarn, "not this binary"},
		{"ao missing from PATH", selfExe, map[string]string{}, doctorWarn, "not found in PATH"},
		{"running executable unresolvable", func() (string, error) { return "", errors.New("no exe") }, map[string]string{"ao": self}, doctorWarn, "could not resolve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := Deps{
				Executable: tc.executable,
				LookPath: func(name string) (string, error) {
					path, ok := tc.paths[name]
					if !ok || path == "" {
						return "", fmt.Errorf("%s missing", name)
					}
					return path, nil
				},
				ProcessAlive: func(int) bool { return false },
			}
			c := &commandContext{deps: deps.withDefaults()}
			check := c.checkAOBinary()
			if check.Level != tc.wantLevel || !strings.Contains(check.Message, tc.wantIn) {
				t.Fatalf("ao-binary check = %+v, want level %s with %q", check, tc.wantLevel, tc.wantIn)
			}
		})
	}
}

// TestDoctorIncludesAOBinaryCheck asserts runDoctor actually surfaces the
// ao-binary check, so the identity probe cannot silently fall out of the report.
func TestDoctorIncludesAOBinaryCheck(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})

	// doctorContext's LookPath has no "ao", so the check lands as a WARN.
	check := findDoctorCheck(t, c.runDoctor(context.Background()), "ao-binary")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "not found in PATH") {
		t.Fatalf("ao-binary check = %+v, want WARN for missing ao", check)
	}
}

func doctorContext(t *testing.T, paths map[string]string, commandOutput func(context.Context, string, ...string) ([]byte, error)) *commandContext {
	t.Helper()
	clearDoctorGitHubEnv(t)
	clearDoctorGitLabEnv(t)
	deps := Deps{
		LookPath: func(name string) (string, error) {
			path, ok := paths[name]
			if !ok || path == "" {
				return "", fmt.Errorf("%s missing", name)
			}
			return path, nil
		},
		ProcessAlive: func(int) bool { return false },
	}
	if commandOutput != nil {
		deps.CommandOutput = commandOutput
	}
	return &commandContext{deps: deps.withDefaults()}
}

func githubDoctorServer(t *testing.T, status int, body, scopes string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user" {
			t.Fatalf("unexpected github probe: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Fatalf("missing bearer auth header: %q", got)
		}
		if scopes != "" {
			w.Header().Set("X-OAuth-Scopes", scopes)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func findDoctorCheck(t *testing.T, checks []doctorCheck, name string) doctorCheck {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("doctor check %q not found in %+v", name, checks)
	return doctorCheck{}
}

func codexCanaryFake(t *testing.T, probeOutput string, probeErr error) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "/bin/git":
			return []byte("git version 2.43.0\n"), nil
		case name == "/bin/codex" && len(args) == 1 && args[0] == "--version":
			return []byte("codex-cli 0.136.0\n"), nil
		case name == "/bin/codex":
			return []byte(probeOutput), probeErr
		default:
			t.Fatalf("unexpected command: %s %v", name, args)
			return nil, nil
		}
	}
}

func TestDoctorCodexLaunchFlagsPass(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "codex": "/bin/codex"}, codexCanaryFake(t, "ok\n", nil))

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "codex-launch-flags")
	if check.Level != doctorPass || !strings.Contains(check.Message, "accepts") {
		t.Fatalf("canary = %+v, want PASS accepts", check)
	}
}

func TestDoctorCodexLaunchFlagsWarnOnRejectedFlag(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "codex": "/bin/codex"},
		codexCanaryFake(t, "error: unexpected argument '--dangerously-bypass-hook-trust' found\n", errors.New("exit status 2")))

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "codex-launch-flags")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "rejected AO's launch flags") {
		t.Fatalf("canary = %+v, want WARN rejected flags", check)
	}
}

func TestDoctorCodexLaunchFlagsWarnOnUnknownConfigField(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "codex": "/bin/codex"},
		codexCanaryFake(t, "unknown configuration field `hooks` in -c/--config override\n", nil))

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "codex-launch-flags")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "no longer recognizes") {
		t.Fatalf("canary = %+v, want WARN unknown config field", check)
	}
}

func TestDoctorCodexLaunchFlagsSkippedWithoutCodex(t *testing.T) {
	setConfigEnv(t)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "codex-launch-flags")
	if check.Level != doctorPass || !strings.Contains(check.Message, "skipped") {
		t.Fatalf("canary = %+v, want skipped PASS", check)
	}
}

func TestDoctorHooksLogStates(t *testing.T) {
	gitOnly := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	}

	t.Run("missing log passes", func(t *testing.T) {
		setConfigEnv(t)
		c := doctorContext(t, map[string]string{"git": "/bin/git"}, gitOnly)
		check := findDoctorCheck(t, c.runDoctor(context.Background()), "hooks-log")
		if check.Level != doctorPass || !strings.Contains(check.Message, "no hook delivery failures") {
			t.Fatalf("hooks-log = %+v, want PASS no failures", check)
		}
	})

	t.Run("recent failures warn", func(t *testing.T) {
		cfg := setConfigEnv(t)
		writeHooksLogLines(t, cfg.dataDir,
			time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339)+" session=old ao hooks codex stop: stale",
			time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)+" session=mer-1 ao hooks codex stop: connection refused",
		)
		c := doctorContext(t, map[string]string{"git": "/bin/git"}, gitOnly)
		check := findDoctorCheck(t, c.runDoctor(context.Background()), "hooks-log")
		if check.Level != doctorWarn || !strings.Contains(check.Message, "1 hook delivery failure") || !strings.Contains(check.Message, "connection refused") {
			t.Fatalf("hooks-log = %+v, want WARN with recent count and latest line", check)
		}
	})

	t.Run("only stale failures pass", func(t *testing.T) {
		cfg := setConfigEnv(t)
		writeHooksLogLines(t, cfg.dataDir,
			time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339)+" session=old ao hooks codex stop: stale",
		)
		c := doctorContext(t, map[string]string{"git": "/bin/git"}, gitOnly)
		check := findDoctorCheck(t, c.runDoctor(context.Background()), "hooks-log")
		if check.Level != doctorPass || !strings.Contains(check.Message, "last 24h") {
			t.Fatalf("hooks-log = %+v, want PASS stale-only", check)
		}
	})
}

func writeHooksLogLines(t *testing.T, dataDir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dataDir, hooksLogName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
