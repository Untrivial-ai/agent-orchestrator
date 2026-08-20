package cursoracp

import (
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureUsesNativeCursorACP(t *testing.T) {
	args, env, err := configure(acpdriver.LaunchConfig{
		Model: "gpt-5.5", Permissions: ports.PermissionModeBypassPermissions,
		SystemPrompt: "AO standing instructions",
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if want := []string{"acp"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
}

func TestSessionOptionsMapOnlyAdvertisedModel(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{
		Model:    "gpt-5.5[context=272k,reasoning=medium,fast=false]",
		Approval: ports.PermissionModeBypassPermissions,
		Effort:   "high",
	})
	want := []acpdriver.SessionOption{{
		ID: "model", Value: "gpt-5.5[context=272k,reasoning=medium,fast=false]",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want only provider-advertised model option %#v", got, want)
	}
}
