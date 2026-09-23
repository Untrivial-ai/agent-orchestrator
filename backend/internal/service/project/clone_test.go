package project

import "testing"

func TestCloneRepositoryNameDecodesURLPathOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "space", url: "file:///tmp/my%20repo.git", want: "my repo"},
		{name: "path separator", url: "https://github.com/acme/nested%2Frepo.git", want: "repo"},
		{name: "escaped percent", url: "file:///tmp/literal%252Frepo.git", want: "literal%2Frepo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := cloneRepositoryName(tt.url)
			if err != nil {
				t.Fatalf("cloneRepositoryName(%q) returned error: %v", tt.url, err)
			}
			if got != tt.want {
				t.Fatalf("cloneRepositoryName(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestCloneRepositoryNameRejectsMalformedEscape(t *testing.T) {
	t.Parallel()

	if _, err := cloneRepositoryName("file:///tmp/bad%ZZ.git"); err == nil {
		t.Fatal("cloneRepositoryName() accepted a malformed URL escape")
	}
}
