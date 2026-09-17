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

func TestScmProvider(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		remote string
		want   string
	}{
		{"empty", "", ""},
		{"whitespace-only", "   ", ""},
		{"github-scp", "git@github.com:aoagents/agent-orchestrator.git", "github"},
		{"github-https", "https://github.com/aoagents/agent-orchestrator.git", "github"},
		{"github-ssh-url", "ssh://git@github.com/octocat/hello.git", "github"},
		{"github-git-proto", "git://github.com/octocat/hello.git", "github"},
		{"github-whitespace", "  https://github.com/aoagents/x.git  ", "github"},
		{"gitlab-scp", "git@gitlab.com:group/repo.git", "gitlab"},
		{"gitlab-https", "https://gitlab.com/group/subgroup/repo.git", "gitlab"},
		{"gitlab-https-userinfo", "https://oauth2:token@gitlab.com/group/repo.git", "gitlab"},
		{"gitlab-ssh-port", "ssh://git@gitlab.com:22/group/repo.git", "gitlab"},
		{"bitbucket-scp", "git@bitbucket.org:team/repo.git", "bitbucket"},
		{"bitbucket-https", "https://bitbucket.org/team/repo.git", "bitbucket"},
		{"self-hosted-gitlab-is-other", "git@gitlab.example.com:group/repo.git", "other"},
		{"gist-subdomain-is-other", "https://gist.github.com/aoagents/abc", "other"},
		{"unknown-host-is-other", "https://git.sr.ht/~user/repo", "other"},
		{"has-remote-but-garbage-is-other", "not-a-url", "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := scmProvider(tc.remote); got != tc.want {
				t.Fatalf("scmProvider(%q) = %q, want %q", tc.remote, got, tc.want)
			}
		})
	}
}
