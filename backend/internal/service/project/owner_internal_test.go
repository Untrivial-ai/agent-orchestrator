package project

import "testing"

func TestGithubOwner(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		remote string
		want   string
	}{
		{"scp-style", "git@github.com:aoagents/agent-orchestrator.git", "aoagents"},
		{"https", "https://github.com/aoagents/agent-orchestrator.git", "aoagents"},
		{"https-no-suffix", "https://github.com/aoagents/agent-orchestrator", "aoagents"},
		{"http", "http://github.com/octocat/hello", "octocat"},
		{"ssh-url", "ssh://git@github.com/octocat/hello.git", "octocat"},
		{"git-proto", "git://github.com/octocat/hello.git", "octocat"},
		{"personal-account", "git@github.com:pulkit7070/dotfiles.git", "pulkit7070"},
		{"whitespace", "  https://github.com/aoagents/x.git  ", "aoagents"},
		{"empty", "", ""},
		{"non-github", "git@gitlab.com:group/repo.git", ""},
		{"owner-only-no-repo", "https://github.com/aoagents", ""},
		{"gist-subdomain-not-matched", "https://gist.github.com/aoagents/abc", ""},
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
