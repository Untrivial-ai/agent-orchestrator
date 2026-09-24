package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestClaudeLoginRunsEverySupportedLoginMethod(t *testing.T) {
	tests := []struct {
		name      string
		selection string
		wantArgs  []string
	}{
		{name: "Claude subscription", selection: "1\n", wantArgs: []string{"auth", "login", "--claudeai"}},
		{name: "Anthropic Console", selection: "2\n", wantArgs: []string{"auth", "login", "--console"}},
		{name: "SSO", selection: "3\n", wantArgs: []string{"auth", "login", "--sso"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotName string
			var gotArgs []string
			deps := Deps{
				In:  strings.NewReader(tt.selection),
				Out: io.Discard,
				Err: io.Discard,
				LookPath: func(string) (string, error) {
					return "", errors.New("not on PATH")
				},
				RunInteractiveCommand: func(_ context.Context, name string, args []string, _ io.Reader, _, _ io.Writer) error {
					gotName = name
					gotArgs = append([]string(nil), args...)
					return nil
				},
			}

			cmd := newClaudeLoginCommand(&commandContext{deps: deps.withDefaults()})
			cmd.SetArgs([]string{"--executable", "/managed/bin/claude"})
			cmd.SetIn(deps.In)
			cmd.SetOut(deps.Out)
			cmd.SetErr(deps.Err)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if gotName != "/managed/bin/claude" {
				t.Errorf("executable = %q, want adapter-resolved Claude", gotName)
			}
			if !slices.Equal(gotArgs, tt.wantArgs) {
				t.Errorf("args = %q, want %q", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestClaudeLoginMenuListsEverySupportedMethod(t *testing.T) {
	var stdout bytes.Buffer
	if err := writeClaudeLoginMenu(&stdout, codexLoginStyle{}); err != nil {
		t.Fatalf("writeClaudeLoginMenu: %v", err)
	}
	for _, fragment := range []string{"Sign in to Claude Code", "Claude subscription", "Anthropic Console", "SSO", "Selection [1-3]"} {
		if !strings.Contains(stdout.String(), fragment) {
			t.Fatalf("menu missing %q:\n%s", fragment, stdout.String())
		}
	}
}
