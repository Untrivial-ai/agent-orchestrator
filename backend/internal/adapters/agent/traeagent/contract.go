// Package traeagent contains the pre-registration conformance gate for the
// ByteDance Trae Agent CLI. It intentionally does not implement or register an
// AO adapter until a released upstream artifact proves every required contract.
package traeagent

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// AuditedVersion is the version declared by the audited source snapshot.
	// Upstream has not published this version as a GitHub or PyPI release.
	AuditedVersion = "0.1.0"

	// AuditedSourceCommit and AuditedSourceTree bind the audit to immutable Git
	// objects even though upstream does not currently publish release artifacts.
	AuditedSourceCommit = "e839e559ac61bdd0e057c375dd1dee391fee797d"
	AuditedSourceTree   = "fceea1cae3ddf5fcc29649db47449c54e011844e"
)

var (
	ErrVersionMissing              = errors.New("trae-agent: version missing")
	ErrVersionInvalid              = errors.New("trae-agent: version is not a three-component semantic version")
	ErrVersionUnaudited            = errors.New("trae-agent: version has not been audited")
	ErrSourceCommitMissing         = errors.New("trae-agent: audited source commit missing")
	ErrSourceCommitMismatch        = errors.New("trae-agent: source commit does not match the audited snapshot")
	ErrSourceTreeMissing           = errors.New("trae-agent: audited source tree missing")
	ErrSourceTreeMismatch          = errors.New("trae-agent: source tree does not match the audited snapshot")
	ErrReleaseMissing              = errors.New("trae-agent: official release artifact missing")
	ErrArtifactFingerprintMissing  = errors.New("trae-agent: executable fingerprint not proven")
	ErrProfilePreservationMissing  = errors.New("trae-agent: user profile preservation not proven")
	ErrAuthStatusMissing           = errors.New("trae-agent: truthful local auth-status probe missing")
	ErrStandingInstructionsMissing = errors.New("trae-agent: isolated standing-instruction channel missing")
	ErrModelConfigMissing          = errors.New("trae-agent: model configuration contract missing")
	ErrHookIsolationMissing        = errors.New("trae-agent: additive isolated hook contract missing")
	ErrInitialPromptMissing        = errors.New("trae-agent: interactive initial-prompt contract missing")
	ErrPermissionModesMissing      = errors.New("trae-agent: permission-mode contract missing")
	ErrLifecycleEventsMissing      = errors.New("trae-agent: lifecycle event contract missing")
	ErrNativeSessionIDMissing      = errors.New("trae-agent: durable native session ID missing")
	ErrExactResumeMissing          = errors.New("trae-agent: exact resume contract missing")
	ErrBoundedCancellationMissing  = errors.New("trae-agent: bounded cancellation contract missing")
	ErrACPContractMissing          = errors.New("trae-agent: ACP initialize contract missing")
	ErrACPNewSessionMissing        = errors.New("trae-agent: ACP session/new contract missing")
	ErrACPIdentityMissing          = errors.New("trae-agent: ACP stable session identity contract missing")
	ErrACPLoadMissing              = errors.New("trae-agent: ACP session/load contract missing")
	ErrACPMissingHistoryGuard      = errors.New("trae-agent: ACP missing-history error contract missing")
	ErrACPReplayMissing            = errors.New("trae-agent: ACP history replay contract missing")
	ErrACPStreamingMissing         = errors.New("trae-agent: ACP streaming contract missing")
	ErrACPPermissionsMissing       = errors.New("trae-agent: ACP permission contract missing")
	ErrACPCancelMissing            = errors.New("trae-agent: ACP cancellation contract missing")
	ErrACPRecoveryMissing          = errors.New("trae-agent: ACP restart recovery contract missing")
	ErrACPWorkspaceGuardMissing    = errors.New("trae-agent: ACP workspace mismatch contract missing")
	ErrACPCapabilityClaimsMissing  = errors.New("trae-agent: ACP capability-reporting contract missing")
)

// Contract records guarantees that must be demonstrated by one pinned,
// officially released Trae Agent artifact. Zero values fail closed.
//
// Source commit/tree identity is necessary source provenance, but it is not a
// substitute for an upstream release artifact or executable fingerprint.
// Likewise, command-line flags prove only syntax, never the behavior behind a
// prompt, permission, restore, cancellation, or ACP contract.
type Contract struct {
	Version                  string
	SourceCommit             string
	SourceTree               string
	OfficialRelease          bool
	ArtifactFingerprint      bool
	PreservesUserProfile     bool
	AuthStatus               bool
	StandingInstructions     bool
	ModelConfiguration       bool
	AdditiveHooks            bool
	InteractiveInitialPrompt bool
	PermissionModes          bool
	LifecycleEvents          bool
	NativeSessionID          bool
	ExactResume              bool
	BoundedCancellation      bool
	ACP                      bool
	ACPNewSession            bool
	ACPStableSessionID       bool
	ACPLoad                  bool
	ACPMissingHistory        bool
	ACPReplay                bool
	ACPStreaming             bool
	ACPPermissions           bool
	ACPCancel                bool
	ACPRecovery              bool
	ACPWorkspaceMismatch     bool
	ACPCapabilityClaims      bool
}

