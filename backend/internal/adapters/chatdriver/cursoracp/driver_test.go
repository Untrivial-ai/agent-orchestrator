package cursoracp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeExtensionBridge struct {
	input         ports.ChatInputRequest
	inputResponse ports.ChatInputResponse
	approval      acpdriver.ClientApprovalRequest
	selected      string
	plan          *domain.ConversationPlan
}

func (f *fakeExtensionBridge) RequestInput(
	_ context.Context,
	request ports.ChatInputRequest,
) (ports.ChatInputResponse, error) {
	f.input = request
	return f.inputResponse, nil
}

func (f *fakeExtensionBridge) RequestApproval(
	_ context.Context,
	request acpdriver.ClientApprovalRequest,
) (string, error) {
	f.approval = request
	return f.selected, nil
}

func (f *fakeExtensionBridge) UpdatePlan(plan *domain.ConversationPlan) { f.plan = plan }

func TestConfigureWritesManagedStandingRuleForStartAndResume(t *testing.T) {
	dataDir := t.TempDir()
	cfg := acpdriver.LaunchConfig{
		SessionID: "worker-1", DataDir: dataDir,
		SystemPrompt: "AO standing instructions for start",
	}
	args, env, err := configure(context.Background(), cfg)
	if err != nil {
		t.Fatalf("configure start: %v", err)
	}
	if len(args) != 3 || args[0] != "--plugin-dir" || args[2] != "acp" || !filepath.IsAbs(args[1]) {
		t.Fatalf("args = %#v, want --plugin-dir <absolute-path> acp", args)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
	rulePath := filepath.Join(args[1], "rules", "ao-standing.mdc")
	rule, err := os.ReadFile(rulePath)
	if err != nil {
		t.Fatalf("read managed rule: %v", err)
	}
	if !strings.Contains(string(rule), "alwaysApply: true") || !strings.Contains(string(rule), cfg.SystemPrompt) {
		t.Fatalf("managed rule = %q", rule)
	}
	if _, err := os.Stat(filepath.Join(args[1], ".cursor-plugin", "plugin.json")); err != nil {
		t.Fatalf("managed plugin manifest: %v", err)
	}

	// Resume re-runs Configure for the same AO session. It must replace the
	// standing rule with the newly generated role instead of retaining launch
	// context from the previous process.
	cfg.SystemPrompt = "AO standing instructions recomputed for resume"
	resumeArgs, _, err := configure(context.Background(), cfg)
	if err != nil {
		t.Fatalf("configure resume: %v", err)
	}
	if !reflect.DeepEqual(resumeArgs, args) {
		t.Fatalf("resume args = %#v, want stable plugin path %#v", resumeArgs, args)
	}
	rule, err = os.ReadFile(rulePath)
	if err != nil {
		t.Fatalf("read resumed rule: %v", err)
	}
	if !strings.Contains(string(rule), cfg.SystemPrompt) || strings.Contains(string(rule), "instructions for start") {
		t.Fatalf("resumed managed rule = %q", rule)
	}
}

func TestConfigureStopsStandingRuleWriteWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dataDir := t.TempDir()
	_, _, err := configure(ctx, acpdriver.LaunchConfig{
		SessionID: "cancelled", DataDir: dataDir, SystemPrompt: "must not be written",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("configure error = %v, want context.Canceled", err)
	}
}

func TestConfigureMapsEveryCursorPermissionMode(t *testing.T) {
	tests := []struct {
		name string
		mode ports.PermissionMode
		want []string
	}{
		{name: "default", mode: ports.PermissionModeDefault, want: []string{"acp"}},
		{name: "accept edits", mode: ports.PermissionModeAcceptEdits, want: []string{"acp"}},
		{name: "auto review", mode: ports.PermissionModeAuto, want: []string{"--auto-review", "acp"}},
		{name: "bypass", mode: ports.PermissionModeBypassPermissions, want: []string{"--force", "acp"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, env, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: tt.mode})
			if err != nil {
				t.Fatalf("configure: %v", err)
			}
			if !reflect.DeepEqual(args, tt.want) {
				t.Fatalf("args = %#v, want %#v", args, tt.want)
			}
			if env != nil {
				t.Fatalf("env = %#v, want nil", env)
			}
		})
	}
}

func TestCursorPermissionPolicyImplementsAdvertisedModes(t *testing.T) {
	edit := acpsdk.ToolKindEdit
	execute := acpsdk.ToolKindExecute
	options := []acpsdk.PermissionOption{
		{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
		{OptionId: "allow-always", Kind: acpsdk.PermissionOptionKindAllowAlways},
		{OptionId: "reject-once", Kind: acpsdk.PermissionOptionKindRejectOnce},
	}
	tests := []struct {
		name    string
		mode    ports.PermissionMode
		kind    *acpsdk.ToolKind
		wantID  acpsdk.PermissionOptionId
		handled bool
	}{
		{name: "default parks", mode: ports.PermissionModeDefault, kind: &edit},
		{name: "accept edits allows edit once", mode: ports.PermissionModeAcceptEdits, kind: &edit, wantID: "allow-once", handled: true},
		{name: "accept edits parks execute", mode: ports.PermissionModeAcceptEdits, kind: &execute},
		{name: "auto parks classifier escalation", mode: ports.PermissionModeAuto, kind: &execute},
		{name: "bypass prefers persistent allow", mode: ports.PermissionModeBypassPermissions, kind: &execute, wantID: "allow-always", handled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotHandled := permissionPolicy(tt.mode, acpsdk.RequestPermissionRequest{
				ToolCall: acpsdk.ToolCallUpdate{Kind: tt.kind}, Options: options,
			})
			if gotID != tt.wantID || gotHandled != tt.handled {
				t.Fatalf("selection = (%q, %v), want (%q, %v)", gotID, gotHandled, tt.wantID, tt.handled)
			}
		})
	}
}

