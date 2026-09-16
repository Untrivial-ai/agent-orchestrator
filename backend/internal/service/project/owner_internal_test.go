package project

import (
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestGithubOwner(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		remote string
		want   string
	}{
		// --- the forms git writes by default ---
		{"scp-style", "git@github.com:aoagents/agent-orchestrator.git", "aoagents"},
		{"https", "https://github.com/aoagents/agent-orchestrator.git", "aoagents"},
		{"https-no-suffix", "https://github.com/aoagents/agent-orchestrator", "aoagents"},
		{"http", "http://github.com/octocat/hello", "octocat"},
		{"ssh-url", "ssh://git@github.com/octocat/hello.git", "octocat"},
		{"git-proto", "git://github.com/octocat/hello.git", "octocat"},
		{"personal-account", "git@github.com:pulkit7070/dotfiles.git", "pulkit7070"},
		{"whitespace", "  https://github.com/aoagents/x.git  ", "aoagents"},

		// --- credentials embedded by a credential helper ---
		{"https-token", "https://ghp_AbCdEf0123456789@github.com/aoagents/x.git", "aoagents"},
		{"https-user", "https://octocat@github.com/aoagents/x.git", "aoagents"},
		{"https-user-password", "https://octocat:ghp_secret@github.com/aoagents/x.git", "aoagents"},
		{"https-oauth2-helper", "https://oauth2:token@github.com/aoagents/x.git", "aoagents"},
		{"https-x-access-token", "https://x-access-token:ghs_tok@github.com/aoagents/x.git", "aoagents"},
		{"http-token", "http://tok@github.com/aoagents/x.git", "aoagents"},

		// --- SSH config host aliases, the multi-account convention ---
		{"scp-alias", "git@github.com-work:aoagents/agent-orchestrator.git", "aoagents"},
		{"scp-alias-personal", "git@github.com-personal:pulkit7070/dotfiles.git", "pulkit7070"},
		{"ssh-url-alias", "ssh://git@github.com-work/aoagents/x.git", "aoagents"},
		{"scp-no-user", "github.com:aoagents/x.git", "aoagents"},

		// --- explicit ports ---
		{"ssh-url-port", "ssh://git@github.com:22/octocat/hello.git", "octocat"},
		{"ssh-url-alias-port", "ssh://git@github.com-work:22/aoagents/x.git", "aoagents"},
		{"https-port", "https://github.com:443/aoagents/x.git", "aoagents"},
		{"ssh-url-port-no-user", "ssh://github.com:22/octocat/hello.git", "octocat"},

		// --- case variants; the owner segment keeps the case git recorded ---
		{"host-mixed-case", "https://GitHub.com/aoagents/x.git", "aoagents"},
		{"host-upper", "git@GITHUB.COM:aoagents/x.git", "aoagents"},
		{"scheme-upper", "HTTPS://github.com/aoagents/x.git", "aoagents"},
		{"alias-mixed-case", "git@GitHub.com-Work:aoagents/x.git", "aoagents"},
		{"owner-case-preserved", "https://github.com/AOAgents/x.git", "AOAgents"},

		// --- shape tolerances ---
		{"www-host", "https://www.github.com/aoagents/x.git", "aoagents"},
		{"trailing-dot-host", "https://github.com./aoagents/x.git", "aoagents"},
		{"scp-leading-slash", "git@github.com:/aoagents/x.git", "aoagents"},
		{"double-slash-path", "https://github.com//aoagents/x.git", "aoagents"},
		{"deep-path", "https://github.com/aoagents/x/tree/main", "aoagents"},
		{"hyphen-owner", "https://github.com/some-org/x.git", "some-org"},
		// Deliberate tolerance, not an oversight: GitHub forbids consecutive
		// dashes at signup, but this filter errs toward keeping a real owner
		// rather than toward matching the signup form. See isGitHubLogin.
		{"consecutive-dashes-tolerated", "https://github.com/some--org/x.git", "some--org"},

		// --- rejected: not github.com ---
		{"empty", "", ""},
		{"blank", "   ", ""},
		{"non-github", "git@gitlab.com:group/repo.git", ""},
		{"gitlab-https", "https://gitlab.com/group/repo.git", ""},
		{"gist-subdomain-not-matched", "https://gist.github.com/aoagents/abc", ""},
		{"lookalike-suffix-host", "https://github.com.evil.example/aoagents/x.git", ""},
		{"lookalike-prefix-host", "https://notgithub.com/aoagents/x.git", ""},
		{"dot-alias-not-a-host-alias", "git@github.com.work:aoagents/x.git", ""},

		// --- rejected: GitHub Enterprise is a separate namespace, out of scope ---
		{"ghe-host", "https://github.mycompany.com/platform/x.git", ""},
		{"ghe-scp", "git@github.mycompany.com:platform/x.git", ""},
		{"ghe-subdomain", "https://code.github.mycompany.com/platform/x.git", ""},

		// --- rejected: not a remote, or no owner/repo pair ---
		{"owner-only-no-repo", "https://github.com/aoagents", ""},
		{"scp-owner-only", "git@github.com:aoagents", ""},
		{"host-only", "https://github.com/", ""},
		{"unsupported-scheme", "ftp://github.com/aoagents/x.git", ""},
		{"file-scheme", "file:///home/dev/github.com/aoagents/x", ""},
		{"local-path", "/home/dev/src/aoagents/x", ""},
		{"relative-path", "../aoagents/x", ""},
		{"windows-path", `C:\src\aoagents\x`, ""},
		{"bare-word", "origin", ""},

		// --- rejected: first segment is not shaped like a GitHub login ---
		{"dot-segment-owner", "https://github.com/../x.git", ""},
		{"percent-escaped-owner", "https://github.com/%2e%2e/x.git", ""},
		{"space-in-owner", "https://github.com/two words/x.git", ""},
		{"owner-leading-hyphen", "https://github.com/-aoagents/x.git", ""},
		{"owner-trailing-hyphen", "https://github.com/aoagents-/x.git", ""},
		// GitHub normalizes every non-alphanumeric character to a dash, so no
		// real login contains one of these.
		{"owner-underscore", "https://github.com/some_user/x.git", ""},
		{"owner-dot", "https://github.com/some.user/x.git", ""},
		{"owner-plus", "https://github.com/some+user/x.git", ""},
		{"owner-too-long", "https://github.com/" + longName(40) + "/x.git", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := githubOwner(tc.remote); got != tc.want {
				t.Fatalf("githubOwner(%q) = %q, want %q", tc.remote, got, tc.want)
			}
		})
	}
}