// AuditedSourceContract returns the positive facts established by source
// inspection. The false fields are deliberate blockers, not unknown defaults.
func AuditedSourceContract() Contract {
	return Contract{
		Version:            AuditedVersion,
		SourceCommit:       AuditedSourceCommit,
		SourceTree:         AuditedSourceTree,
		ModelConfiguration: true,
	}
}

// ValidateTUIContract rejects registration until the complete terminal-agent
// contract is independently proven for the pinned release.
func ValidateTUIContract(contract Contract) error {
	failures := commonFailures(contract)
	if !contract.AdditiveHooks {
		failures = append(failures, ErrHookIsolationMissing)
	}
	if !contract.InteractiveInitialPrompt {
		failures = append(failures, ErrInitialPromptMissing)
	}
	if !contract.PermissionModes {
		failures = append(failures, ErrPermissionModesMissing)
	}
	if !contract.LifecycleEvents {
		failures = append(failures, ErrLifecycleEventsMissing)
	}
	if !contract.NativeSessionID {
		failures = append(failures, ErrNativeSessionIDMissing)
	}
	if !contract.ExactResume {
		failures = append(failures, ErrExactResumeMissing)
	}
	if !contract.BoundedCancellation {
		failures = append(failures, ErrBoundedCancellationMissing)
	}
	return errors.Join(failures...)
}

// ValidateACPContract is independent from the TUI gate. MCP client support in
// Trae Agent does not satisfy any ACP server contract.
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
	if !contract.ACPCapabilityClaims {
		failures = append(failures, ErrACPCapabilityClaimsMissing)
	}
	return errors.Join(failures...)
}

// ValidateContract evaluates both independently gated interfaces.
func ValidateContract(contract Contract) error {
	return errors.Join(ValidateTUIContract(contract), ValidateACPContract(contract))
}

func commonFailures(contract Contract) []error {
	var failures []error
	version, ok := parseSemver(contract.Version)
	switch {
	case strings.TrimSpace(contract.Version) == "":
		failures = append(failures, ErrVersionMissing)
	case !ok:
		failures = append(failures, fmt.Errorf("%w: %q", ErrVersionInvalid, contract.Version))
	case version.String() != AuditedVersion:
		failures = append(failures, fmt.Errorf("%w: have %s, audited %s", ErrVersionUnaudited, version, AuditedVersion))
	}

	switch {
	case strings.TrimSpace(contract.SourceCommit) == "":
		failures = append(failures, ErrSourceCommitMissing)
	case contract.SourceCommit != AuditedSourceCommit:
		failures = append(failures, fmt.Errorf("%w: have %s, audited %s", ErrSourceCommitMismatch, contract.SourceCommit, AuditedSourceCommit))
	}
	switch {
	case strings.TrimSpace(contract.SourceTree) == "":
		failures = append(failures, ErrSourceTreeMissing)
	case contract.SourceTree != AuditedSourceTree:
		failures = append(failures, fmt.Errorf("%w: have %s, audited %s", ErrSourceTreeMismatch, contract.SourceTree, AuditedSourceTree))
	}

	if !contract.OfficialRelease {
		failures = append(failures, ErrReleaseMissing)
	}
	if !contract.ArtifactFingerprint {
		failures = append(failures, ErrArtifactFingerprintMissing)
	}
	if !contract.PreservesUserProfile {
		failures = append(failures, ErrProfilePreservationMissing)
	}
	if !contract.AuthStatus {
		failures = append(failures, ErrAuthStatusMissing)
	}
	if !contract.StandingInstructions {
		failures = append(failures, ErrStandingInstructionsMissing)
	}
	if !contract.ModelConfiguration {
		failures = append(failures, ErrModelConfigMissing)
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
	var version semver
	for index := range version {
		component, err := strconv.Atoi(match[index+1])
		if err != nil {
			return semver{}, false
		}
		version[index] = component
	}
	return version, true
}

func (version semver) String() string {
	return fmt.Sprintf("%d.%d.%d", version[0], version[1], version[2])
}
