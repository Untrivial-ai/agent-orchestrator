// Package deepagents contains the upstream capability gate for DeepAgents Code.
// It deliberately has no production registration until the live conformance
// suite proves every capability represented by Contract.
package deepagents

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

const MinimumVersion = "0.1.70"

var (
	ErrVersionUnsupported          = errors.New("DeepAgents version is unsupported")
	ErrProfileIsolationUnsafe      = errors.New("DeepAgents profile isolation would replace user state")
	ErrSystemPromptContractMissing = errors.New("DeepAgents system-prompt contract is missing")
	ErrHookIsolationMissing        = errors.New("DeepAgents isolated hook contract is missing")
	ErrTUIContractMissing          = errors.New("DeepAgents TUI lifecycle contract is incomplete")
	ErrACPContractMissing          = errors.New("DeepAgents ACP contract is incomplete")
	ErrAuthStatusMissing           = errors.New("DeepAgents auth-status contract is missing")
	semverPattern                  = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)
)

// Contract records capabilities proved against one exact upstream executable.
// Boolean fields are evidence, not feature wishes: callers must leave a field
// false until the live conformance suite has observed the behavior end to end.
type Contract struct {
	Version             string
	SystemPromptFile    bool
	SupportedOverlay    bool
	UsesReplacementHome bool
	IsolatedHooks       bool
	InitialMessage      bool
	ExactRestore        bool
	TUISessionID        bool
	ACP                 bool
	ACPLoad             bool
	ACPReplay           bool
	ACPPermissions      bool
	ACPCancel           bool
	AuthStatus          bool
}

func ValidateContract(c Contract) error {
	if c.UsesReplacementHome {
		return ErrProfileIsolationUnsafe
	}
	installed, ok := parseSemver(c.Version)
	minimum, _ := parseSemver(MinimumVersion)
	if !ok || installed.less(minimum) {
		return fmt.Errorf("%w: got %q, need %s or newer", ErrVersionUnsupported, c.Version, MinimumVersion)
	}
	if !c.SystemPromptFile && !c.SupportedOverlay {
		return ErrSystemPromptContractMissing
	}
	if !c.IsolatedHooks {
		return ErrHookIsolationMissing
	}
	if !c.InitialMessage || !c.ExactRestore || !c.TUISessionID {
		return ErrTUIContractMissing
	}
	if !c.ACP || !c.ACPLoad || !c.ACPReplay || !c.ACPPermissions || !c.ACPCancel {
		return ErrACPContractMissing
	}
	if !c.AuthStatus {
		return ErrAuthStatusMissing
	}
	return nil
}

type semver [3]int

func parseSemver(value string) (semver, bool) {
	match := semverPattern.FindStringSubmatch(value)
	if len(match) != 4 {
		return semver{}, false
	}
	var result semver
	for i := range result {
		part, err := strconv.Atoi(match[i+1])
		if err != nil {
			return semver{}, false
		}
		result[i] = part
	}
	return result, true
}

func (v semver) less(other semver) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

func (v semver) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }
