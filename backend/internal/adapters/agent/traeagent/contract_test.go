package traeagent

import (
	"errors"
	"testing"
)

func TestAuditedSourceContractFailsClosed(t *testing.T) {
	contract := AuditedSourceContract()

	tuiErr := ValidateTUIContract(contract)
	for _, want := range []error{
		ErrReleaseMissing,
		ErrArtifactFingerprintMissing,
		ErrProfilePreservationMissing,
		ErrAuthStatusMissing,
		ErrStandingInstructionsMissing,
		ErrHookIsolationMissing,
		ErrInitialPromptMissing,
		ErrPermissionModesMissing,
		ErrLifecycleEventsMissing,
		ErrNativeSessionIDMissing,
		ErrExactResumeMissing,
		ErrBoundedCancellationMissing,
	} {
		if !errors.Is(tuiErr, want) {
			t.Errorf("ValidateTUIContract(AuditedSourceContract()) = %v, want errors.Is(_, %v)", tuiErr, want)
		}
	}
	if errors.Is(tuiErr, ErrModelConfigMissing) {
		t.Errorf("source audit did prove model configuration syntax: %v", tuiErr)
	}

	acpErr := ValidateACPContract(contract)
	for _, want := range []error{
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
		ErrACPCapabilityClaimsMissing,
	} {
		if !errors.Is(acpErr, want) {
			t.Errorf("ValidateACPContract(AuditedSourceContract()) = %v, want errors.Is(_, %v)", acpErr, want)
		}
	}
}

func TestValidateTUIContractRequirements(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Contract)
		wantErr error
	}{
		{name: "missing version", mutate: func(c *Contract) { c.Version = "" }, wantErr: ErrVersionMissing},
		{name: "invalid version", mutate: func(c *Contract) { c.Version = "development" }, wantErr: ErrVersionInvalid},
		{name: "unaudited version", mutate: func(c *Contract) { c.Version = "0.1.1" }, wantErr: ErrVersionUnaudited},
		{name: "missing source commit", mutate: func(c *Contract) { c.SourceCommit = "" }, wantErr: ErrSourceCommitMissing},
		{name: "wrong source commit", mutate: func(c *Contract) { c.SourceCommit = "deadbeef" }, wantErr: ErrSourceCommitMismatch},
		{name: "missing source tree", mutate: func(c *Contract) { c.SourceTree = "" }, wantErr: ErrSourceTreeMissing},
		{name: "wrong source tree", mutate: func(c *Contract) { c.SourceTree = "deadbeef" }, wantErr: ErrSourceTreeMismatch},
		{name: "missing release", mutate: func(c *Contract) { c.OfficialRelease = false }, wantErr: ErrReleaseMissing},
		{name: "missing fingerprint", mutate: func(c *Contract) { c.ArtifactFingerprint = false }, wantErr: ErrArtifactFingerprintMissing},
		{name: "profile preservation", mutate: func(c *Contract) { c.PreservesUserProfile = false }, wantErr: ErrProfilePreservationMissing},
		{name: "auth status", mutate: func(c *Contract) { c.AuthStatus = false }, wantErr: ErrAuthStatusMissing},
		{name: "standing instructions", mutate: func(c *Contract) { c.StandingInstructions = false }, wantErr: ErrStandingInstructionsMissing},
		{name: "model config", mutate: func(c *Contract) { c.ModelConfiguration = false }, wantErr: ErrModelConfigMissing},
		{name: "hooks", mutate: func(c *Contract) { c.AdditiveHooks = false }, wantErr: ErrHookIsolationMissing},
		{name: "initial prompt", mutate: func(c *Contract) { c.InteractiveInitialPrompt = false }, wantErr: ErrInitialPromptMissing},
		{name: "permissions", mutate: func(c *Contract) { c.PermissionModes = false }, wantErr: ErrPermissionModesMissing},
		{name: "lifecycle", mutate: func(c *Contract) { c.LifecycleEvents = false }, wantErr: ErrLifecycleEventsMissing},
		{name: "session id", mutate: func(c *Contract) { c.NativeSessionID = false }, wantErr: ErrNativeSessionIDMissing},
		{name: "exact resume", mutate: func(c *Contract) { c.ExactResume = false }, wantErr: ErrExactResumeMissing},
		{name: "bounded cancellation", mutate: func(c *Contract) { c.BoundedCancellation = false }, wantErr: ErrBoundedCancellationMissing},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := completeContract()
			test.mutate(&contract)
			if err := ValidateTUIContract(contract); !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateTUIContract() = %v, want errors.Is(_, %v)", err, test.wantErr)
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
		{name: "initialize", mutate: func(c *Contract) { c.ACP = false }, wantErr: ErrACPContractMissing},
		{name: "new session", mutate: func(c *Contract) { c.ACPNewSession = false }, wantErr: ErrACPNewSessionMissing},
		{name: "identity", mutate: func(c *Contract) { c.ACPStableSessionID = false }, wantErr: ErrACPIdentityMissing},
		{name: "load", mutate: func(c *Contract) { c.ACPLoad = false }, wantErr: ErrACPLoadMissing},
		{name: "missing history", mutate: func(c *Contract) { c.ACPMissingHistory = false }, wantErr: ErrACPMissingHistoryGuard},
		{name: "replay", mutate: func(c *Contract) { c.ACPReplay = false }, wantErr: ErrACPReplayMissing},
		{name: "streaming", mutate: func(c *Contract) { c.ACPStreaming = false }, wantErr: ErrACPStreamingMissing},
		{name: "permissions", mutate: func(c *Contract) { c.ACPPermissions = false }, wantErr: ErrACPPermissionsMissing},
		{name: "cancel", mutate: func(c *Contract) { c.ACPCancel = false }, wantErr: ErrACPCancelMissing},
		{name: "recovery", mutate: func(c *Contract) { c.ACPRecovery = false }, wantErr: ErrACPRecoveryMissing},
		{name: "workspace guard", mutate: func(c *Contract) { c.ACPWorkspaceMismatch = false }, wantErr: ErrACPWorkspaceGuardMissing},
		{name: "capability claims", mutate: func(c *Contract) { c.ACPCapabilityClaims = false }, wantErr: ErrACPCapabilityClaimsMissing},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := completeContract()
			test.mutate(&contract)
			if err := ValidateACPContract(contract); !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateACPContract() = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
		})
	}
}

