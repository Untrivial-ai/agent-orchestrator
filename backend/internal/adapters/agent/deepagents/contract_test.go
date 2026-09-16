package deepagents

import (
	"errors"
	"testing"
)

func TestValidateContract(t *testing.T) {
	complete := Contract{
		Version: "0.1.70", SystemPromptFile: true, IsolatedHooks: true,
		InitialMessage: true, ExactRestore: true, TUISessionID: true,
		ACP: true, ACPLoad: true, ACPReplay: true, ACPPermissions: true,
		ACPCancel: true, AuthStatus: true,
	}

	tests := []struct {
		name string
		edit func(*Contract)
		want error
	}{
		{name: "complete"},
		{name: "replacement home", edit: func(c *Contract) { c.UsesReplacementHome = true }, want: ErrProfileIsolationUnsafe},
		{name: "old version", edit: func(c *Contract) { c.Version = "0.1.69" }, want: ErrVersionUnsupported},
		{name: "invalid version", edit: func(c *Contract) { c.Version = "dev" }, want: ErrVersionUnsupported},
		{name: "missing prompt channel", edit: func(c *Contract) { c.SystemPromptFile = false }, want: ErrSystemPromptContractMissing},
		{name: "supported overlay", edit: func(c *Contract) { c.SystemPromptFile = false; c.SupportedOverlay = true }},
		{name: "missing isolated hooks", edit: func(c *Contract) { c.IsolatedHooks = false }, want: ErrHookIsolationMissing},
		{name: "missing initial message", edit: func(c *Contract) { c.InitialMessage = false }, want: ErrTUIContractMissing},
		{name: "missing exact restore", edit: func(c *Contract) { c.ExactRestore = false }, want: ErrTUIContractMissing},
		{name: "missing native id", edit: func(c *Contract) { c.TUISessionID = false }, want: ErrTUIContractMissing},
		{name: "missing ACP", edit: func(c *Contract) { c.ACP = false }, want: ErrACPContractMissing},
		{name: "missing ACP load", edit: func(c *Contract) { c.ACPLoad = false }, want: ErrACPContractMissing},
		{name: "missing ACP replay", edit: func(c *Contract) { c.ACPReplay = false }, want: ErrACPContractMissing},
		{name: "missing ACP permissions", edit: func(c *Contract) { c.ACPPermissions = false }, want: ErrACPContractMissing},
		{name: "missing ACP cancel", edit: func(c *Contract) { c.ACPCancel = false }, want: ErrACPContractMissing},
		{name: "missing auth status", edit: func(c *Contract) { c.AuthStatus = false }, want: ErrAuthStatusMissing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := complete
			if tt.edit != nil {
				tt.edit(&contract)
			}
			err := ValidateContract(contract)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateContract() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestMinimumVersionIsAtLeastPlanFloor(t *testing.T) {
	got, ok := parseSemver(MinimumVersion)
	if !ok {
		t.Fatalf("MinimumVersion %q is not semantic version", MinimumVersion)
	}
	floor, _ := parseSemver("0.1.70")
	if got.less(floor) {
		t.Fatalf("MinimumVersion = %s, want >= 0.1.70", got)
	}
}
