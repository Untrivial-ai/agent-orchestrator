package acp

import (
	"errors"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func selectOption(id, name, current string, values ...string) acpsdk.SessionConfigOption {
	if len(values) == 0 {
		values = []string{current}
	}
	ungrouped := make(acpsdk.SessionConfigSelectOptionsUngrouped, 0, len(values))
	for _, value := range values {
		ungrouped = append(ungrouped, acpsdk.SessionConfigSelectOption{
			Value: acpsdk.SessionConfigValueId(value),
			Name:  value,
		})
	}
	return acpsdk.SessionConfigOption{
		Select: &acpsdk.SessionConfigOptionSelect{
			Id:           acpsdk.SessionConfigId(id),
			Name:         name,
			CurrentValue: acpsdk.SessionConfigValueId(current),
			Options:      acpsdk.SessionConfigSelectOptions{Ungrouped: &ungrouped},
		},
	}
}

func boolOption(id, name string, current bool) acpsdk.SessionConfigOption {
	return acpsdk.SessionConfigOption{
		Boolean: &acpsdk.SessionConfigOptionBoolean{
			Id:           acpsdk.SessionConfigId(id),
			Name:         name,
			CurrentValue: current,
		},
	}
}

// The session/update notification documents itself as a complete replacement
// "including removing an option", so an empty catalog from that channel is a
// real statement about the session and must apply verbatim. Swallowing it would
// leave a picker offering options the agent has withdrawn.
func TestReplaceConfigOptionsAppliesEmptyCatalogVerbatim(t *testing.T) {
	c := &conversation{capabilities: make(ports.ChatCapabilities)}
	c.replaceConfigOptions([]acpsdk.SessionConfigOption{selectOption("model", "Model", "sonnet")})
	if got := len(c.configOptions); got != 1 {
		t.Fatalf("seed catalog: got %d options, want 1", got)
	}

	c.replaceConfigOptions(nil)

	if got := len(c.configOptions); got != 0 {
		t.Fatalf("authoritative empty replacement was ignored: got %d options, want 0", got)
	}
}

// A non-empty replacement is authoritative too: switching models can add,
// change, or remove the other controls, so the new catalog replaces the old one
// wholesale rather than merging into it.
func TestReplaceConfigOptionsReplacesWholesale(t *testing.T) {
	c := &conversation{capabilities: make(ports.ChatCapabilities)}
	c.replaceConfigOptions([]acpsdk.SessionConfigOption{
		selectOption("model", "Model", "sonnet"),
		selectOption("effort", "Effort", "high"),
	})

	c.replaceConfigOptions([]acpsdk.SessionConfigOption{selectOption("model", "Model", "opus")})

	if got := len(c.configOptions); got != 1 {
		t.Fatalf("got %d options, want 1 — a non-empty update is a full replacement", got)
	}
	if got := c.configOptions[0].Current.Select; got != "opus" {
		t.Fatalf("current value not updated: got %q, want %q", got, "opus")
	}
	if !c.capabilities[ports.ChatCapabilityConfigOptions] {
		t.Fatal("config-options capability should be set by a non-empty catalog")
	}
}

// The bug this guards: an agent accepts session/set_config_option but answers
// without the rebuilt catalog. Wiping made the picker vanish; returning the
// pre-change catalog would show the old value for a change the agent already
// applied. Neither is acceptable — record the accepted value and keep the rest.
func TestApplyAcceptedConfigOptionRecordsSelectWithoutLosingCatalog(t *testing.T) {
	c := &conversation{capabilities: make(ports.ChatCapabilities)}
	c.replaceConfigOptions([]acpsdk.SessionConfigOption{
		selectOption("model", "Model", "sonnet", "sonnet", "opus"),
		selectOption("effort", "Effort", "high", "high", "low"),
	})

	c.applyAcceptedConfigOption("model", ports.ChatConfigOptionValue{Select: "opus"})

	if got := len(c.configOptions); got != 2 {
		t.Fatalf("catalog lost entries: got %d options, want 2", got)
	}
	if got := c.configOptions[0].Current.Select; got != "opus" {
		t.Fatalf("accepted value not recorded: got %q, want %q", got, "opus")
	}
	if got := c.configOptions[1].Current.Select; got != "high" {
		t.Fatalf("unrelated option was disturbed: got %q, want %q", got, "high")
	}
	if got := len(c.configOptions[0].Choices); got != 2 {
		t.Fatalf("choices dropped: got %d, want 2", got)
	}
}

func TestApplyAcceptedConfigOptionRecordsBoolean(t *testing.T) {
	c := &conversation{capabilities: make(ports.ChatCapabilities)}
	c.replaceConfigOptions([]acpsdk.SessionConfigOption{boolOption("fast", "Fast mode", false)})

	c.applyAcceptedConfigOption("fast", ports.ChatConfigOptionValue{Boolean: boolPtr(true)})

	current := c.configOptions[0].Current.Boolean
	if current == nil || !*current {
		t.Fatalf("accepted boolean not recorded: got %v, want true", current)
	}
}

// An id with no matching entry must leave the catalog untouched rather than
// inventing a row for an option the session never advertised.
func TestApplyAcceptedConfigOptionIgnoresUnknownID(t *testing.T) {
	c := &conversation{capabilities: make(ports.ChatCapabilities)}
	c.replaceConfigOptions([]acpsdk.SessionConfigOption{selectOption("model", "Model", "sonnet")})

	c.applyAcceptedConfigOption("nope", ports.ChatConfigOptionValue{Select: "whatever"})

	if got := len(c.configOptions); got != 1 {
		t.Fatalf("got %d options, want 1", got)
	}
	if got := c.configOptions[0].Current.Select; got != "sonnet" {
		t.Fatalf("catalog mutated by unknown id: got %q, want %q", got, "sonnet")
	}
}

func boolPtr(v bool) *bool { return &v }

// TestModeOfferedChecksLiveCatalogNotStaticMapping guards the fix for a real
// incident: Claude Code only advertises the "auto" permission mode when the
// current model's SDK reports classifier support (e.g. Haiku does not), so a
// static AO-vocabulary mapping (permission "auto" -> ACP mode id "auto") can
// name a mode the live session never offered.
func TestModeOfferedChecksLiveCatalogNotStaticMapping(t *testing.T) {
	options := normalizeSessionOptions(nil, nil, &acpsdk.SessionModeState{
		CurrentModeId: "default",
		AvailableModes: []acpsdk.SessionMode{
			{Id: "default", Name: "Manual"},
			{Id: "acceptEdits", Name: "Accept Edits"},
		},
	})

	if modeOffered(options, "auto") {
		t.Fatal("modeOffered(auto) = true, want false — auto is not in AvailableModes")
	}
	if !modeOffered(options, "acceptEdits") {
		t.Fatal("modeOffered(acceptEdits) = false, want true — acceptEdits is in AvailableModes")
	}
}

// TestApplyTurnSettingsToleratesModeUnavailableOnThisModel verifies that
// applyTurnSettings does not call session/set_mode with a mode the agent's
// live catalog does not offer. Calling it anyway trips the agent's own hard
// validation (session.modes.availableModes.some(...) throw), which surfaced
// as a fatal spawn failure instead of the graceful degrade every other
// unsupported-setter path already gets.
func TestApplyTurnSettingsToleratesModeUnavailableOnThisModel(t *testing.T) {
	c := &conversation{
		sessionID:    "sess-1",
		capabilities: make(ports.ChatCapabilities),
		modeFor:      func(ports.PermissionMode) string { return "auto" },
		legacyMode:   true,
		configOptions: normalizeSessionOptions(nil, nil, &acpsdk.SessionModeState{
			CurrentModeId: "default",
			AvailableModes: []acpsdk.SessionMode{
				{Id: "default", Name: "Manual"},
				{Id: "acceptEdits", Name: "Accept Edits"},
			},
		}),
	}

	err := c.applyTurnSettings(t.Context(), ports.ChatTurnSettings{Approval: ports.PermissionModeAuto})
	if !errors.Is(err, ErrACPSetterUnsupported) {
		t.Fatalf("applyTurnSettings with unavailable mode: err = %v, want ErrACPSetterUnsupported", err)
	}
}

// TestApplyTurnSettingsSkipsModeReapplicationWhenUnchanged guards a real
// incident: Start tolerates ErrACPSetterUnsupported for the initial mode
// (the mode may already have reached the agent via launch-time flags or the
// ACP Initialize meta), but SendTurn does not — it must surface a genuine
// runtime mode *change* the agent refuses. Without this guard, the very
// first SendTurn re-sends the session's own unchanged initial settings,
// hits the same unavailable-mode condition Start already tolerated, and
// fails the user's first message instead of just running in whatever mode
// the agent actually started in.
func TestApplyTurnSettingsSkipsModeReapplicationWhenUnchanged(t *testing.T) {
	c := &conversation{
		sessionID:         "sess-1",
		capabilities:      make(ports.ChatCapabilities),
		modeFor:           func(ports.PermissionMode) string { return "auto" },
		legacyMode:        true,
		initialPermission: ports.PermissionModeAuto,
		permissionMode:    ports.PermissionModeAuto, // set by start(), as if Start() already ran
		configOptions: normalizeSessionOptions(nil, nil, &acpsdk.SessionModeState{
			CurrentModeId: "default",
			AvailableModes: []acpsdk.SessionMode{
				{Id: "default", Name: "Manual"},
				{Id: "acceptEdits", Name: "Accept Edits"},
			},
		}),
	}

	// Re-sending the same initial Approval on the first real turn must not
	// re-trigger the mode-unavailable failure Start() already tolerated.
	err := c.applyTurnSettings(t.Context(), ports.ChatTurnSettings{Approval: ports.PermissionModeAuto})
	if err != nil {
		t.Fatalf("applyTurnSettings reapplying unchanged initial mode: err = %v, want nil", err)
	}
}
