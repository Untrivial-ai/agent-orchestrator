package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resolvePRRef's gh fallback runs on the machine executing the CLI, in
// project.Path — a path the REMOTE daemon reported. The assertion that matters
// is the call count, not the message: "refused" and "ran gh against the wrong
// checkout and got a plausible URL back" produce output that looks the same.
//
// Deliberately path-free (no filepath, no t.Chdir, no absolute-path literals):
// project.Path here is an opaque string the CLI must never hand to a
// subprocess, and it must stay opaque on every runner OS.
func TestResolvePRRefRefusesLocalGhForRemoteProject(t *testing.T) {
	project := projectDetails{ID: "p1", Path: "remote/host/checkout", Repo: ""}

	for _, source := range []string{"--url", "AO_URL"} {
		t.Run(source, func(t *testing.T) {
			calls := 0
			ctx := &commandContext{
				deps: Deps{CommandOutputInDir: func(context.Context, string, string, ...string) ([]byte, error) {
					calls++
					return []byte("https://github.com/laptop/wrong-repo"), nil
				}}.withDefaults(),
				remote: &remoteTarget{baseURL: "http://host:3011", token: "tok", source: source},
			}

			got, err := ctx.resolvePRRef(context.Background(), "42", project)
			if err == nil {
				t.Fatalf("resolvePRRef = %q, want a refusal", got)
			}
			if calls != 0 {
				t.Fatalf("refused resolvePRRef still ran %d subprocess(es)", calls)
			}
			if code := ExitCode(err); code != 2 {
				t.Errorf("exit code = %d, want 2 (usage)", code)
			}
			for _, want := range []string{source, "http://host:3011", "gh repo view", "full PR URL"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
}

// A full PR URL needs no repo lookup, so it stays usable against a remote
// target — the refusal must be scoped to the fallback, not to the command.
func TestResolvePRRefAcceptsFullURLForRemoteProject(t *testing.T) {
	calls := 0
	ctx := &commandContext{
		deps: Deps{CommandOutputInDir: func(context.Context, string, string, ...string) ([]byte, error) {
			calls++
			return nil, errors.New("must not run")
		}}.withDefaults(),
		remote: &remoteTarget{baseURL: "http://host:3011", token: "tok", source: "--url"},
	}

	got, err := ctx.resolvePRRef(context.Background(), "https://github.com/owner/repo/pull/7", projectDetails{Path: "remote/host/checkout"})
	if err != nil {
		t.Fatalf("resolvePRRef = %v, want success", err)
	}
	if want := "https://github.com/owner/repo/pull/7"; got != want {
		t.Errorf("resolvePRRef = %q, want %q", got, want)
	}
	if calls != 0 {
		t.Errorf("full PR URL ran %d subprocess(es), want 0", calls)
	}
}

// End to end through the real command, against an httptest stand-in remote
// daemon that serves a project record with an empty repo — the case the audit
// could not reach on live hardware. It proves the guard is actually reachable
// from `ao session claim-pr --url`, which the unit tests above cannot: an
// earlier step could have short-circuited it.
//
// Two assertions, neither of which is the message: no subprocess ran, and the
// remote request log stops after the two reads, with no PR claimed. Without the
// guard the laptop's gh answers, the claim POST goes out carrying a URL derived
// from this machine's checkout, and the command prints a plain success.
func TestSessionClaimPRRefusesLocalGhAgainstRemoteDaemon(t *testing.T) {
	aoHome(t)
	setConfigEnv(t)
	t.Setenv("AO_TOKEN", "tok")

	log := &sessionRequestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.append(r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1":
			_, _ = io.WriteString(w, `{"session":`+sessionJSON("demo-1", "demo", "worker", "working", false)+`}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"remote/host/checkout","repo":"","defaultBranch":"main"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/pr/claim":
			// Answers happily, so that without the guard this test's failure mode
			// is what the audit describes: a plain success on the wrong PR.
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-1","prs":[],"branchChanged":false,"takenOverFrom":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	calls := 0
	_, _, err := executeCLI(t, Deps{
		CommandOutputInDir: func(context.Context, string, string, ...string) ([]byte, error) {
			calls++
			return []byte("https://github.com/laptop/wrong-repo"), nil
		},
	}, "session", "claim-pr", "demo-1", "142", "--url", srv.URL)
	if err == nil {
		t.Fatal("session claim-pr --url with a repo-less remote project succeeded, want a refusal")
	}
	if calls != 0 {
		t.Fatalf("refused claim-pr still ran %d subprocess(es)", calls)
	}
	if code := ExitCode(err); code != 2 {
		t.Errorf("exit code = %d, want 2 (usage)", code)
	}
	for _, req := range log.all() {
		if strings.Contains(req, "/pr/claim") {
			t.Errorf("refused claim-pr still claimed a PR on the remote daemon: %v", log.all())
		}
	}
}

// Local behavior must not change: with no remote target the gh fallback still
// runs and still resolves a bare number.
func TestResolvePRRefLocalStillUsesGhFallback(t *testing.T) {
	calls := 0
	ctx := &commandContext{
		deps: Deps{CommandOutputInDir: func(_ context.Context, dir, name string, _ ...string) ([]byte, error) {
			calls++
			if dir != "local/checkout" || name != "gh" {
				t.Errorf("CommandOutputInDir(%q, %q), want the project path and gh", dir, name)
			}
			return []byte("https://github.com/owner/repo\n"), nil
		}}.withDefaults(),
	}

	got, err := ctx.resolvePRRef(context.Background(), "#42", projectDetails{Path: "local/checkout"})
	if err != nil {
		t.Fatalf("resolvePRRef = %v, want success", err)
	}
	if want := "https://github.com/owner/repo/pull/42"; got != want {
		t.Errorf("resolvePRRef = %q, want %q", got, want)
	}
	if calls != 1 {
		t.Errorf("gh fallback ran %d times, want 1", calls)
	}
}
