package project

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func TestNormalizePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "absolute", raw: home, want: home},
		{name: "relative", raw: "repository", want: "repository"},
		{name: "clean", raw: "./parent/../repository/", want: "repository"},
		{name: "leading space", raw: " repository", want: " repository"},
		{name: "trailing space", raw: "repository ", want: "repository "},
		{name: "surrounding spaces", raw: " repository ", want: " repository "},
		{name: "explicit whitespace directory", raw: "./   ", want: "   "},
		{name: "home", raw: "~", want: home},
		{name: "home child", raw: "~/repository ", want: filepath.Join(home, "repository ")},
		{name: "backslash home child", raw: `~\repository `, want: filepath.Join(home, "repository ")},
		{name: "literal tilde with space", raw: "~ ", want: "~ "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			want, err := filepath.Abs(tt.want)
			if err != nil {
				t.Fatal(err)
			}
			got, err := normalizePath(tt.raw)
			if err != nil || got != want {
				t.Fatalf("normalizePath(%q) = %q, %v; want %q", tt.raw, got, err, want)
			}
		})
	}
	for _, raw := range []string{"", " ", "\t\r\n", "\u2003"} {
		t.Run("blank="+raw, func(t *testing.T) {
			got, err := normalizePath(raw)
			var apiError *apierr.Error
			if got != "" || !errors.As(err, &apiError) || apiError.Code != "PATH_REQUIRED" {
				t.Fatalf("normalizePath(%q) = %q, %v; want PATH_REQUIRED", raw, got, err)
			}
		})
	}
}
