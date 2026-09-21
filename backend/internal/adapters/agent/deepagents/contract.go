// Package deepagents contains the pre-registration conformance gate for the
// DeepAgents Code CLI. It intentionally does not implement or register an AO
// agent adapter until the upstream contracts represented here are proven.
package deepagents

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// AuditedVersion is the exact DeepAgents Code release represented by this
// conformance gate. A newer release must be audited and update this constant;
// version ordering alone is not proof that its contracts are unchanged.
const AuditedVersion = "0.1.72"

// MinimumVersion is retained as the documented candidate floor. The current
// gate additionally requires an exact AuditedVersion match.
const MinimumVersion = AuditedVersion

// Conformance errors identify each independently missing proof so registration
// callers can inspect a joined gate result with errors.Is.
var (
	ErrVersionMissing              = errors.New("deepagents: version missing")
	ErrVersionInvalid              = errors.New("deepagents: version is not a three-component semantic version")
	ErrVersionTooOld               = errors.New("deepagents: version is older than the conformance floor")
	ErrVersionUnaudited            = errors.New("deepagents: version has not been audited")
	ErrArtifactFingerprintMissing  = errors.New("deepagents: executable fingerprint not proven")
	ErrProfilePreservationMissing  = errors.New("deepagents: user profile preservation not proven")
	ErrSystemPromptContractMissing = errors.New("deepagents: isolated system-prompt contract missing")
	ErrHookIsolationMissing        = errors.New("deepagents: additive isolated hook contract missing")
	ErrInitialMessageMissing       = errors.New("deepagents: interactive initial-message contract missing")
	ErrExactRestoreMissing         = errors.New("deepagents: exact restore contract missing")
	ErrTUISessionIDMissing         = errors.New("deepagents: TUI session-id contract missing")
	ErrACPContractMissing          = errors.New("deepagents: ACP contract missing")
	ErrACPNewSessionMissing        = errors.New("deepagents: ACP session/new contract missing")
	ErrACPIdentityMissing          = errors.New("deepagents: ACP stable session identity contract missing")
	ErrACPLoadMissing              = errors.New("deepagents: ACP session/load contract missing")
	ErrACPMissingHistoryGuard      = errors.New("deepagents: ACP missing-history error contract missing")
	ErrACPReplayMissing            = errors.New("deepagents: ACP history replay contract missing")
	ErrACPStreamingMissing         = errors.New("deepagents: ACP streaming contract missing")
	ErrACPPermissionsMissing       = errors.New("deepagents: ACP permission contract missing")
	ErrACPCancelMissing            = errors.New("deepagents: ACP cancellation contract missing")
	ErrACPRecoveryMissing          = errors.New("deepagents: ACP restart recovery contract missing")
	ErrACPWorkspaceGuardMissing    = errors.New("deepagents: ACP workspace mismatch contract missing")
	ErrACPModelConfigMissing       = errors.New("deepagents: ACP model/config contract missing")
	ErrACPCapabilityClaimsMissing  = errors.New("deepagents: ACP capability-reporting contract missing")
	ErrAuthStatusMissing           = errors.New("deepagents: local auth-status contract missing")
)

// Contract records capabilities that must be independently demonstrated by a
// pinned DeepAgents Code executable before AO may register the harness.
//
// SystemPromptFile is the preferred explicit-file mechanism. SupportedOverlay
// may be used instead only when the conformance record proves that it appends
// AO instructions while preserving the built-in prompt and user profile.
// ArtifactFingerprint and PreservesUserProfile are positive evidence fields;
// their zero values deliberately fail closed.
// IsolatedHooks means the AO hook input is process-local and additive: it does
// not replace or shadow user, project, or plugin hooks. AuthStatus means a
// local status probe truthfully distinguishes verified auth from mere secret
// presence.
type Contract struct {
	Version              string
	ArtifactFingerprint  bool
	PreservesUserProfile bool
	SystemPromptFile     bool
	SupportedOverlay     bool
	IsolatedHooks        bool
	InitialMessage       bool
	ExactRestore         bool
	TUISessionID         bool
	ACP                  bool
	ACPNewSession        bool
	ACPStableSessionID   bool
	ACPLoad              bool
	ACPMissingHistory    bool
	ACPReplay            bool
	ACPStreaming         bool
	ACPPermissions       bool
	ACPCancel            bool
	ACPRecovery          bool
	ACPWorkspaceMismatch bool
	ACPModelConfig       bool
	ACPCapabilityClaims  bool
	AuthStatus           bool
}

// ValidateTUIContract rejects any contract that would make AO replace a user's
// DeepAgents profile, or that lacks a required TUI, lifecycle, or auth
// guarantee. ACP capabilities are deliberately excluded so TUI registration
// can proceed independently after this gate passes.
//
// All missing guarantees are joined so callers can use errors.Is to inspect an
// individual failure without losing the complete gate result.
func ValidateTUIContract(contract Contract) error {
	failures := commonFailures(contract)
	if !contract.IsolatedHooks {
		failures = append(failures, ErrHookIsolationMissing)
	}
	if !contract.InitialMessage {
		failures = append(failures, ErrInitialMessageMissing)
	}
	if !contract.ExactRestore {
		failures = append(failures, ErrExactRestoreMissing)
	}
	if !contract.TUISessionID {
		failures = append(failures, ErrTUISessionIDMissing)
	}

	return errors.Join(failures...)
}

