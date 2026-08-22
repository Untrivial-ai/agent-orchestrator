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
	"slices"
	"strings"
	"sync"
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
			// The default probe scopes its lookup to gitlab.com so the token it
			// gets back is attributable to the instance it will be sent to.
			want := []string{"auth", "status", "--show-token", "--hostname", "gitlab.com"}
			if !slices.Equal(args, want) {
				t.Fatalf("glab command = %s %v, want %v", name, args, want)
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

// TestDoctorProbesSelfManagedGitLabHost covers the host-aware probe: with a
// self-managed host in AO_GITLAB_ALLOWED_HOSTS, doctor must validate the token
// against that host, not only against gitlab.com.
func TestDoctorProbesSelfManagedGitLabHost(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	srv, tokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"self-hosted-user"}`)
	dotCom, dotComTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "env-token")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "GitLab.Internal:8443")
	t.Setenv("AO_GITLAB_HOST_TOKENS", "gitlab.internal:8443=host-pat")
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(host string) string {
		if host != "gitlab.internal:8443" {
			t.Errorf("host REST base requested for %q, want the normalized allowlist host", host)
		}
		return srv.URL
	}

	checks := c.runDoctor(context.Background())
	check := findDoctorCheck(t, checks, "gitlab-token:gitlab.internal:8443")
	if check.Level != doctorPass || !strings.Contains(check.Message, "self-hosted-user") {
		t.Fatalf("gitlab-token check = %+v, want PASS for the self-managed host", check)
	}
	if got := tokens(); len(got) != 1 || got[0] != "host-pat" {
		t.Fatalf("self-managed probe tokens = %v, want the host's own token once", got)
	}
	// The host has its own credential, so the global default is attributable to
	// gitlab.com and is validated there.
	if got := dotComTokens(); len(got) != 1 || got[0] != "env-token" {
		t.Fatalf("gitlab.com probe tokens = %v, want the global default once", got)
	}
}

// TestDoctorNeverSendsTheGlobalDefaultToASelfManagedHost covers the other half
// of the credential boundary: an allowlisted host with nothing bound to it is
// reached only by AO_GITLAB_TOKEN/GITLAB_TOKEN, which nothing attributes to
// that instance. Sending it would hand a likely-gitlab.com credential to
// whoever operates the server, so doctor reports the host instead of probing
// it — and gitlab.com, whose token this most likely is, is still validated.
func TestDoctorNeverSendsTheGlobalDefaultToASelfManagedHost(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	selfHosted, hostTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"self-hosted-user"}`)
	dotCom, dotComTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "dotcom-pat")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return selfHosted.URL }

	checks := c.runDoctor(context.Background())
	check := findDoctorCheck(t, checks, "gitlab-token:gitlab.internal")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "not probed") {
		t.Fatalf("self-managed check = %+v, want WARN reporting the skipped probe", check)
	}
	if got := hostTokens(); len(got) != 0 {
		t.Fatalf("self-managed probe tokens = %v, want the global default never sent to an internal host", got)
	}
	if check := findDoctorCheck(t, checks, "gitlab-token"); check.Level != doctorPass {
		t.Fatalf("gitlab.com check = %+v, want PASS: nothing else claims that token", check)
	}
	if got := dotComTokens(); len(got) != 1 || got[0] != "dotcom-pat" {
		t.Fatalf("gitlab.com probe tokens = %v, want the global default once", got)
	}
}

