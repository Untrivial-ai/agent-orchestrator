package domain

import (
	"strings"
	"testing"
)

func TestParseChangeRequestURL(t *testing.T) {
	tests := []struct {
		input, url, key string
	}{
		{"http://github.com/OWNER/Repo/pull/042/", "https://github.com/owner/repo/pull/42", "github|github.com|owner/repo|42"},
		{"https://www.github.com/Owner/Repo/pull/42", "https://github.com/owner/repo/pull/42", "github|github.com|owner/repo|42"},
		{"https://gitlab.com/group/sub/repo/-/merge_requests/9", "https://gitlab.com/group/sub/repo/-/merge_requests/9", "gitlab|gitlab.com|group/sub/repo|9"},
	}
	for _, tc := range tests {
		got, err := ParseChangeRequestURL(tc.input)
		if err != nil || got.URL != tc.url || got.Key() != tc.key {
			t.Errorf("ParseChangeRequestURL(%q) = %+v, %v", tc.input, got, err)
		}
	}
	invalid := []string{
		"https://github.com/o/r/pull/0", "https://github.com/o/r/issues/1",
		"https://api.github.com/o/r/pull/1", "https://gist.github.com/o/r/pull/1",
		"https://github.com/o/r/pull/1?x=1", "https://github.com/o/r/pull/1#x",
		"https://user:pass@github.com/o/r/pull/1", "https://github.com:123/o/r/pull/1",
		"https://gitlab.com/group/repo/-/merge_requests/nope", "https://gitlab.com/a/-/merge_requests/1",
		"git://github.com/o/r/pull/1", " https://github.com/o/r/pull/1",
		strings.Repeat("x", 2049),
	}
	for _, input := range invalid {
		if got, err := ParseChangeRequestURL(input); err == nil {
			t.Errorf("ParseChangeRequestURL(%q) = %+v, want error", input, got)
		}
	}
}
