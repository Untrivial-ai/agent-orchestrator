package qodercliacp

import (
	"context"
	"slices"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureLaunchesACPWithModelAndSystemPrompt(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{
		Model:        "qoder-ultimate",
		SystemPrompt: "Follow AO rules.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Fatalf("no environment overlay expected, got %#v", env)
	}
	if len(args) == 0 || args[0] != "--acp" {
		t.Fatalf("args = %#v, want --acp first", args)
	}
	for _, want := range [][2]string{{"--model", "qoder-ultimate"}, {"--append-system-prompt", "Follow AO rules."}} {
		index := slices.Index(args, want[0])
		if index < 0 || args[index+1] != want[1] {
			t.Fatalf("args = %#v, want %s %s", args, want[0], want[1])
		}
	}
	// Chat carries settings only to pin auto-update off; hooks here would
	// double-report every turn the ACP driver already reports.
	if index := slices.Index(args, "--settings"); index < 0 {
		t.Fatalf("args = %#v, want --settings", args)
	}
}

func TestSessionModeMapsAOApprovalVocabulary(t *testing.T) {
	for _, tc := range []struct {
		mode ports.PermissionMode
		want string
	}{
		{mode: ports.PermissionModeDefault, want: ""},
		{mode: ports.PermissionModeAcceptEdits, want: "acceptEdits"},
		{mode: ports.PermissionModeAuto, want: "auto"},
		{mode: ports.PermissionModeBypassPermissions, want: "yolo"},
		{mode: ports.PermissionMode("nonsense"), want: ""},
	} {
		if got := sessionMode(tc.mode); got != tc.want {
			t.Fatalf("sessionMode(%q) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestSessionOptionsCarryTheDurableModel(t *testing.T) {
	if options := sessionOptions(ports.ChatTurnSettings{}); options != nil {
		t.Fatalf("no model chosen should send no option, got %#v", options)
	}
	options := sessionOptions(ports.ChatTurnSettings{Model: "qoder-ultimate"})
	if len(options) != 1 || options[0].ID != "model" || options[0].Value != "qoder-ultimate" {
		t.Fatalf("unexpected session options %#v", options)
	}
}

func TestValidateVersionOutputEnforcesTheModeVocabularyFloor(t *testing.T) {
	if err := validateVersionOutput("1.1.49\n"); err != nil {
		t.Fatalf("1.1.49 should be admitted: %v", err)
	}
	if err := validateVersionOutput("1.1.37"); err != nil {
		t.Fatalf("the floor itself should be admitted: %v", err)
	}
	// Older builds advertise a different ACP mode vocabulary, so AO's mapping
	// would silently select modes they do not have.
	if err := validateVersionOutput("1.1.36"); err == nil {
		t.Fatal("1.1.36 should be refused")
	}
	if err := validateVersionOutput("not a version"); err == nil {
		t.Fatal("unparsable output should be refused")
	}
}