// TestDoctorNeverSendsSelfManagedTokenToDotCom covers the credential boundary:
// when the default token is also the credential for an allowlisted host,
// doctor cannot attribute it to gitlab.com, so it must skip the gitlab.com
// probe entirely rather than disclose a possibly-internal token to a third
// party.
func TestDoctorNeverSendsSelfManagedTokenToDotCom(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	selfHosted, _ := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"self-hosted-user"}`)
	dotCom, dotComTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "self-managed-token")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	// The host binds that same value, so the credential is attributable to the
	// internal instance and gitlab.com's claim on it is ambiguous.
	t.Setenv("AO_GITLAB_HOST_TOKENS", "gitlab.internal=self-managed-token")
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return selfHosted.URL }

	checks := c.runDoctor(context.Background())
	check := findDoctorCheck(t, checks, "gitlab-token")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "not probed") {
		t.Fatalf("gitlab.com check = %+v, want WARN reporting the skipped probe", check)
	}
	if !strings.Contains(check.Message, "gitlab.internal") {
		t.Fatalf("gitlab.com check message = %q, want the sharing host named", check.Message)
	}
	if got := dotComTokens(); len(got) != 0 {
		t.Fatalf("gitlab.com probe tokens = %v, want the self-managed token never sent to gitlab.com", got)
	}
	if check := findDoctorCheck(t, checks, "gitlab-token:gitlab.internal"); check.Level != doctorPass {
		t.Fatalf("self-managed check = %+v, want PASS", check)
	}
}

// TestDoctorNeverSendsGLabHostTokenToDotCom covers the credential boundary
// when the self-managed host has its own AO_GITLAB_HOST_TOKENS entry: the
// default token then comes from a glab logged into the internal instance, so
// it is not byte-identical to any host credential, yet it is still an internal
// credential and must not reach gitlab.com.
func TestDoctorNeverSendsGLabHostTokenToDotCom(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	selfHosted, _ := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"self-hosted-user"}`)
	dotCom, dotComTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "glab": "/bin/glab"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/bin/glab" {
			return []byte("git version 2.43.0\n"), nil
		}
		// glab is logged into the internal instance only: asked about
		// gitlab.com it reports nothing, and the unscoped query answers with
		// the internal host's token.
		if slices.Contains(args, "gitlab.com") {
			return []byte("Hostname: gitlab.com\nNo token found\n"), nil
		}
		return []byte("Hostname: gitlab.internal\n✓ Token found: glpat-internal\n"), nil
	})
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	t.Setenv("AO_GITLAB_HOST_TOKENS", "gitlab.internal=glpat-configured")
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return selfHosted.URL }

	checks := c.runDoctor(context.Background())
	check := findDoctorCheck(t, checks, "gitlab-token")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "no token for gitlab.com") {
		t.Fatalf("gitlab.com check = %+v, want WARN reporting no gitlab.com credential", check)
	}
	if got := dotComTokens(); len(got) != 0 {
		t.Fatalf("gitlab.com probe tokens = %v, want the internal glab token never sent to gitlab.com", got)
	}
	if check := findDoctorCheck(t, checks, "gitlab-token:gitlab.internal"); check.Level != doctorPass {
		t.Fatalf("self-managed check = %+v, want PASS from its configured token", check)
	}
}

// TestDoctorProbesDotComWithItsOwnToken covers the other side of that
// boundary: when every self-managed host has its own credential, the default
// token is gitlab.com's, so gitlab.com is probed and a rejection is a real
// failure rather than a warning.
func TestDoctorProbesDotComWithItsOwnToken(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	selfHosted, _ := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"self-hosted-user"}`)
	dotCom, dotComTokens := gitlabDoctorHostServer(t, http.StatusUnauthorized, `{"message":"401 Unauthorized"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "dotcom-token")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	t.Setenv("AO_GITLAB_HOST_TOKENS", "gitlab.internal=internal-token")
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return selfHosted.URL }

	checks := c.runDoctor(context.Background())
	check := findDoctorCheck(t, checks, "gitlab-token")
	if check.Level != doctorFail || !strings.Contains(check.Message, "HTTP 401") {
		t.Fatalf("gitlab.com check = %+v, want FAIL for a rejected gitlab.com token", check)
	}
	if got := dotComTokens(); len(got) != 1 || got[0] != "dotcom-token" {
		t.Fatalf("gitlab.com probe tokens = %v, want only the default token", got)
	}
}

