package vmbrowser

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// This file is a faithful port of frontend/src/main/browser-act-matcher.ts:
// pure, deterministic matching for the `act` action, given agent-browser's
// structured refs map. No LLM, no session state.

// SnapshotEntry is one actionable element from a snapshot's refs map.
type SnapshotEntry struct {
	Role string `json:"role"`
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

// ActCandidate is a scored match candidate.
type ActCandidate struct {
	Role  string `json:"role"`
	Name  string `json:"name"`
	Ref   string `json:"ref"`
	Score int    `json:"score"`
}

// MatchOutcome values.
const (
	OutcomeMatched   = "matched"
	OutcomeAmbiguous = "ambiguous"
	OutcomeNoMatch   = "no-match"
)

// MatchResult is the outcome of matching one instruction against refs.
type MatchResult struct {
	Outcome    string         `json:"outcome"`
	Candidate  ActCandidate   `json:"candidate,omitempty"`
	Candidates []ActCandidate `json:"candidates,omitempty"`
}

// MatchOptions carries disambiguation inputs. HasNth distinguishes an
// explicit --nth 0 from an unset option (TypeScript uses undefined).
type MatchOptions struct {
	Nth    int
	HasNth bool
}

func (o MatchOptions) nthValid(length int) bool {
	return o.HasNth && o.Nth >= 0 && o.Nth < length
}

var ariaRoles = map[string]struct{}{
	"button": {}, "link": {}, "textbox": {}, "checkbox": {}, "radio": {}, "combobox": {}, "listbox": {},
	"option": {}, "tab": {}, "menuitem": {}, "heading": {}, "image": {}, "dialog": {}, "switch": {},
	"slider": {}, "searchbox": {},
}

var leadingVerbs = map[string]struct{}{
	"click": {}, "tap": {}, "press": {}, "fill": {}, "type": {}, "select": {},
	"check": {}, "uncheck": {}, "hover": {}, "focus": {},
}

var articles = map[string]struct{}{"the": {}, "a": {}, "an": {}}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

var refKeyPattern = regexp.MustCompile(`^e\d+$`)

// ParseSnapshotEntries converts agent-browser's refs map (object keyed by
// "e<number>" ref) into document-ordered entries, ignoring malformed input
// instead of inventing candidates.
func ParseSnapshotEntries(snapshotRefs any) []SnapshotEntry {
	entries := []SnapshotEntry{}
	object, ok := snapshotRefs.(map[string]any)
	if !ok {
		return entries
	}
	for ref, value := range object {
		if !refKeyPattern.MatchString(ref) {
			continue
		}
		fields, ok := value.(map[string]any)
		if !ok {
			continue
		}
		role, roleOK := fields["role"].(string)
		name, nameOK := fields["name"].(string)
		if !roleOK || !nameOK {
			continue
		}
		entries = append(entries, SnapshotEntry{Role: role, Name: name, Ref: ref})
	}
	sort.Slice(entries, func(i, j int) bool {
		return refNumber(entries[i].Ref) < refNumber(entries[j].Ref)
	})
	return entries
}

func refNumber(ref string) int {
	number, err := strconv.Atoi(ref[1:])
	if err != nil {
		return math.MaxInt
	}
	return number
}

type parsedInstruction struct {
	roleHint   string
	nameHint   string
	nameTokens map[string]struct{}
}

func parseInstruction(instruction string, stripLeadingVerb bool) parsedInstruction {
	tokens := tokenize(instruction)
	if stripLeadingVerb && len(tokens) > 0 {
		if _, ok := leadingVerbs[tokens[0]]; ok {
			tokens = tokens[1:]
		}
	}
	filtered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, ok := articles[token]; ok {
			continue
		}
		filtered = append(filtered, token)
	}
	tokens = filtered

	roleHint := ""
	if len(tokens) > 0 {
		if _, ok := ariaRoles[tokens[len(tokens)-1]]; ok {
			roleHint = tokens[len(tokens)-1]
			tokens = tokens[:len(tokens)-1]
		}
	}
	nameTokens := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		nameTokens[token] = struct{}{}
	}
	return parsedInstruction{roleHint: roleHint, nameHint: strings.Join(tokens, " "), nameTokens: nameTokens}
}