func TestCompleteContractsPassIndependently(t *testing.T) {
	contract := completeContract()
	if err := ValidateTUIContract(contract); err != nil {
		t.Fatalf("ValidateTUIContract() = %v", err)
	}
	if err := ValidateACPContract(contract); err != nil {
		t.Fatalf("ValidateACPContract() = %v", err)
	}
	if err := ValidateContract(contract); err != nil {
		t.Fatalf("ValidateContract() = %v", err)
	}
}

func TestParseSemverRejectsPrereleaseAndExtraComponents(t *testing.T) {
	for _, value := range []string{"0.1.0rc1", "0.1.0-rc.1", "0.1.0.1"} {
		t.Run(value, func(t *testing.T) {
			if parsed, ok := parseSemver(value); ok {
				t.Fatalf("parseSemver(%q) = %s, want rejected", value, parsed)
			}
		})
	}
}

func completeContract() Contract {
	return Contract{
		Version:                  AuditedVersion,
		SourceCommit:             AuditedSourceCommit,
		SourceTree:               AuditedSourceTree,
		OfficialRelease:          true,
		ArtifactFingerprint:      true,
		PreservesUserProfile:     true,
		AuthStatus:               true,
		StandingInstructions:     true,
		ModelConfiguration:       true,
		AdditiveHooks:            true,
		InteractiveInitialPrompt: true,
		PermissionModes:          true,
		LifecycleEvents:          true,
		NativeSessionID:          true,
		ExactResume:              true,
		BoundedCancellation:      true,
		ACP:                      true,
		ACPNewSession:            true,
		ACPStableSessionID:       true,
		ACPLoad:                  true,
		ACPMissingHistory:        true,
		ACPReplay:                true,
		ACPStreaming:             true,
		ACPPermissions:           true,
		ACPCancel:                true,
		ACPRecovery:              true,
		ACPWorkspaceMismatch:     true,
		ACPCapabilityClaims:      true,
	}
}
