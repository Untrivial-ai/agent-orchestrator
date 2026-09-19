package vmbrowser

import (
	"reflect"
	"testing"
)

// Ports of every case in frontend/src/main/browser-act-matcher.test.ts.
// The TypeScript suite is the source of truth; expectations are identical.

func baseFixture() map[string]any {
	return map[string]any{
		"e1":  map[string]any{"role": "heading", "name": "Hi"},
		"e2":  map[string]any{"role": "button", "name": "Sign In"},
		"e3":  map[string]any{"role": "button", "name": "Nope"},
		"e5":  map[string]any{"role": "checkbox", "name": ""},
		"e10": map[string]any{"role": "switch", "name": "Toggle"},
		"e11": map[string]any{"role": "option", "name": "A"},
	}
}

func TestParseSnapshotEntriesParsesStructuredRefsMap(t *testing.T) {
	got := ParseSnapshotEntries(baseFixture())
	want := []SnapshotEntry{
		{Role: "heading", Name: "Hi", Ref: "e1"},
		{Role: "button", Name: "Sign In", Ref: "e2"},
		{Role: "button", Name: "Nope", Ref: "e3"},
		{Role: "checkbox", Name: "", Ref: "e5"},
		{Role: "switch", Name: "Toggle", Ref: "e10"},
		{Role: "option", Name: "A", Ref: "e11"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseSnapshotEntriesIgnoresMalformedRefs(t *testing.T) {
	got := ParseSnapshotEntries(map[string]any{
		"e1":  map[string]any{"role": "button", "name": "Save"},
		"bad": map[string]any{"role": "button", "name": "Delete"},
		"e2":  map[string]any{"role": 12, "name": "Cancel"},
		"e3":  map[string]any{"role": "button"},
	})
	want := []SnapshotEntry{{Role: "button", Name: "Save", Ref: "e1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseSnapshotEntriesRestoresDocumentOrder(t *testing.T) {
	got := ParseSnapshotEntries(map[string]any{
		"e1":  map[string]any{"role": "button", "name": "First"},
		"e10": map[string]any{"role": "button", "name": "Tenth"},
		"e11": map[string]any{"role": "button", "name": "Eleventh"},
		"e2":  map[string]any{"role": "button", "name": "Second"},
		"e3":  map[string]any{"role": "button", "name": "Third"},
	})
	want := []SnapshotEntry{
		{Role: "button", Name: "First", Ref: "e1"},
		{Role: "button", Name: "Second", Ref: "e2"},
		{Role: "button", Name: "Third", Ref: "e3"},
		{Role: "button", Name: "Tenth", Ref: "e10"},
		{Role: "button", Name: "Eleventh", Ref: "e11"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestMatchInstructionExactNameMatch(t *testing.T) {
	got := MatchInstruction("the submit button",
		map[string]any{"e3": map[string]any{"role": "button", "name": "Submit"}}, MatchOptions{})
	if got.Outcome != OutcomeMatched || got.Candidate != (ActCandidate{Role: "button", Name: "Submit", Ref: "e3", Score: 13}) {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchInstructionFuzzyMatch(t *testing.T) {
	got := MatchInstruction("submit", map[string]any{
		"e3": map[string]any{"role": "button", "name": "Submit"},
		"e4": map[string]any{"role": "button", "name": "Cancel"},
	}, MatchOptions{})
	if got.Outcome != OutcomeMatched || got.Candidate.Score != 10 || got.Candidate.Ref != "e3" {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchInstructionAmbiguousOnIdenticalScores(t *testing.T) {
	got := MatchInstruction("add to cart", map[string]any{
		"e1": map[string]any{"role": "button", "name": "Add to Cart"},
		"e2": map[string]any{"role": "button", "name": "Add to Cart"},
	}, MatchOptions{})
	if got.Outcome != OutcomeAmbiguous {
		t.Fatalf("outcome = %q", got.Outcome)
	}
	if len(got.Candidates) != 2 || got.Candidates[0].Ref != "e1" || got.Candidates[1].Ref != "e2" {
		t.Fatalf("candidates = %#v", got.Candidates)
	}
}

func TestMatchInstructionLoneCandidateBelowFloor(t *testing.T) {
	got := MatchInstruction("the cancel button",
		map[string]any{"e1": map[string]any{"role": "button", "name": "Confirm delete"}}, MatchOptions{})
	if got.Outcome != OutcomeNoMatch {
		t.Fatalf("outcome = %q, want no-match", got.Outcome)
	}
}

func TestMatchInstructionDoesNotInflateShortNames(t *testing.T) {
	for _, name := range []string{"", "C"} {
		got := MatchInstruction("the cancel button",
			map[string]any{"e1": map[string]any{"role": "button", "name": name}}, MatchOptions{})
		if got.Outcome != OutcomeNoMatch {
			t.Fatalf("name %q: outcome = %q, want no-match", name, got.Outcome)
		}
	}
}

func TestMatchInstructionNthCannotForceLoneCandidate(t *testing.T) {
	got := MatchInstruction("the cancel button",
		map[string]any{"e1": map[string]any{"role": "button", "name": "Confirm delete"}}, MatchOptions{Nth: 0, HasNth: true})
	if got.Outcome != OutcomeNoMatch {
		t.Fatalf("outcome = %q, want no-match", got.Outcome)
	}
}

func TestMatchInstructionActionWordDoesNotOutrankExplicitRole(t *testing.T) {
	got := MatchInstruction("check the checkbox", map[string]any{
		"e1": map[string]any{"role": "checkbox", "name": ""},
		"e2": map[string]any{"role": "button", "name": "Check"},
	}, MatchOptions{})
	if got.Outcome != OutcomeNoMatch {
		t.Fatalf("outcome = %q, want no-match", got.Outcome)
	}
}

func TestMatchInstructionNthSelectsUnnamedControlFromRoleOnly(t *testing.T) {
	got := MatchInstruction("check the checkbox", map[string]any{
		"e1": map[string]any{"role": "checkbox", "name": ""},
		"e2": map[string]any{"role": "button", "name": "Check"},
	}, MatchOptions{Nth: 0, HasNth: true})
	if got.Outcome != OutcomeMatched || got.Candidate != (ActCandidate{Role: "checkbox", Name: "", Ref: "e1", Score: 3}) {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchInstructionTrailingRoleNounIsRoleHint(t *testing.T) {
	got := MatchInstruction("the Save Image button", map[string]any{
		"e1": map[string]any{"role": "image", "name": "Save"},
		"e2": map[string]any{"role": "button", "name": "Save Image"},
	}, MatchOptions{})
	if got.Outcome != OutcomeMatched || got.Candidate != (ActCandidate{Role: "button", Name: "Save Image", Ref: "e2", Score: 13}) {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchInstructionKeepsLeadingActionWordInName(t *testing.T) {
	got := MatchInstruction("select all", map[string]any{
		"e1": map[string]any{"role": "button", "name": "Select all"},
		"e2": map[string]any{"role": "button", "name": "Clear all"},
	}, MatchOptions{})
	if got.Outcome != OutcomeMatched || got.Candidate != (ActCandidate{Role: "button", Name: "Select all", Ref: "e1", Score: 10}) {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchInstructionRoleOnlyAgainstManyIsAmbiguous(t *testing.T) {
	got := MatchInstruction("the button", map[string]any{
		"e1": map[string]any{"role": "button", "name": "Add to Cart"},
		"e2": map[string]any{"role": "button", "name": "Buy Now"},
		"e3": map[string]any{"role": "button", "name": "Wishlist"},
	}, MatchOptions{})
	if got.Outcome != OutcomeAmbiguous {
		t.Fatalf("outcome = %q, want ambiguous", got.Outcome)
	}
}

func TestMatchInstructionNothingScores(t *testing.T) {
	got := MatchInstruction("the submit button",
		map[string]any{"e1": map[string]any{"role": "textbox", "name": "Email"}}, MatchOptions{})
	if got.Outcome != OutcomeNoMatch {
		t.Fatalf("outcome = %q, want no-match", got.Outcome)
	}
}

func TestMatchInstructionNthBreaksTie(t *testing.T) {
	got := MatchInstruction("add to cart", map[string]any{
		"e1": map[string]any{"role": "button", "name": "Add to Cart"},
		"e2": map[string]any{"role": "button", "name": "Add to Cart"},
	}, MatchOptions{Nth: 1, HasNth: true})
	if got.Outcome != OutcomeMatched || got.Candidate != (ActCandidate{Role: "button", Name: "Add to Cart", Ref: "e2", Score: 10}) {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchInstructionExactNameOutranksFuzzy(t *testing.T) {
	got := MatchInstruction("submit", map[string]any{
		"e1": map[string]any{"role": "button", "name": "Submit"},
		"e2": map[string]any{"role": "button", "name": "Submit and continue"},
	}, MatchOptions{})
	if got.Outcome != OutcomeMatched || got.Candidate != (ActCandidate{Role: "button", Name: "Submit", Ref: "e1", Score: 10}) {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchInstructionCapsAmbiguousCandidates(t *testing.T) {
	refs := map[string]any{}
	names := []string{"Item 1", "Item 2", "Item 3", "Item 4", "Item 5", "Item 6", "Item 7", "Item 8"}
	keys := []string{"e1", "e2", "e3", "e4", "e5", "e6", "e7", "e8"}
	for i, key := range keys {
		refs[key] = map[string]any{"role": "button", "name": names[i]}
	}
	got := MatchInstruction("the item button", refs, MatchOptions{})
	if got.Outcome != OutcomeAmbiguous {
		t.Fatalf("outcome = %q, want ambiguous", got.Outcome)
	}
	if len(got.Candidates) != 5 {
		t.Fatalf("candidates = %d, want capped at 5", len(got.Candidates))
	}
}
