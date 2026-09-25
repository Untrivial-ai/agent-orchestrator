package worker

import "testing"

func TestExtraRepoDirName(t *testing.T) {
	cases := map[string]string{
		"https://github.com/Pritom14/ChartSnip": "ChartSnip",
		"https://github.com/owner/repo.git":     "repo",
		"https://github.com/owner/repo/":        "repo",
		"https://github.com/owner/weird name":   "weird-name",
		"":                                      "repo",
	}
	for in, want := range cases {
		if got := ExtraRepoDirName(in); got != want {
			t.Errorf("ExtraRepoDirName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtraRepoPathIsSiblingOfWorkspace(t *testing.T) {
	got := ExtraRepoPath("/workspace/repository", "https://github.com/Pritom14/ChartSnip")
	want := "/workspace/ChartSnip"
	if got != want {
		t.Fatalf("ExtraRepoPath = %q, want %q (sibling of the primary checkout)", got, want)
	}
}