func nameScore(entryName, nameHint string, nameTokens map[string]struct{}) int {
	if nameHint == "" {
		return 0
	}
	normalizedEntryName := strings.Join(tokenize(entryName), " ")
	if normalizedEntryName == "" {
		return 0
	}
	if normalizedEntryName == nameHint {
		return 10
	}
	paddedEntryName := " " + normalizedEntryName + " "
	paddedNameHint := " " + nameHint + " "
	if strings.Contains(paddedEntryName, paddedNameHint) || strings.Contains(paddedNameHint, paddedEntryName) {
		return 6
	}

	entryTokens := map[string]struct{}{}
	for _, token := range strings.Fields(normalizedEntryName) {
		entryTokens[token] = struct{}{}
	}
	if len(entryTokens) == 0 || len(nameTokens) == 0 {
		return 0
	}
	intersection := 0
	for token := range entryTokens {
		if _, ok := nameTokens[token]; ok {
			intersection++
		}
	}
	union := make(map[string]struct{}, len(entryTokens)+len(nameTokens))
	for token := range entryTokens {
		union[token] = struct{}{}
	}
	for token := range nameTokens {
		union[token] = struct{}{}
	}
	if len(union) == 0 {
		return 0
	}
	return int(math.Round(float64(intersection) / float64(len(union)) * 4))
}

func scoreCandidates(entries []SnapshotEntry, roleHint, nameHint string, nameTokens map[string]struct{}) []ActCandidate {
	candidates := []ActCandidate{}
	for _, entry := range entries {
		roleScore := 0
		if roleHint != "" && entry.Role == roleHint {
			roleScore = 3
		}
		score := roleScore + nameScore(entry.Name, nameHint, nameTokens)
		if score > 0 {
			candidates = append(candidates, ActCandidate{Role: entry.Role, Name: entry.Name, Ref: entry.Ref, Score: score})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	return candidates
}

const (
	maxAmbiguousCandidates = 5
	exactNameTier          = 10
	matchScoreFloor        = 4
	matchScoreGap          = 3
)

// MatchInstruction resolves a natural-language instruction against a
// snapshot's refs map, or says plainly that it cannot.
func MatchInstruction(instruction string, snapshotRefs any, opts MatchOptions) MatchResult {
	entries := ParseSnapshotEntries(snapshotRefs)
	instructions := []parsedInstruction{parseInstruction(instruction, false)}
	if tokens := tokenize(instruction); len(tokens) > 0 {
		if _, ok := leadingVerbs[tokens[0]]; ok {
			instructions = append(instructions, parseInstruction(instruction, true))
		}
	}
	var roleOnlyInstruction *parsedInstruction
	for _, parsed := range instructions {
		if parsed.roleHint != "" && parsed.nameHint == "" {
			roleOnlyInstruction = &parsed
			break
		}
	}

	evaluate := instructions
	if roleOnlyInstruction != nil {
		evaluate = []parsedInstruction{*roleOnlyInstruction}
	}
	candidatesByRef := map[string]ActCandidate{}
	for _, parsed := range evaluate {
		for _, candidate := range scoreCandidates(entries, parsed.roleHint, parsed.nameHint, parsed.nameTokens) {
			if previous, ok := candidatesByRef[candidate.Ref]; !ok || candidate.Score > previous.Score {
				candidatesByRef[candidate.Ref] = candidate
			}
		}
	}
	candidates := make([]ActCandidate, 0, len(candidatesByRef))
	for _, candidate := range candidatesByRef {
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return refNumber(candidates[i].Ref) < refNumber(candidates[j].Ref)
	})
	if len(candidates) == 0 {
		return MatchResult{Outcome: OutcomeNoMatch}
	}

	top := candidates[0]
	confident := top.Score >= exactNameTier && (len(candidates) < 2 || candidates[1].Score < exactNameTier)
	if !confident && top.Score >= matchScoreFloor && (len(candidates) < 2 || top.Score-candidates[1].Score >= matchScoreGap) {
		confident = true
	}
	if confident {
		return MatchResult{Outcome: OutcomeMatched, Candidate: top}
	}
	if roleOnlyInstruction != nil && opts.nthValid(len(candidates)) {
		return MatchResult{Outcome: OutcomeMatched, Candidate: candidates[opts.Nth]}
	}
	if len(candidates) == 1 && top.Score < matchScoreFloor {
		return MatchResult{Outcome: OutcomeNoMatch}
	}
	if opts.nthValid(len(candidates)) {
		return MatchResult{Outcome: OutcomeMatched, Candidate: candidates[opts.Nth]}
	}
	if len(candidates) > maxAmbiguousCandidates {
		candidates = candidates[:maxAmbiguousCandidates]
	}
	return MatchResult{Outcome: OutcomeAmbiguous, Candidates: candidates}
}
