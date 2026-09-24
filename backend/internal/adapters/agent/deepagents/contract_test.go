package deepagents

import (
	"errors"
	"testing"
)

func TestValidateTUIContractAcceptsPromptMechanismsWithoutACP(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Contract)
	}{
		{name: "system prompt file"},
		{
			name: "supported overlay",
			mutate: func(contract *Contract) {
				contract.SystemPromptFile = false
				contract.SupportedOverlay = true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := completeContract()
			contract.ACP = false
			contract.ACPNewSession = false
			contract.ACPStableSessionID = false
			contract.ACPLoad = false
			contract.ACPMissingHistory = false
			contract.ACPReplay = false
			contract.ACPStreaming = false
			contract.ACPPermissions = false
			contract.ACPCancel = false
			contract.ACPRecovery = false
			contract.ACPWorkspaceMismatch = false
			contract.ACPModelConfig = false
			contract.ACPCapabilityClaims = false
			if test.mutate != nil {
				test.mutate(&contract)
			}

			if err := ValidateTUIContract(contract); err != nil {
				t.Fatalf("ValidateTUIContract() error = %v", err)
			}
		})
	}
}

func TestValidateTUIContractRequirements(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Contract)
		wantErr error
	}{
		{
			name:    "missing version",
			mutate:  func(contract *Contract) { contract.Version = "" },
			wantErr: ErrVersionMissing,
		},
		{
			name:    "invalid version",
			mutate:  func(contract *Contract) { contract.Version = "development" },
			wantErr: ErrVersionInvalid,
		},
		{
			name:    "old version",
			mutate:  func(contract *Contract) { contract.Version = "0.1.71" },
			wantErr: ErrVersionTooOld,
		},
		{
			name:    "unaudited newer version",
			mutate:  func(contract *Contract) { contract.Version = "0.1.73" },
			wantErr: ErrVersionUnaudited,
		},
		{
			name:    "missing artifact fingerprint",
			mutate:  func(contract *Contract) { contract.ArtifactFingerprint = false },
			wantErr: ErrArtifactFingerprintMissing,
		},
		{
			name:    "profile preservation not proven",
			mutate:  func(contract *Contract) { contract.PreservesUserProfile = false },
			wantErr: ErrProfilePreservationMissing,
		},
		{
			name: "missing prompt mechanism",
			mutate: func(contract *Contract) {
				contract.SystemPromptFile = false
				contract.SupportedOverlay = false
			},
			wantErr: ErrSystemPromptContractMissing,
		},
		{
			name:    "missing additive isolated hooks",
			mutate:  func(contract *Contract) { contract.IsolatedHooks = false },
			wantErr: ErrHookIsolationMissing,
		},
		{
			name:    "missing initial message",
			mutate:  func(contract *Contract) { contract.InitialMessage = false },
			wantErr: ErrInitialMessageMissing,
		},
		{
			name:    "missing exact restore",
			mutate:  func(contract *Contract) { contract.ExactRestore = false },
			wantErr: ErrExactRestoreMissing,
		},
		{
			name:    "missing TUI session ID",
			mutate:  func(contract *Contract) { contract.TUISessionID = false },
			wantErr: ErrTUISessionIDMissing,
		},
		{
			name:    "missing truthful auth status",
			mutate:  func(contract *Contract) { contract.AuthStatus = false },
			wantErr: ErrAuthStatusMissing,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := completeContract()
			test.mutate(&contract)
			err := ValidateTUIContract(contract)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateTUIContract() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateTUIContractAcceptsVersionOutput(t *testing.T) {
	tests := []string{AuditedVersion, "deepagents-code 0.1.72", "deepagents-code v0.1.72"}
	for _, version := range tests {
		t.Run(version, func(t *testing.T) {
			contract := completeContract()
			contract.Version = version
			if err := ValidateTUIContract(contract); err != nil {
				t.Fatalf("ValidateTUIContract() error = %v", err)
			}
		})
	}
}

func TestValidateACPContractRequirements(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Contract)
		wantErr error
	}{
		{
			name:    "initialize support",
			mutate:  func(contract *Contract) { contract.ACP = false },
			wantErr: ErrACPContractMissing,
		},
		{
			name:    "new session",
			mutate:  func(contract *Contract) { contract.ACPNewSession = false },
			wantErr: ErrACPNewSessionMissing,
		},
		{
			name:    "stable session identity",
			mutate:  func(contract *Contract) { contract.ACPStableSessionID = false },
			wantErr: ErrACPIdentityMissing,
		},
		{
			name:    "load",
			mutate:  func(contract *Contract) { contract.ACPLoad = false },
			wantErr: ErrACPLoadMissing,
		},
		{
			name:    "missing history guard",
			mutate:  func(contract *Contract) { contract.ACPMissingHistory = false },
			wantErr: ErrACPMissingHistoryGuard,
		},
		{
			name:    "replay",
			mutate:  func(contract *Contract) { contract.ACPReplay = false },
			wantErr: ErrACPReplayMissing,
		},
		{
			name:    "streaming",
			mutate:  func(contract *Contract) { contract.ACPStreaming = false },
			wantErr: ErrACPStreamingMissing,
		},
		{
			name:    "permissions",
			mutate:  func(contract *Contract) { contract.ACPPermissions = false },
			wantErr: ErrACPPermissionsMissing,
		},
		{
			name:    "cancel",
			mutate:  func(contract *Contract) { contract.ACPCancel = false },
			wantErr: ErrACPCancelMissing,
		},
		{
			name:    "restart recovery",
			mutate:  func(contract *Contract) { contract.ACPRecovery = false },
			wantErr: ErrACPRecoveryMissing,
		},
		{
			name:    "workspace mismatch",
			mutate:  func(contract *Contract) { contract.ACPWorkspaceMismatch = false },
			wantErr: ErrACPWorkspaceGuardMissing,
		},
		{
			name:    "model and config",
			mutate:  func(contract *Contract) { contract.ACPModelConfig = false },
			wantErr: ErrACPModelConfigMissing,
		},
		{
			name:    "capability claims",
			mutate:  func(contract *Contract) { contract.ACPCapabilityClaims = false },
			wantErr: ErrACPCapabilityClaimsMissing,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := completeContract()
			test.mutate(&contract)
			err := ValidateACPContract(contract)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateACPContract() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateACPContractDoesNotRequireTUI(t *testing.T) {
	contract := Contract{
		Version:              AuditedVersion,
		ArtifactFingerprint:  true,
		PreservesUserProfile: true,
		SystemPromptFile:     true,
		AuthStatus:           true,
		ACP:                  true,
		ACPNewSession:        true,
		ACPStableSessionID:   true,
		ACPLoad:              true,
		ACPMissingHistory:    true,
		ACPReplay:            true,
		ACPStreaming:         true,
		ACPPermissions:       true,
		ACPCancel:            true,
		ACPRecovery:          true,
		ACPWorkspaceMismatch: true,
		ACPModelConfig:       true,
		ACPCapabilityClaims:  true,
	}

	if err := ValidateACPContract(contract); err != nil {
		t.Fatalf("ValidateACPContract() error = %v", err)
	}
}

func TestValidateACPContractRequiresCommonSafetyContract(t *testing.T) {
	contract := completeContract()
	contract.SystemPromptFile = false
	contract.PreservesUserProfile = false
	contract.AuthStatus = false

	err := ValidateACPContract(contract)
	for _, want := range []error{ErrSystemPromptContractMissing, ErrProfilePreservationMissing, ErrAuthStatusMissing} {
		if !errors.Is(err, want) {
			t.Errorf("ValidateACPContract() error = %v, want errors.Is(_, %v)", err, want)
		}
	}
}

func TestValidateContractComposesTUIAndACPFailures(t *testing.T) {
	err := ValidateContract(Contract{Version: MinimumVersion})
	wants := []error{
		ErrSystemPromptContractMissing,
		ErrHookIsolationMissing,
		ErrInitialMessageMissing,
		ErrExactRestoreMissing,
		ErrTUISessionIDMissing,
		ErrACPContractMissing,
		ErrACPNewSessionMissing,
		ErrACPIdentityMissing,
		ErrACPLoadMissing,
		ErrACPMissingHistoryGuard,
		ErrACPReplayMissing,
		ErrACPStreamingMissing,
		ErrACPPermissionsMissing,
		ErrACPCancelMissing,
		ErrACPRecoveryMissing,
		ErrACPWorkspaceGuardMissing,
		ErrACPModelConfigMissing,
		ErrACPCapabilityClaimsMissing,
		ErrAuthStatusMissing,
		ErrArtifactFingerprintMissing,
		ErrProfilePreservationMissing,
	}
	for _, want := range wants {
		if !errors.Is(err, want) {
			t.Errorf("ValidateContract() error = %v, want errors.Is(_, %v)", err, want)
		}
	}
}

func completeContract() Contract {
	return Contract{
		Version:              AuditedVersion,
		ArtifactFingerprint:  true,
		PreservesUserProfile: true,
		SystemPromptFile:     true,
		IsolatedHooks:        true,
		InitialMessage:       true,
		ExactRestore:         true,
		TUISessionID:         true,
		ACP:                  true,
		ACPNewSession:        true,
		ACPStableSessionID:   true,
		ACPLoad:              true,
		ACPMissingHistory:    true,
		ACPReplay:            true,
		ACPStreaming:         true,
		ACPPermissions:       true,
		ACPCancel:            true,
		ACPRecovery:          true,
		ACPWorkspaceMismatch: true,
		ACPModelConfig:       true,
		ACPCapabilityClaims:  true,
		AuthStatus:           true,
	}
}