func TestValidateTurnSettingsRejectsCursorProcessModeTransitions(t *testing.T) {
	tests := []struct {
		name    string
		initial ports.PermissionMode
		turn    ports.PermissionMode
		wantErr bool
	}{
		{name: "empty preserves auto launch", initial: ports.PermissionModeAuto},
		{name: "same auto is valid", initial: ports.PermissionModeAuto, turn: ports.PermissionModeAuto},
		{name: "default to accept edits is dynamic", initial: ports.PermissionModeDefault, turn: ports.PermissionModeAcceptEdits},
		{name: "accept edits to default is dynamic", initial: ports.PermissionModeAcceptEdits, turn: ports.PermissionModeDefault},
		{name: "default to auto needs restart", initial: ports.PermissionModeDefault, turn: ports.PermissionModeAuto, wantErr: true},
		{name: "auto to default needs restart", initial: ports.PermissionModeAuto, turn: ports.PermissionModeDefault, wantErr: true},
		{name: "default to bypass needs restart", initial: ports.PermissionModeDefault, turn: ports.PermissionModeBypassPermissions, wantErr: true},
		{name: "bypass to accept edits needs restart", initial: ports.PermissionModeBypassPermissions, turn: ports.PermissionModeAcceptEdits, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTurnSettings(test.initial, ports.ChatTurnSettings{Approval: test.turn})
			if (err != nil) != test.wantErr {
				t.Fatalf("validateTurnSettings(%q, %q) error = %v, wantErr %v",
					test.initial, test.turn, err, test.wantErr)
			}
		})
	}
}

func TestHandleAskQuestionUsesDurableInputFlow(t *testing.T) {
	bridge := &fakeExtensionBridge{inputResponse: ports.ChatInputResponse{
		Action: ports.ChatInputActionAccept,
		Content: map[string]any{
			"mode":    "agent",
			"targets": []any{"backend", "frontend"},
		},
	}}
	raw := json.RawMessage(`{
		"toolCallId":"call-1",
		"title":"Need input",
		"questions":[
			{"id":"mode","prompt":"Which mode?","options":[{"id":"agent","label":"Agent"},{"id":"plan","label":"Plan"}]},
			{"id":"targets","prompt":"Which targets?","allowMultiple":true,"options":[{"id":"backend","label":"Backend"},{"id":"frontend","label":"Frontend"}]}
		]
	}`)

	result, handled, err := handleExtension(context.Background(), bridge, "cursor/ask_question", raw)
	if err != nil {
		t.Fatalf("handleExtension: %v", err)
	}
	if !handled {
		t.Fatal("cursor/ask_question was not handled")
	}
	properties, _ := bridge.input.Schema["properties"].(map[string]any)
	mode, _ := properties["mode"].(map[string]any)
	targets, _ := properties["targets"].(map[string]any)
	if bridge.input.Mode != ports.ChatInputModeForm || bridge.input.Message != "Need input" ||
		mode["type"] != "string" || targets["type"] != "array" {
		t.Fatalf("input request = %#v", bridge.input)
	}
	encoded, _ := json.Marshal(result)
	var response struct {
		Outcome struct {
			Outcome string `json:"outcome"`
			Answers []struct {
				QuestionID        string   `json:"questionId"`
				SelectedOptionIDs []string `json:"selectedOptionIds"`
			} `json:"answers"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Outcome.Outcome != "answered" || len(response.Outcome.Answers) != 2 ||
		!reflect.DeepEqual(response.Outcome.Answers[0].SelectedOptionIDs, []string{"agent"}) ||
		!reflect.DeepEqual(response.Outcome.Answers[1].SelectedOptionIDs, []string{"backend", "frontend"}) {
		t.Fatalf("ask response = %s", encoded)
	}
}

func TestHandleCreatePlanPublishesPlanAndUsesDurableApprovalFlow(t *testing.T) {
	bridge := &fakeExtensionBridge{selected: "accept"}
	raw := json.RawMessage(`{
		"toolCallId":"call-2",
		"name":"Repair ACP",
		"overview":"Preserve lifecycle safety.",
		"plan":"1. Fix replay. 2. Verify.",
		"todos":[
			{"id":"one","content":"Fix replay","status":"completed"},
			{"id":"two","content":"Verify","status":"in_progress"}
		]
	}`)

	result, handled, err := handleExtension(context.Background(), bridge, "cursor/create_plan", raw)
	if err != nil {
		t.Fatalf("handleExtension: %v", err)
	}
	if !handled {
		t.Fatal("cursor/create_plan was not handled")
	}
	if bridge.plan == nil || len(bridge.plan.Steps) != 2 || bridge.plan.Steps[0].Status != domain.PlanStepCompleted ||
		bridge.plan.Steps[1].Status != domain.PlanStepInProgress {
		t.Fatalf("published plan = %#v", bridge.plan)
	}
	if bridge.approval.Summary != "Approve Cursor plan: Repair ACP" || len(bridge.approval.Decisions) != 2 {
		t.Fatalf("approval request = %#v", bridge.approval)
	}
	encoded, _ := json.Marshal(result)
	if string(encoded) != `{"outcome":{"outcome":"accepted"}}` {
		t.Fatalf("plan response = %s", encoded)
	}
}

func TestConfigureUsesNativeCursorACPWithoutStandingRule(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{
		Model: "gpt-5.5", Permissions: ports.PermissionModeDefault,
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