// TestDoctorIgnoresEmptyPerHostGitLabToken covers `host=` in
// AO_GITLAB_HOST_TOKENS: an empty override is not a credential. Doctor must
// fall back to the default token chain, exactly as the daemon's
// gitlabHostTokenSources does, instead of validating an empty token — and it
// must say so, because an entry the user believes is in use being read by
// nobody is exactly the silent misconfiguration doctor exists to surface.
func TestDoctorIgnoresEmptyPerHostGitLabToken(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	srv, tokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"self-hosted-user"}`)
	dotCom, _ := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "env-token")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	t.Setenv("AO_GITLAB_HOST_TOKENS", "gitlab.internal=")
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return srv.URL }

	checks := c.runDoctor(context.Background())
	// Nothing is bound to the host, so the global default is all that is left
	// for it — reported, not sent.
	check := findDoctorCheck(t, checks, "gitlab-token:gitlab.internal")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "AO_GITLAB_TOKEN") {
		t.Fatalf("gitlab-token check = %+v, want WARN naming the global default", check)
	}
	if got := tokens(); len(got) != 0 {
		t.Fatalf("probe tokens = %v, want no request from an empty override", got)
	}
	unused := findDoctorCheck(t, checks, "gitlab-host-tokens")
	if unused.Level != doctorWarn || !strings.Contains(unused.Message, "no token value") {
		t.Fatalf("gitlab-host-tokens check = %+v, want WARN flagging the valueless entry", unused)
	}
}

// TestDoctorWarnsWhenSelfManagedHostUnreachable covers the off-VPN case: a
// transport failure against an internal instance says nothing about the
// credential, so it must not fail `ao doctor`.
func TestDoctorWarnsWhenSelfManagedHostUnreachable(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	dotCom, _ := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachableURL := unreachable.URL
	unreachable.Close() // nothing is listening on that port any more
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "env-token")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	// The host needs a credential of its own, or doctor reports it as unprobed
	// before it ever reaches the network.
	t.Setenv("AO_GITLAB_HOST_TOKENS", "gitlab.internal=host-pat")
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return unreachableURL }

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token:gitlab.internal")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "unreachable") {
		t.Fatalf("gitlab-token check = %+v, want WARN for an unreachable self-managed host", check)
	}
}

// TestDoctorSurfacesGLabCommandFailure covers diagnostics: when `glab auth
// status` itself fails, doctor must report why rather than collapsing it into
// the same message as "authenticated but printed no token".
func TestDoctorSurfacesGLabCommandFailure(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	dotCom, _ := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "glab": "/bin/glab"}, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "/bin/glab" {
			return nil, errors.New("config file permission denied")
		}
		return []byte("git version 2.43.0\n"), nil
	})
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "config file permission denied") {
		t.Fatalf("gitlab-token check = %+v, want WARN naming the glab failure", check)
	}
}

// TestDoctorUsesPerHostGitLabToken covers AO_GITLAB_HOST_TOKENS: the per-host
// override must be the credential doctor validates, not the default token, and
// must be matched case-insensitively like the provider does.
func TestDoctorUsesPerHostGitLabToken(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	srv, tokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"host-user"}`)
	dotCom := gitlabDoctorServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "default-token")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.example.com")
	t.Setenv("AO_GITLAB_HOST_TOKENS", "GitLab.Example.COM=host-token")
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return srv.URL }

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token:gitlab.example.com")
	if check.Level != doctorPass || !strings.Contains(check.Message, "AO_GITLAB_HOST_TOKENS") {
		t.Fatalf("gitlab-token check = %+v, want PASS sourced from AO_GITLAB_HOST_TOKENS", check)
	}
	if got := tokens(); len(got) != 1 || got[0] != "host-token" {
		t.Fatalf("probe tokens = %v, want the per-host override", got)
	}
}

// TestDoctorProbesEveryAllowedGitLabHost covers multi-instance setups: each
// allowlisted host gets its own check, duplicates and gitlab.com aliases
// collapse into the default check, and host names are normalized.
func TestDoctorProbesEveryAllowedGitLabHost(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	dotCom := gitlabDoctorServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	selfHosted, _ := gitlabDoctorHostServer(t, http.StatusUnauthorized, `{"message":"401 Unauthorized"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "env-token")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.com, www.gitlab.com, GitLab.Internal , gitlab.internal,")
	t.Setenv("AO_GITLAB_HOST_TOKENS", "gitlab.internal=host-token")
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return selfHosted.URL }

	checks := c.runDoctor(context.Background())
	// The internal host carries its own credential, so the default token is
	// gitlab.com's and is probed there.
	if check := findDoctorCheck(t, checks, "gitlab-token"); check.Level != doctorPass {
		t.Fatalf("gitlab.com check = %+v, want PASS for the default token", check)
	}
	check := findDoctorCheck(t, checks, "gitlab-token:gitlab.internal")
	if check.Level != doctorFail || !strings.Contains(check.Message, "HTTP 401") {
		t.Fatalf("self-managed check = %+v, want FAIL rejected token", check)
	}
	gitlabChecks := 0
	for _, check := range checks {
		if strings.HasPrefix(check.Name, "gitlab-token") {
			gitlabChecks++
		}
	}
	if gitlabChecks != 2 {
		t.Fatalf("gitlab-token checks = %d, want 2 (gitlab.com aliases and duplicate hosts collapsed)", gitlabChecks)
	}
}

