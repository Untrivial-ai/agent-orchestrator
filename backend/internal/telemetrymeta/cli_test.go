package telemetrymeta

import "testing"

func TestNormalizeCommandPath(t *testing.T) {
	if got := NormalizeCommandPath("  AO   Hooks  claude-code  post-tool-use "); got != "ao hooks claude-code post-tool-use" {
		t.Fatalf("NormalizeCommandPath = %q, want normalized lowercase fields", got)
	}
}

func TestIsRoutineInternalCLICommandNormalizesLegacyShapes(t *testing.T) {
	for _, commandPath := range []string{
		"ao hooks",
		"ao  hooks",
		"AO HOOKS",
		"ao hooks claude-code post-tool-use",
		"ao session get sess-123",
		"ao session agent-switch ls sess-123",
		"ao session handoff submit --switch switch-1",
		"ao project ls",
		"ao pty-host session-1",
	} {
		if !IsRoutineInternalCLICommand(commandPath) {
			t.Errorf("IsRoutineInternalCLICommand(%q) = false, want true", commandPath)
		}
	}
}

func TestCLIActorTypeKeepsKnownLegacyUserCommands(t *testing.T) {
	for _, commandPath := range []string{
		"ao agent ls",
		"ao session claim-pr",
		"ao session switch-agent",
		"ao session agent-switch",
		"ao session agent-switch ls",
		"ao dev import-projects",
		"ao project orchestration get",
		"ao project orchestration set",
		"ao handoff",
		"ao smoke list",
		"ao smoke set",
	} {
		if got := CLIActorType("", commandPath); got != "user" {
			t.Errorf("CLIActorType(%q) = %q, want user", commandPath, got)
		}
	}
}

func TestCLIActorTypeTreatsInternalAgentHandoffAsSystemByDefault(t *testing.T) {
	for _, commandPath := range []string{
		"ao session handoff",
		"ao session handoff submit",
	} {
		if got := CLIActorType("", commandPath); got != "system" {
			t.Errorf("CLIActorType(%q) = %q, want system", commandPath, got)
		}
	}
}

func TestCLIActorTypeSystemCommandsOverrideExplicitActor(t *testing.T) {
	for _, tc := range []struct {
		actorType   string
		commandPath string
	}{
		{actorType: "user", commandPath: "ao daemon"},
		{actorType: "agent", commandPath: "ao start"},
		{actorType: "user", commandPath: "AO  AGENT-PROCESS  SUPERVISE"},
	} {
		if got := CLIActorType(tc.actorType, tc.commandPath); got != "system" {
			t.Errorf("CLIActorType(%q, %q) = %q, want system", tc.actorType, tc.commandPath, got)
		}
	}
}

func TestCLIActorTypeKeepsConservativeFallback(t *testing.T) {
	for _, tc := range []struct {
		actorType   string
		commandPath string
		want        string
	}{
		{actorType: "agent", commandPath: "ao surprise", want: "agent"},
		{actorType: "user", commandPath: "ao surprise", want: "user"},
		{actorType: "system", commandPath: "ao spawn", want: "system"},
		{commandPath: "ao daemon", want: "system"},
		{commandPath: "ao spawn", want: "user"},
		{commandPath: "ao surprise", want: "system"},
	} {
		if got := CLIActorType(tc.actorType, tc.commandPath); got != tc.want {
			t.Errorf("CLIActorType(%q, %q) = %q, want %q", tc.actorType, tc.commandPath, got, tc.want)
		}
	}
}
