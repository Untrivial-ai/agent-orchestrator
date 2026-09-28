package domain

import (
	"fmt"
	"slices"
	"strings"
)

// AgentModeVocabulary is the set of agent-owned mode values one harness
// accepts in AgentConfig.Mode. Adapters own the vocabulary at runtime (the
// real CLI defines what `--mode` means); this domain table is the single
// shared copy both domain validation and each adapter's ConfigSpec derive
// from, so they cannot drift the way the former Amp-only hardcode did.
type AgentModeVocabulary struct {
	// Harness the vocabulary belongs to.
	Harness AgentHarness
	// Values are the mode strings the adapter accepts, in the order the
	// adapter documents them.
	Values []string
}

// HarnessModeVocabularies lists the mode vocabulary of every harness whose
// AgentConfig.Mode is agent-owned (adapters that expose modes instead of raw
// model ids). Registering a vocabulary here is what makes the domain accept
// those values in AgentConfig.Mode; adapters must derive their ConfigSpec
// enum from the same table (pinned by the registry cross-check test) so a UI
// can never offer a value the domain rejects, and vice versa.
//
// Amp deliberately chooses the underlying models per mode (low|medium|high|
// ultra). ZCode passes the mode through to `zcode --mode` (zcode 0.16.5:
// "Supported modes: build, edit, plan, yolo").
var HarnessModeVocabularies = []AgentModeVocabulary{
	{Harness: HarnessAmp, Values: []string{"low", "medium", "high", "ultra"}},
	{Harness: HarnessZCode, Values: []string{"build", "edit", "plan", "yolo"}},
}

// ModeVocabulary returns the vocabulary declared for harness. ok is false for
// a harness without agent-owned modes.
func ModeVocabulary(harness AgentHarness) (AgentModeVocabulary, bool) {
	for _, v := range HarnessModeVocabularies {
		if v.Harness == harness {
			return v, true
		}
	}
	return AgentModeVocabulary{}, false
}

// ModeKnownAnywhere reports whether mode is accepted by some harness's mode
// vocabulary (or empty, meaning "the adapter's own baseline"). Domain-level
// config validation cannot know which harness a value will reach — a project
// may re-point the harness later — so it accepts the union and lets the
// adapter make the final harness-specific call at argv-build time.
func ModeKnownAnywhere(mode string) bool {
	if mode == "" {
		return true
	}
	for _, v := range HarnessModeVocabularies {
		if slices.Contains(v.Values, mode) {
			return true
		}
	}
	return false
}

// AllModeValues renders the combined mode vocabulary for usage errors.
func AllModeValues() string {
	seen := map[string]bool{}
	var parts []string
	for _, v := range HarnessModeVocabularies {
		for _, m := range v.Values {
			if seen[m] {
				continue
			}
			seen[m] = true
			parts = append(parts, m)
		}
	}
	return strings.Join(parts, ", ")
}

// ValidateMode reports whether mode is a valid AgentConfig.Mode for harness,
// with a clean usage error naming that harness's vocabulary. Adapters call
// this at argv-build time so a config written by another path fails as input
// validation instead of reaching the agent CLI.
func ValidateMode(harness AgentHarness, mode string) error {
	v, ok := ModeVocabulary(harness)
	if !ok {
		if mode == "" {
			return nil
		}
		return fmt.Errorf("harness %q does not expose agent modes; agentConfig.mode must be empty", harness)
	}
	if mode == "" || slices.Contains(v.Values, mode) {
		return nil
	}
	return fmt.Errorf("invalid %s mode %q: supported modes are %s", harness, mode, strings.Join(v.Values, ", "))
}