// TestGithubOwnerBoundaryLength pins the login-length boundary itself, so the
// too-long rejection above cannot pass by rejecting every long-ish name.
func TestGithubOwnerBoundaryLength(t *testing.T) {
	t.Parallel()
	if got, want := githubOwner("https://github.com/"+longName(39)+"/x.git"), longName(39); got != want {
		t.Fatalf("39-character owner = %q, want it accepted", got)
	}
}

// TestGithubOwnerNeverLeaksBeyondTheOwner is the standing privacy rule: whatever
// a remote carries — repo name, host, credentials — only the owner segment may
// ever come back out.
func TestGithubOwnerNeverLeaksBeyondTheOwner(t *testing.T) {
	t.Parallel()
	remotes := []string{
		"https://ghp_TopSecretToken@github.com/aoagents/private-repo.git",
		"https://octocat:ghp_TopSecretToken@github.com/aoagents/private-repo.git",
		"git@github.com-work:aoagents/private-repo.git",
		"ssh://git@github.com:22/aoagents/private-repo.git",
	}
	for _, remote := range remotes {
		if got := githubOwner(remote); got != "aoagents" {
			t.Fatalf("githubOwner(%q) = %q, want aoagents", remote, got)
		}
	}
}

func longName(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
func TestWorkspaceRepoOwners(t *testing.T) {
	t.Parallel()
	repo := func(origin string) domain.WorkspaceRepoRecord {
		return domain.WorkspaceRepoRecord{RepoOriginURL: origin}
	}
	cases := []struct {
		name  string
		repos []domain.WorkspaceRepoRecord
		want  []string
	}{
		{"no-children", nil, []string{}},
		{
			"same-owner-deduped",
			[]domain.WorkspaceRepoRecord{
				repo("git@github.com:aoagents/api.git"),
				repo("https://github.com/aoagents/web.git"),
			},
			[]string{"aoagents"},
		},
		{
			"mixed-owners-sorted",
			[]domain.WorkspaceRepoRecord{
				repo("https://github.com/zulu/api.git"),
				repo("https://github.com/aoagents/web.git"),
			},
			[]string{"aoagents", "zulu"},
		},
		{
			"missing-and-non-github-remotes-ignored",
			[]domain.WorkspaceRepoRecord{
				repo(""),
				repo("git@gitlab.com:group/repo.git"),
				repo("https://github.com/aoagents/web.git"),
			},
			[]string{"aoagents"},
		},
		{
			"no-github-remotes",
			[]domain.WorkspaceRepoRecord{repo(""), repo("https://example.com/api.git")},
			[]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := workspaceRepoOwners(tc.repos); !slices.Equal(got, tc.want) {
				t.Fatalf("workspaceRepoOwners() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// Enumeration order must not leak into the payload: the same workspace has to
// produce the same owner list however the child records happen to be ordered.
func TestWorkspaceRepoOwnersIsOrderIndependent(t *testing.T) {
	t.Parallel()
	forward := workspaceRepoOwners([]domain.WorkspaceRepoRecord{
		{RepoOriginURL: "https://github.com/zulu/api.git"},
		{RepoOriginURL: "https://github.com/alpha/web.git"},
	})
	reverse := workspaceRepoOwners([]domain.WorkspaceRepoRecord{
		{RepoOriginURL: "https://github.com/alpha/web.git"},
		{RepoOriginURL: "https://github.com/zulu/api.git"},
	})
	if !slices.Equal(forward, reverse) {
		t.Fatalf("owners depend on child order: %#v vs %#v", forward, reverse)
	}
}