// TestDoctorScopesGLabLookupToHost covers the glab fallback for a self-managed
// host: the lookup must name the host, otherwise a glab authenticated against
// several instances returns whichever it lists first.
func TestDoctorScopesGLabLookupToHost(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	srv, tokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"self-hosted-user"}`)
	dotCom := gitlabDoctorServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	var glabArgs [][]string
	c := doctorContext(t, map[string]string{"git": "/bin/git", "glab": "/bin/glab"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/bin/glab" {
			return []byte("git version 2.43.0\n"), nil
		}
		glabArgs = append(glabArgs, args)
		for i, arg := range args {
			if arg == "--hostname" && i+1 < len(args) && args[i+1] == "gitlab.internal" {
				return []byte("Hostname: gitlab.internal\n✓ Token found: glpat-self-hosted\n"), nil
			}
		}
		return []byte("Hostname: gitlab.com\n✓ Token found: glpat-dotcom\n"), nil
	})
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return srv.URL }

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token:gitlab.internal")
	if check.Level != doctorPass || !strings.Contains(check.Message, "glab token valid") {
		t.Fatalf("gitlab-token check = %+v, want PASS from a host-scoped glab lookup", check)
	}
	if got := tokens(); len(got) != 1 || got[0] != "glpat-self-hosted" {
		t.Fatalf("probe tokens = %v, want the token glab reports for the host", got)
	}
	scoped := false
	for _, args := range glabArgs {
		if slices.Contains(args, "--hostname") {
			scoped = true
		}
	}
	if !scoped {
		t.Fatalf("glab invocations = %v, want one scoped with --hostname", glabArgs)
	}
}

// TestDoctorNeverSendsAnotherHostsGLabTokenToHost covers the other half of the
// credential boundary: when the host-scoped glab query yields nothing, the
// unscoped fallback names gitlab.com as the owner of the only token glab has.
// Doctor must not hand that credential to a self-managed instance just because
// the daemon's chain also falls back to the unscoped lookup — while gitlab.com,
// which the token is attributed to, is still validated with it.
func TestDoctorNeverSendsAnotherHostsGLabTokenToHost(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	srv, tokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"self-hosted-user"}`)
	dotCom, dotComTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "glab": "/bin/glab"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/bin/glab" {
			return []byte("git version 2.43.0\n"), nil
		}
		// An installed glab that predates `--hostname`: no lookup can be
		// scoped, so nothing glab returns is attributable to an instance.
		if slices.Contains(args, "--hostname") {
			return nil, errors.New("unknown flag: --hostname")
		}
		return []byte("Hostname: gitlab.com\n✓ Token found: glpat-default\n"), nil
	})
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	c.deps.HTTPClient = srv.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return srv.URL }

	checks := c.runDoctor(context.Background())
	check := findDoctorCheck(t, checks, "gitlab-token:gitlab.internal")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "no token for gitlab.internal") {
		t.Fatalf("self-managed check = %+v, want WARN reporting no credential for the host", check)
	}
	if got := tokens(); len(got) != 0 {
		t.Fatalf("self-managed probe tokens = %v, want glab's default-host token never sent to gitlab.internal", got)
	}
	// The very same output attributes that token to gitlab.com, so gitlab.com
	// is validated with it rather than left unchecked.
	if check := findDoctorCheck(t, checks, "gitlab-token"); check.Level != doctorPass {
		t.Fatalf("gitlab.com check = %+v, want PASS from the token attributed to gitlab.com", check)
	}
	if got := dotComTokens(); len(got) != 1 || got[0] != "glpat-default" {
		t.Fatalf("gitlab.com probe tokens = %v, want gitlab.com's own token once", got)
	}
}

