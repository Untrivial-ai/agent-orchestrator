package onedev

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// UnmappedState is the normalized state used for a OneDev state that appears
// in no mapping. See the package doc for why the default leans open rather
// than done: treating live work as finished is silent and unrecoverable,
// treating finished work as live is visible and self-correcting.
const UnmappedState = domain.IssueOpen

// StateMap maps a OneDev issue state name onto the normalized cross-provider
// vocabulary. Keys keep the spelling an operator wrote, because the key is
// also what goes back into a OneDev query; lookup folds case and trims
// surrounding whitespace, so "in progress" and " In Progress " are one entry.
//
// OneDev states are configured per instance, so this map is data rather than a
// switch statement: DefaultStateMap is a starting point, not the truth.
type StateMap map[string]domain.NormalizedIssueState

// DefaultStateMap is the mapping used when an operator configures none. It
// covers the state names OneDev's own default issue workflow and the common
// board setups use. Anything else is an instance-specific workflow state and
// belongs in AO_ONEDEV_ISSUE_STATES rather than guessed at here.
func DefaultStateMap() StateMap {
	return StateMap{
		"Open":        domain.IssueOpen,
		"In Progress": domain.IssueInProgress,
		"In Review":   domain.IssueInReview,
		"Review":      domain.IssueInReview,
		"Closed":      domain.IssueDone,
	}
}

// normalizedStates is the closed set an override may target. It is the domain
// vocabulary verbatim — adding a value is a port-level decision, so an
// override naming something outside it is a configuration error rather than a
// new state.
var normalizedStates = map[string]domain.NormalizedIssueState{
	string(domain.IssueOpen):       domain.IssueOpen,
	string(domain.IssueInProgress): domain.IssueInProgress,
	string(domain.IssueInReview):   domain.IssueInReview,
	string(domain.IssueDone):       domain.IssueDone,
	string(domain.IssueCancelled):  domain.IssueCancelled,
}

func stateKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Normalize returns the normalized state for a OneDev state name and whether
// the name was mapped. An unmapped name yields UnmappedState with ok=false, so
// a caller that wants to log or count the gap can, while a caller that just
// wants a state gets a usable one.
func (m StateMap) Normalize(native string) (domain.NormalizedIssueState, bool) {
	if s, ok := m[native]; ok {
		return s, true
	}
	key := stateKey(native)
	if key == "" {
		return UnmappedState, false
	}
	for name, s := range m {
		if stateKey(name) == key {
			return s, true
		}
	}
	return UnmappedState, false
}

// TerminalStates returns the OneDev state names that map to a finished state
// (done or cancelled), in the spelling the instance uses, sorted so a
// generated query does not change with map iteration order.
//
// These names — not a hardcoded "Closed" — are what List turns into its
// state criteria, and the two directions are deliberately asymmetric:
// "open" excludes exactly these states (so an unmapped state stays in the
// results, matching UnmappedState), while "closed" includes exactly these
// states (so an unmapped state stays out). Both directions therefore agree
// with the unmapped default instead of contradicting it.
func (m StateMap) TerminalStates() []string {
	out := make([]string, 0, len(m))
	for name, s := range m {
		if s == domain.IssueDone || s == domain.IssueCancelled {
			out = append(out, strings.TrimSpace(name))
		}
	}
	sort.Strings(out)
	return out
}

// WithOverrides returns a copy of the map with the given overrides applied.
// Keys match existing entries case-insensitively, so an operator writing
// "closed=open" replaces the default "Closed" entry rather than adding a
// second one that shadows it depending on iteration order. A key absent from
// the map is added, which is how instance-specific states such as "Blocked"
// are configured.
//
// Values must name one of the normalized states; anything else is rejected so
// a typo surfaces at daemon boot rather than as an issue silently mapped to
// nothing.
func (m StateMap) WithOverrides(overrides map[string]string) (StateMap, error) {
	out := make(StateMap, len(m)+len(overrides))
	for name, s := range m {
		out[name] = s
	}
	for _, name := range sortedKeys(overrides) {
		raw := strings.TrimSpace(overrides[name])
		native := strings.TrimSpace(name)
		if native == "" {
			continue
		}
		state, ok := normalizedStates[strings.ToLower(raw)]
		if !ok {
			return nil, fmt.Errorf(
				"onedev tracker: state %q maps to unknown normalized state %q (want one of %s)",
				native, raw, strings.Join(normalizedStateNames(), ", "))
		}
		for existing := range out {
			if stateKey(existing) == stateKey(native) {
				delete(out, existing)
			}
		}
		out[native] = state
	}
	return out, nil
}

// NewStateMap builds the tracker's state mapping from the defaults plus an
// operator's overrides. A nil or empty override map yields DefaultStateMap.
func NewStateMap(overrides map[string]string) (StateMap, error) {
	return DefaultStateMap().WithOverrides(overrides)
}

func normalizedStateNames() []string {
	names := make([]string, 0, len(normalizedStates))
	for name := range normalizedStates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
