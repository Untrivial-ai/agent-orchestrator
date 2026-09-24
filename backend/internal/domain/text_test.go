package domain

import "testing"

func TestSanitizeControlChars(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text unchanged", in: "hello world", want: "hello world"},
		{name: "keeps newline tab carriage return", in: "a\nb\tc\rd", want: "a\nb\tc\rd"},
		{name: "strips ansi escape byte leaving harmless residue", in: "before\x1b[2Jafter", want: "before[2Jafter"},
		{name: "strips nul and bell", in: "x\x00y\az", want: "xyz"},
		{name: "strips osc sequence bytes", in: "\x1b]0;title\a", want: "]0;title"},
		{name: "empty stays empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeControlChars(tt.in); got != tt.want {
				t.Fatalf("SanitizeControlChars(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTitleCaseSessionTitle(t *testing.T) {
	tests := map[string]string{
		"redesign the topbar on the page": "Redesign the Topbar on the Page",
		"improve API errors":              "Improve API Errors",
		"PR #42 follow-up":                "PR #42 Follow-up",
	}
	for input, want := range tests {
		if got := TitleCaseSessionTitle(input); got != want {
			t.Errorf("TitleCaseSessionTitle(%q) = %q, want %q", input, got, want)
		}
	}
}
