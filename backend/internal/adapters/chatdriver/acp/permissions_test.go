package acp

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func offered(kinds ...acpsdk.PermissionOptionKind) []acpsdk.PermissionOption {
	options := make([]acpsdk.PermissionOption, 0, len(kinds))
	for _, kind := range kinds {
		options = append(options, acpsdk.PermissionOption{
			OptionId: acpsdk.PermissionOptionId(kind), Kind: kind,
		})
	}
	return options
}

func request(kind *acpsdk.ToolKind, options []acpsdk.PermissionOption) acpsdk.RequestPermissionRequest {
	return acpsdk.RequestPermissionRequest{
		ToolCall: acpsdk.ToolCallUpdate{Kind: kind}, Options: options,
	}
}

// The three shapes in use: a provider that settles auto and bypass at launch
// keeps them out of the policy (Cline), one that offers no persistent auto mode
// blanket-allows only bypass (Auggie, Cursor), and one that resolves everything
// over ACP blanket-allows both (Kiro, Autohand).
func TestStandardPermissionPolicyResolvesPerBindingModes(t *testing.T) {
	edit, execute := acpsdk.ToolKindEdit, acpsdk.ToolKindExecute
	all := offered(
		acpsdk.PermissionOptionKindAllowOnce,
		acpsdk.PermissionOptionKindAllowAlways,
		acpsdk.PermissionOptionKindRejectOnce,
	)
	tests := []struct {
		name         string
		blanketAllow []ports.PermissionMode
		mode         ports.PermissionMode
		kind         *acpsdk.ToolKind
		wantID       acpsdk.PermissionOptionId
		wantHandled  bool
	}{
		{name: "default always parks", mode: ports.PermissionModeDefault, kind: &edit},
		{name: "accept edits allows an edit once", mode: ports.PermissionModeAcceptEdits, kind: &edit,
			wantID: "allow_once", wantHandled: true},
		{name: "accept edits parks a command", mode: ports.PermissionModeAcceptEdits, kind: &execute},
		// Vibe 2.25.5 sends no kind, so this is its whole accept-edits behavior.
		{name: "accept edits parks an unkinded call", mode: ports.PermissionModeAcceptEdits, kind: nil},
		{name: "launch-settled auto parks", mode: ports.PermissionModeAuto, kind: &execute},
		{name: "bypass-only binding parks auto",
			blanketAllow: []ports.PermissionMode{ports.PermissionModeBypassPermissions},
			mode:         ports.PermissionModeAuto, kind: &execute},
		{name: "bypass-only binding allows bypass persistently",
			blanketAllow: []ports.PermissionMode{ports.PermissionModeBypassPermissions},
			mode:         ports.PermissionModeBypassPermissions, kind: &execute,
			wantID: "allow_always", wantHandled: true},
		{name: "full binding allows auto persistently",
			blanketAllow: []ports.PermissionMode{ports.PermissionModeAuto, ports.PermissionModeBypassPermissions},
			mode:         ports.PermissionModeAuto, kind: &execute,
			wantID: "allow_always", wantHandled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, handled := StandardPermissionPolicy(tt.blanketAllow...)(tt.mode, request(tt.kind, all))
			if id != tt.wantID || handled != tt.wantHandled {
				t.Fatalf("selection = (%q, %v), want (%q, %v)", id, handled, tt.wantID, tt.wantHandled)
			}
		})
	}
}

// A provider that offers no persistent choice must still be auto-answered,
// otherwise a bypass session parks on every tool call.
func TestStandardPermissionPolicyFallsBackToAllowOnce(t *testing.T) {
	execute := acpsdk.ToolKindExecute
	policy := StandardPermissionPolicy(ports.PermissionModeBypassPermissions)
	id, handled := policy(ports.PermissionModeBypassPermissions,
		request(&execute, offered(acpsdk.PermissionOptionKindAllowOnce)))
	if !handled || id != "allow_once" {
		t.Fatalf("selection = (%q, %v), want (allow_once, true)", id, handled)
	}
}

func TestPermissionOptionReportsMissingKind(t *testing.T) {
	options := offered(acpsdk.PermissionOptionKindRejectOnce)
	if id, ok := PermissionOption(options, acpsdk.PermissionOptionKindAllowAlways); ok {
		t.Fatalf("selection = %q, want no match", id)
	}
	if id, ok := PermissionOption(nil, acpsdk.PermissionOptionKindAllowOnce); ok {
		t.Fatalf("selection from no options = %q, want no match", id)
	}
}

func TestApprovalFixedAtLaunchRejectsOnlyRealChanges(t *testing.T) {
	validate := ApprovalFixedAtLaunch("Kilo Code ACP permission mode", nil)
	if err := validate(ports.PermissionModeDefault, ports.ChatTurnSettings{}); err != nil {
		t.Fatalf("unset approval: %v", err)
	}
	if err := validate(ports.PermissionModeAuto,
		ports.ChatTurnSettings{Approval: ports.PermissionModeAuto}); err != nil {
		t.Fatalf("unchanged approval: %v", err)
	}
	err := validate(ports.PermissionModeDefault, ports.ChatTurnSettings{Approval: ports.PermissionModeAuto})
	if !errors.Is(err, ErrACPSetterUnsupported) {
		t.Fatalf("err = %v, want ErrACPSetterUnsupported", err)
	}
	if !strings.Contains(err.Error(), "Kilo Code ACP permission mode") {
		t.Fatalf("err = %q, want the binding's surface named", err)
	}
}

// Goose launches auto and bypass as the same GOOSE_MODE, so switching between
// them needs no restart even though the AO modes differ.
func TestApprovalFixedAtLaunchAcceptsModesThatLaunchedIdentically(t *testing.T) {
	sameProcess := func(mode ports.PermissionMode) string {
		switch ports.NormalizePermissionMode(mode) {
		case ports.PermissionModeAuto, ports.PermissionModeBypassPermissions:
			return "auto"
		default:
			return ""
		}
	}
	validate := ApprovalFixedAtLaunch("Goose ACP approval mode", sameProcess)
	if err := validate(ports.PermissionModeAuto,
		ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions}); err != nil {
		t.Fatalf("auto -> bypass launches the same process, so it must be accepted: %v", err)
	}
	if err := validate(ports.PermissionModeAuto,
		ports.ChatTurnSettings{Approval: ports.PermissionModeDefault}); !errors.Is(err, ErrACPSetterUnsupported) {
		t.Fatalf("err = %v, want ErrACPSetterUnsupported for a mode that launches differently", err)
	}
}

func TestModelOptionSelectsAdvertisedModel(t *testing.T) {
	if got := ModelOption(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("no model = %#v, want nil so the session keeps the provider default", got)
	}
	if got := ModelOption(ports.ChatTurnSettings{Model: "  "}); got != nil {
		t.Fatalf("blank model = %#v, want nil", got)
	}
	got := ModelOption(ports.ChatTurnSettings{Model: "anthropic/claude-sonnet-4"})
	want := []SessionOption{{ID: "model", Value: "anthropic/claude-sonnet-4"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}