// gitlabDoctorHostServer is gitlabDoctorServer plus a recorder of the
// PRIVATE-TOKEN values it was called with, so tests can assert which credential
// doctor picked for a host.
func gitlabDoctorHostServer(t *testing.T, status int, body string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user" {
			t.Errorf("unexpected gitlab probe: %s %s", r.Method, r.URL.Path)
			return
		}
		mu.Lock()
		tokens = append(tokens, r.Header.Get("PRIVATE-TOKEN"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), tokens...)
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
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "")
	t.Setenv("AO_GITLAB_HOST_TOKENS", "")
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

// glabSelfManagedStatus is `glab auth status --show-token` output for a glab
// whose only (and therefore default) instance is self-managed.
const glabSelfManagedStatus = "gitlab.internal\n" +
	"  ✓ Logged in to gitlab.internal as alice (keyring)\n" +
	"  ✓ Token: glpat-internal\n"

// TestDoctorNeverSendsGLabDefaultHostTokenToDotCom covers the credential
// boundary for the default probe: glab's own default host is independent of
// AO's allowlist, so a token it reports without being asked about gitlab.com
// may belong to an internal instance. Sending it to gitlab.com would disclose
// it to a third party, so the probe must be skipped instead.
func TestDoctorNeverSendsGLabDefaultHostTokenToDotCom(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	dotCom, dotComTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "glab": "/bin/glab"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/glab" {
			if slices.Contains(args, "--hostname") {
				return []byte("unknown host\n"), errors.New("exit status 1")
			}
			return []byte(glabSelfManagedStatus), nil
		}
		return []byte("git version 2.43.0\n"), nil
	})
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "no token for gitlab.com") {
		t.Fatalf("gitlab-token check = %+v, want WARN reporting no gitlab.com credential", check)
	}
	if got := dotComTokens(); len(got) != 0 {
		t.Fatalf("gitlab.com probe tokens = %v, want another instance's token never sent to gitlab.com", got)
	}
}

// TestDoctorProbesDotComWithAttributedGLabDefaultHostToken covers a glab too
// old for `auth status --hostname`: the unscoped status block still names the
// instance each token belongs to, so a token it attributes to gitlab.com is
// safe to validate against gitlab.com.
func TestDoctorProbesDotComWithAttributedGLabDefaultHostToken(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	dotCom, dotComTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "glab": "/bin/glab"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/glab" {
			if slices.Contains(args, "--hostname") {
				return []byte("unknown flag: --hostname\n"), errors.New("exit status 1")
			}
			return []byte("Hostname: gitlab.com\n✓ Token found: glpat-dotcom\n"), nil
		}
		return []byte("git version 2.43.0\n"), nil
	})
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token")
	if check.Level != doctorPass || !strings.Contains(check.Message, "dotcom-user") {
		t.Fatalf("gitlab-token check = %+v, want PASS from the attributed glab token", check)
	}
	if got := dotComTokens(); len(got) != 1 || got[0] != "glpat-dotcom" {
		t.Fatalf("gitlab.com probe tokens = %v, want gitlab.com's own token once", got)
	}
}

