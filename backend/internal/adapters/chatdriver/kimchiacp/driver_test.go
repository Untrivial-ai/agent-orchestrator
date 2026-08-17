package kimchiacp

import (
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureReturnsACPMode(t *testing.T) {
	args, env, err := configure(acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--mode", "acp"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
}

func TestConfigurePassesModel(t *testing.T) {
	args, _, err := configure(acpdriver.LaunchConfig{Model: "glm-5.2"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--mode", "acp", "--model", "glm-5.2"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestConfigurePassesAutoPermission(t *testing.T) {
	tests := []ports.PermissionMode{
		ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto,
	}
	for _, perm := range tests {
		args, _, err := configure(acpdriver.LaunchConfig{Permissions: perm})
		if err != nil {
			t.Fatalf("configure(%q): %v", perm, err)
		}
		want := []string{"--mode", "acp", "--auto"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("perm %q: args = %#v, want %#v", perm, args, want)
		}
	}
}

func TestConfigurePassesYoloPermission(t *testing.T) {
	args, _, err := configure(acpdriver.LaunchConfig{Permissions: ports.PermissionModeBypassPermissions})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--mode", "acp", "--yolo"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestConfigurePassesModelAndPermissionTogether(t *testing.T) {
	args, _, err := configure(acpdriver.LaunchConfig{
		Model: "glm-5.2", Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--mode", "acp", "--model", "glm-5.2", "--yolo"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestConfigureAppendsSystemPromptWhenProvided(t *testing.T) {
	args, _, err := configure(acpdriver.LaunchConfig{SystemPrompt: "Follow AO worker rules."})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--mode", "acp", "--append-system-prompt", "Follow AO worker rules."}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestConfigureOmitsBlankSystemPrompt(t *testing.T) {
	args, _, err := configure(acpdriver.LaunchConfig{SystemPrompt: "   "})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--mode", "acp"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestKimchiPermissionModeMapsVocabulary(t *testing.T) {
	tests := map[ports.PermissionMode]string{
		ports.PermissionModeDefault:           "",
		ports.PermissionModeAcceptEdits:       "auto",
		ports.PermissionModeAuto:              "auto",
		ports.PermissionModeBypassPermissions: "yolo",
	}
	for permission, want := range tests {
		if got := kimchiPermissionMode(permission); got != want {
			t.Errorf("mode(%q) = %q, want %q", permission, got, want)
		}
	}
}

func TestSessionOptionsUseModelConfigOption(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); len(got) != 0 {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{Model: "glm-5.2"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "glm-5.2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}

func TestSessionOptionsIncludePermissionMode(t *testing.T) {
	got := sessionOptions(ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions})
	want := []acpdriver.SessionOption{{ID: "permissions-mode", Value: "yolo"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}

func TestSessionOptionsIncludeModelAndPermissionMode(t *testing.T) {
	got := sessionOptions(ports.ChatTurnSettings{
		Model: "glm-5.2", Approval: ports.PermissionModeAuto,
	})
	want := []acpdriver.SessionOption{
		{ID: "model", Value: "glm-5.2"},
		{ID: "permissions-mode", Value: "auto"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}
