package cli

import (
	"testing"
)

func TestCLIParsePRURL(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantHost  string
		wantOwner string
		wantRepo  string
		wantNum   int
		wantErr   bool
	}{
		{"github", "https://github.com/owner/repo/pull/42", "github.com", "owner", "repo", 42, false},
		{"gitlab", "https://gitlab.com/castai/ctxd/-/merge_requests/9", "gitlab.com", "castai", "ctxd", 9, false},
		{"gitlab nested namespace", "https://gitlab.com/group/subgroup/repo/-/merge_requests/5", "gitlab.com", "group/subgroup", "repo", 5, false},
		{"gitlab deep nested namespace", "https://gitlab.com/group/sub1/sub2/repo/-/merge_requests/3", "gitlab.com", "group/sub1/sub2", "repo", 3, false},
		{"not https", "http://github.com/owner/repo/pull/1", "", "", "", 0, true},
		{"bad number", "https://github.com/owner/repo/pull/abc", "", "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, owner, repo, num, err := cliParsePRURL(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tc.wantHost {
				t.Errorf("host = %q, want %q", host, tc.wantHost)
			}
			if owner != tc.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tc.wantOwner)
			}
			if repo != tc.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tc.wantRepo)
			}
			if num != tc.wantNum {
				t.Errorf("num = %d, want %d", num, tc.wantNum)
			}
		})
	}
}

func TestCLIRepoFromURL(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantHost  string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"github https", "https://github.com/owner/repo.git", "github.com", "owner", "repo", false},
		{"github https no suffix", "https://github.com/owner/repo", "github.com", "owner", "repo", false},
		{"gitlab https", "https://gitlab.com/castai/ctxd.git", "gitlab.com", "castai", "ctxd", false},
		{"gitlab nested namespace", "https://gitlab.com/group/subgroup/repo.git", "gitlab.com", "group/subgroup", "repo", false},
		{"gitlab deep nested namespace", "https://gitlab.com/group/sub1/sub2/repo.git", "gitlab.com", "group/sub1/sub2", "repo", false},
		{"gitlab self-managed nested", "https://gitlab.mycompany.com/eng/team/repo.git", "gitlab.mycompany.com", "eng/team", "repo", false},
		{"ssh remote", "git@gitlab.com:group/subgroup/repo.git", "gitlab.com", "group/subgroup", "repo", false},
		{"ssh single group", "git@github.com:owner/repo.git", "github.com", "owner", "repo", false},
		{"empty", "", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, owner, repo, err := cliRepoFromURL(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tc.wantHost {
				t.Errorf("host = %q, want %q", host, tc.wantHost)
			}
			if owner != tc.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tc.wantOwner)
			}
			if repo != tc.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tc.wantRepo)
			}
		})
	}
}

// TestNumericClaimNestedNamespace verifies that a numeric claim against a
// project whose repo origin URL uses a nested GitLab namespace constructs the
// correct merge-request path and round-trips through cliParsePRURL. This is the
// regression scenario from reviewer Item 10: before the fix, cliRepoFromURL
// truncated the namespace to the first component, so a numeric claim built
// /group/subgroup/-/merge_requests/N and failed same-repository validation.
func TestNumericClaimNestedNamespace(t *testing.T) {
	const repoOrigin = "https://gitlab.com/group/subgroup/repo.git"
	host, owner, repo, err := cliRepoFromURL(repoOrigin)
	if err != nil {
		t.Fatalf("cliRepoFromURL: %v", err)
	}
	const num = 7
	mrURL := cliPRURLFromParts(host, owner, repo, num)
	const wantURL = "https://gitlab.com/group/subgroup/repo/-/merge_requests/7"
	if mrURL != wantURL {
		t.Fatalf("mrURL = %q, want %q", mrURL, wantURL)
	}
	// Round-trip: the constructed MR URL must parse back to the same
	// host/owner/repo/number so same-repository validation passes.
	pHost, pOwner, pRepo, pNum, err := cliParsePRURL(mrURL)
	if err != nil {
		t.Fatalf("cliParsePRURL(%q): %v", mrURL, err)
	}
	if pHost != host || pOwner != owner || pRepo != repo || pNum != num {
		t.Errorf("round-trip mismatch: got host=%q owner=%q repo=%q num=%d, want host=%q owner=%q repo=%q num=%d",
			pHost, pOwner, pRepo, pNum, host, owner, repo, num)
	}
}