// TestDoctorPrefersHostScopedGLabTokenOverGlobalEnv covers credential
// precedence for a self-managed host: the credential glab holds for that
// instance wins over the global default, which belongs to gitlab.com and would
// both fail there and disclose itself to the internal server.
func TestDoctorPrefersHostScopedGLabTokenOverGlobalEnv(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	selfHosted, hostTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"self-hosted-user"}`)
	dotCom, dotComTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "glab": "/bin/glab"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/glab" {
			if slices.Contains(args, "gitlab.internal") {
				return []byte(glabSelfManagedStatus), nil
			}
			if slices.Contains(args, "--hostname") {
				return []byte("unknown host\n"), errors.New("exit status 1")
			}
			return []byte(glabSelfManagedStatus), nil
		}
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("GITLAB_TOKEN", "dotcom-pat")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return selfHosted.URL }

	checks := c.runDoctor(context.Background())
	if check := findDoctorCheck(t, checks, "gitlab-token:gitlab.internal"); check.Level != doctorPass {
		t.Fatalf("self-managed check = %+v, want PASS", check)
	}
	if got := hostTokens(); len(got) != 1 || got[0] != "glpat-internal" {
		t.Fatalf("self-managed probe tokens = %v, want the host's own glab token", got)
	}
	// The global default is no longer the self-managed host's credential, so
	// gitlab.com is validated with it instead of being skipped.
	if check := findDoctorCheck(t, checks, "gitlab-token"); check.Level != doctorPass {
		t.Fatalf("gitlab.com check = %+v, want PASS from the global default token", check)
	}
	if got := dotComTokens(); len(got) != 1 || got[0] != "dotcom-pat" {
		t.Fatalf("gitlab.com probe tokens = %v, want the global default token once", got)
	}
}

// TestDoctorIgnoresTokensPrintedByAFailedGLabRun covers a glab that exits
// non-zero: its diagnostics are not a credential, however much the text looks
// like one, and doctor must not send a scrap of an error message to GitLab as a
// token.
func TestDoctorIgnoresTokensPrintedByAFailedGLabRun(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	dotCom, dotComTokens := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git", "glab": "/bin/glab"}, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/glab" {
			return []byte("Hostname: gitlab.com\nerror: Token: expired or revoked\n"), errors.New("exit status 1")
		}
		return []byte("git version 2.43.0\n"), nil
	})
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-token")
	if check.Level != doctorWarn {
		t.Fatalf("gitlab-token check = %+v, want WARN rather than a probe with error text", check)
	}
	if got := dotComTokens(); len(got) != 0 {
		t.Fatalf("gitlab.com probe tokens = %v, want no request from a failed glab run", got)
	}
}

// TestDoctorFlagsHostTokensForUnallowlistedHosts covers the misconfiguration
// doctor exists to catch: a per-host token whose host is not allowlisted is
// dropped by the provider before any request, so GitLab observation is dead for
// that instance while every check still reads green.
func TestDoctorFlagsHostTokensForUnallowlistedHosts(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	dotCom, _ := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "env-token")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	t.Setenv("AO_GITLAB_HOST_TOKENS", "gitlab.internal=glpat-ok,gitlab.typo=glpat-unused")
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return dotCom.URL }

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "gitlab-host-tokens")
	if check.Level != doctorWarn || !strings.Contains(check.Message, "gitlab.typo") {
		t.Fatalf("gitlab-host-tokens check = %+v, want WARN naming the unallowlisted host", check)
	}
	if strings.Contains(check.Message, "gitlab.internal=") || strings.Contains(check.Message, "glpat-") {
		t.Fatalf("gitlab-host-tokens message = %q, want no token values in doctor output", check.Message)
	}
}

// TestDoctorStaysQuietWhenEveryHostTokenIsAllowlisted covers the other side:
// a correct AO_GITLAB_HOST_TOKENS must not add noise to `ao doctor`.
func TestDoctorStaysQuietWhenEveryHostTokenIsAllowlisted(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitLabEnv(t)
	dotCom, _ := gitlabDoctorHostServer(t, http.StatusOK, `{"username":"dotcom-user"}`)
	c := doctorContext(t, map[string]string{"git": "/bin/git"}, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("git version 2.43.0\n"), nil
	})
	t.Setenv("AO_GITLAB_TOKEN", "env-token")
	t.Setenv("AO_GITLAB_ALLOWED_HOSTS", "gitlab.internal")
	t.Setenv("AO_GITLAB_HOST_TOKENS", "GitLab.Internal=glpat-ok")
	c.deps.HTTPClient = dotCom.Client()
	c.deps.DoctorGitLabRESTBase = dotCom.URL
	c.deps.DoctorGitLabHostRESTBase = func(string) string { return dotCom.URL }

	for _, check := range c.runDoctor(context.Background()) {
		if check.Name == "gitlab-host-tokens" {
			t.Fatalf("unexpected check for a correct configuration: %+v", check)
		}
	}
}