// ValidateACPContract rejects contracts that lack the independently proven ACP
// initialize, identity, load, replay, permission, cancellation, workspace, or
// restart-recovery guarantees required to register a structured Chat driver.
// TUI capabilities are deliberately excluded from this gate.
func ValidateACPContract(contract Contract) error {
	failures := commonFailures(contract)

	if !contract.ACP {
		failures = append(failures, ErrACPContractMissing)
	}
	if !contract.ACPNewSession {
		failures = append(failures, ErrACPNewSessionMissing)
	}
	if !contract.ACPStableSessionID {
		failures = append(failures, ErrACPIdentityMissing)
	}
	if !contract.ACPLoad {
		failures = append(failures, ErrACPLoadMissing)
	}
	if !contract.ACPMissingHistory {
		failures = append(failures, ErrACPMissingHistoryGuard)
	}
	if !contract.ACPReplay {
		failures = append(failures, ErrACPReplayMissing)
	}
	if !contract.ACPStreaming {
		failures = append(failures, ErrACPStreamingMissing)
	}
	if !contract.ACPPermissions {
		failures = append(failures, ErrACPPermissionsMissing)
	}
	if !contract.ACPCancel {
		failures = append(failures, ErrACPCancelMissing)
	}
	if !contract.ACPRecovery {
		failures = append(failures, ErrACPRecoveryMissing)
	}
	if !contract.ACPWorkspaceMismatch {
		failures = append(failures, ErrACPWorkspaceGuardMissing)
	}
	if !contract.ACPModelConfig {
		failures = append(failures, ErrACPModelConfigMissing)
	}
	if !contract.ACPCapabilityClaims {
		failures = append(failures, ErrACPCapabilityClaimsMissing)
	}

	return errors.Join(failures...)
}

// ValidateContract is the composed full-interface conformance gate. Callers
// deciding whether to register only one interface should use
// ValidateTUIContract or ValidateACPContract instead.
func ValidateContract(contract Contract) error {
	failures := commonFailures(contract)

	if !contract.IsolatedHooks {
		failures = append(failures, ErrHookIsolationMissing)
	}
	if !contract.InitialMessage {
		failures = append(failures, ErrInitialMessageMissing)
	}
	if !contract.ExactRestore {
		failures = append(failures, ErrExactRestoreMissing)
	}
	if !contract.TUISessionID {
		failures = append(failures, ErrTUISessionIDMissing)
	}
	if !contract.ACP {
		failures = append(failures, ErrACPContractMissing)
	}
	if !contract.ACPNewSession {
		failures = append(failures, ErrACPNewSessionMissing)
	}
	if !contract.ACPStableSessionID {
		failures = append(failures, ErrACPIdentityMissing)
	}
	if !contract.ACPLoad {
		failures = append(failures, ErrACPLoadMissing)
	}
	if !contract.ACPMissingHistory {
		failures = append(failures, ErrACPMissingHistoryGuard)
	}
	if !contract.ACPReplay {
		failures = append(failures, ErrACPReplayMissing)
	}
	if !contract.ACPStreaming {
		failures = append(failures, ErrACPStreamingMissing)
	}
	if !contract.ACPPermissions {
		failures = append(failures, ErrACPPermissionsMissing)
	}
	if !contract.ACPCancel {
		failures = append(failures, ErrACPCancelMissing)
	}
	if !contract.ACPRecovery {
		failures = append(failures, ErrACPRecoveryMissing)
	}
	if !contract.ACPWorkspaceMismatch {
		failures = append(failures, ErrACPWorkspaceGuardMissing)
	}
	if !contract.ACPModelConfig {
		failures = append(failures, ErrACPModelConfigMissing)
	}
	if !contract.ACPCapabilityClaims {
		failures = append(failures, ErrACPCapabilityClaimsMissing)
	}

	return errors.Join(failures...)
}

func commonFailures(contract Contract) []error {
	var failures []error

	if strings.TrimSpace(contract.Version) == "" {
		failures = append(failures, ErrVersionMissing)
	} else {
		installed, ok := parseSemver(contract.Version)
		if !ok {
			failures = append(failures, fmt.Errorf("%w: %q", ErrVersionInvalid, contract.Version))
		} else {
			minimum, _ := parseSemver(MinimumVersion)
			if installed.less(minimum) {
				failures = append(failures, fmt.Errorf("%w: have %s, require %s or newer", ErrVersionTooOld, installed, minimum))
			} else if installed != minimum {
				failures = append(failures, fmt.Errorf("%w: have %s, audited %s", ErrVersionUnaudited, installed, AuditedVersion))
			}
		}
	}

	if !contract.ArtifactFingerprint {
		failures = append(failures, ErrArtifactFingerprintMissing)
	}
	if !contract.PreservesUserProfile {
		failures = append(failures, ErrProfilePreservationMissing)
	}
	if !contract.SystemPromptFile && !contract.SupportedOverlay {
		failures = append(failures, ErrSystemPromptContractMissing)
	}
	if !contract.AuthStatus {
		failures = append(failures, ErrAuthStatusMissing)
	}

	return failures
}

var semverPattern = regexp.MustCompile(`(?:^|[^0-9A-Za-z.-])v?(\d+)\.(\d+)\.(\d+)(?:$|[^0-9A-Za-z.-])`)

type semver [3]int

func parseSemver(value string) (semver, bool) {
	match := semverPattern.FindStringSubmatch(value)
	if len(match) != 4 {
		return semver{}, false
	}

	var parsed semver
	for index := range parsed {
		component, err := strconv.Atoi(match[index+1])
		if err != nil {
			return semver{}, false
		}
		parsed[index] = component
	}
	return parsed, true
}

func (version semver) less(other semver) bool {
	for index := range version {
		if version[index] != other[index] {
			return version[index] < other[index]
		}
	}
	return false
}

func (version semver) String() string {
	return fmt.Sprintf("%d.%d.%d", version[0], version[1], version[2])
}
