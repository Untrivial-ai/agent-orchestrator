package commanddetail

import "testing"

func TestUnwrapShell(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "plain", command: "date -u", want: "date -u"},
		{name: "single quoted wrapper", command: "/bin/sh -c 'ls -la'", want: "ls -la"},
		{name: "double quoted wrapper", command: `/bin/bash -lc "git status"`, want: "git status"},
		{
			name:    "nested double quotes remain exact",
			command: `/bin/zsh -lc 'printf "%s" "hello world"'`,
			want:    `printf "%s" "hello world"`,
		},
		{
			name:    "nested single quotes remain exact",
			command: `/bin/zsh -lc "printf '%s' 'hello world'"`,
			want:    `printf '%s' 'hello world'`,
		},
		{name: "unquoted wrapper", command: "/bin/fish -c echo hello", want: "echo hello"},
		{name: "non shell c flag", command: "python -c print(1)", want: "python -c print(1)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UnwrapShell(tt.command); got != tt.want {
				t.Fatalf("UnwrapShell(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}
