// Package plandex records the upstream contract that must be proven before
// Plandex can be registered as an AO worker or orchestrator harness.
package plandex

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// ObservedVersion is the released Plandex CLI version whose source and help
	// surface were inspected for this contract.
	ObservedVersion = "2.2.1"
	// ObservedCommit is the commit referenced by the cli/v2.2.1 upstream tag.
	ObservedCommit = "df17a187974c3795c3f1d2ea47bbacbdb675dde5"
)

var (
	errVersionTooOld                     = errors.New("plandex: version is older than the inspected baseline")
	errInitialTaskContractMissing        = errors.New("plandex: race-free interactive initial-task contract is missing")
	errStandingInstructionsMissing       = errors.New("plandex: hidden standing-instruction channel is missing")
	errObserverHooksMissing              = errors.New("plandex: isolated observation hooks are missing")
	errNativeSessionIdentityMissing      = errors.New("plandex: durable native session identity is missing")
	errExactRestoreMissing               = errors.New("plandex: exact restore-by-id command is missing")
	errPermissionContractMissing         = errors.New("plandex: truthful permission mapping is missing")
	errBoundedCancellationMissing        = errors.New("plandex: bounded session cancellation is missing")
	errAuthenticationStatusMissing       = errors.New("plandex: non-interactive authentication status is missing")
	errReplacementProfileIsolationUnsafe = errors.New("plandex: replacement home/profile isolation is unsafe")
)

// Contract is the subset of the upstream CLI contract AO needs before it can
// safely own a Plandex terminal session. A field must be set only after it is
// documented and exercised against the pinned executable.
type Contract struct {
	Version string

	RaceFreeInitialTask        bool
	HiddenStandingInstructions bool
	IsolatedObserverHooks      bool
	NativeSessionID            bool
	ExactRestoreByID           bool
	TruthfulPermissions        bool
	BoundedCancellation        bool
	AuthenticationStatus       bool

	// UsesReplacementHome is always rejected. Plandex keeps authentication,
	// account selection, project identity, plan selection, and REPL state below
	// ~/.plandex-home-v2, so replacing HOME would hide user-owned state.
	UsesReplacementHome bool
}

// ObservedContract returns the capabilities exposed by Plandex cli/v2.2.1.
// It intentionally does not describe a usable AO adapter: ValidateContract
// rejects this value and therefore keeps registration fail-closed.
func ObservedContract() Contract {
	return Contract{Version: ObservedVersion}
}

// ValidateContract rejects an upstream contract until every TUI safety and
// lifecycle gate AO relies on has been proven. Registration code must not use a
// Plandex adapter unless this function returns nil for a live-tested contract.
func ValidateContract(contract Contract) error {
	var errs []error

	version, err := parseVersion(contract.Version)
	if err != nil {
		errs = append(errs, fmt.Errorf("plandex: parse version %q: %w", contract.Version, err))
	} else if version.lessThan(versionTriple{major: 2, minor: 2, patch: 1}) {
		errs = append(errs, errVersionTooOld)
	}

	if contract.UsesReplacementHome {
		errs = append(errs, errReplacementProfileIsolationUnsafe)
	}
	if !contract.RaceFreeInitialTask {
		errs = append(errs, errInitialTaskContractMissing)
	}
	if !contract.HiddenStandingInstructions {
		errs = append(errs, errStandingInstructionsMissing)
	}
	if !contract.IsolatedObserverHooks {
		errs = append(errs, errObserverHooksMissing)
	}
	if !contract.NativeSessionID {
		errs = append(errs, errNativeSessionIdentityMissing)
	}
	if !contract.ExactRestoreByID {
		errs = append(errs, errExactRestoreMissing)
	}
	if !contract.TruthfulPermissions {
		errs = append(errs, errPermissionContractMissing)
	}
	if !contract.BoundedCancellation {
		errs = append(errs, errBoundedCancellationMissing)
	}
	if !contract.AuthenticationStatus {
		errs = append(errs, errAuthenticationStatusMissing)
	}

	return errors.Join(errs...)
}

type versionTriple struct {
	major int
	minor int
	patch int
}

func parseVersion(raw string) (versionTriple, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "v"))
	if suffix := strings.IndexAny(raw, "+-"); suffix >= 0 {
		raw = raw[:suffix]
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return versionTriple{}, errors.New("expected major.minor.patch")
	}
	values := make([]int, len(parts))
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return versionTriple{}, fmt.Errorf("invalid component %q", part)
		}
		values[i] = value
	}
	return versionTriple{major: values[0], minor: values[1], patch: values[2]}, nil
}

func (v versionTriple) lessThan(other versionTriple) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	return v.patch < other.patch
}
