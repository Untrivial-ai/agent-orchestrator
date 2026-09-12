package acp

import (
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
