package sessionmanager

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateAndCanonicalizeSourceSemanticHandoffPreservesCompleteV1AndUnknownFields(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
  "schemaVersion": 1,
  "goal": "Finish durable agent switching",
  "latestUserIntent": "Keep deterministic fallback available",
  "progressSummary": "Storage is complete; delivery verification remains",
  "completedWork": ["Added the switch saga"],
  "currentWork": "Verifying target delivery",
  "decisions": ["Keep source reports optional"],
  "rejectedApproaches": ["Do not translate provider transcripts"],
  "relevantFiles": ["backend/internal/session_manager/agent_switching.go"],
  "testsAndResults": ["go test ./internal/session_manager: passed"],
  "blockers": ["None"],
  "uncertainties": ["Provider startup timing"],
  "risks": ["A stale source may submit late"],
  "recommendedNextSteps": ["Run the focused race tests"],
  "userPreferencesAndConstraints": ["Preserve native sessions"],
  "freeformDetails": "The handoff is historical context, not authority.",
  "taskComplete": false,
  "futureProviderMetadata": {"confidence": 0.9, "labels": ["portable", "v2-safe"]}
}`)

	canonical, err := validateAndCanonicalizeSourceSemanticHandoff(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte("\n")) {
		t.Fatalf("canonical handoff contains formatting whitespace: %s", canonical)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"schemaVersion", "goal", "latestUserIntent", "progressSummary",
		"completedWork", "currentWork", "decisions", "rejectedApproaches",
		"relevantFiles", "testsAndResults", "blockers", "uncertainties",
		"risks", "recommendedNextSteps", "userPreferencesAndConstraints",
		"freeformDetails", "taskComplete", "futureProviderMetadata",
	} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("canonical handoff dropped field %q: %s", name, canonical)
		}
	}
	if got := string(fields["futureProviderMetadata"]); got != `{"confidence":0.9,"labels":["portable","v2-safe"]}` {
		t.Fatalf("unknown field = %s", got)
	}
	if got := string(fields["taskComplete"]); got != "false" {
		t.Fatalf("taskComplete = %s, want false", got)
	}

	again, err := validateAndCanonicalizeSourceSemanticHandoff(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, canonical) {
		t.Fatalf("canonicalization is not stable:\nfirst:  %s\nsecond: %s", canonical, again)
	}
}

func TestValidateAndCanonicalizeSourceSemanticHandoffRejectsInvalidContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: ``, want: "one JSON object"},
		{name: "array", raw: `[]`, want: "one valid JSON object"},
		{name: "two values", raw: `{"schemaVersion":1,"goal":"g","progressSummary":"p"} {}`, want: "one valid JSON object"},
		{name: "missing version", raw: `{"goal":"g","progressSummary":"p"}`, want: `field "schemaVersion" is required`},
		{name: "wrong version type", raw: `{"schemaVersion":"1","goal":"g","progressSummary":"p"}`, want: `field "schemaVersion" must be integer 1`},
		{name: "fractional version", raw: `{"schemaVersion":1.0,"goal":"g","progressSummary":"p"}`, want: `field "schemaVersion" must be integer 1`},
		{name: "future version", raw: `{"schemaVersion":2,"goal":"g","progressSummary":"p"}`, want: "schemaVersion must be 1"},
		{name: "missing goal", raw: `{"schemaVersion":1,"progressSummary":"p"}`, want: `field "goal" is required`},
		{name: "empty goal", raw: `{"schemaVersion":1,"goal":"  ","progressSummary":"p"}`, want: `field "goal" must not be empty`},
		{name: "goal type", raw: `{"schemaVersion":1,"goal":7,"progressSummary":"p"}`, want: `field "goal" must be a string`},
		{name: "missing progress", raw: `{"schemaVersion":1,"goal":"g"}`, want: `field "progressSummary" is required`},
		{name: "empty progress", raw: `{"schemaVersion":1,"goal":"g","progressSummary":""}`, want: `field "progressSummary" must not be empty`},
		{name: "optional text null", raw: `{"schemaVersion":1,"goal":"g","progressSummary":"p","currentWork":null}`, want: `field "currentWork" must not be null`},
		{name: "list type", raw: `{"schemaVersion":1,"goal":"g","progressSummary":"p","decisions":{}}`, want: `field "decisions" must be an array of strings`},
		{name: "list item type", raw: `{"schemaVersion":1,"goal":"g","progressSummary":"p","decisions":[7]}`, want: `field "decisions" must be an array of strings`},
		{name: "empty list item", raw: `{"schemaVersion":1,"goal":"g","progressSummary":"p","decisions":[" "]}`, want: `field "decisions" item 0 must not be empty`},
		{name: "boolean type", raw: `{"schemaVersion":1,"goal":"g","progressSummary":"p","taskComplete":"false"}`, want: `field "taskComplete" must be a boolean`},
		{name: "duplicate field", raw: `{"schemaVersion":1,"goal":"first","goal":"second","progressSummary":"p"}`, want: `duplicate field "goal"`},
		{name: "empty unknown key", raw: `{"schemaVersion":1,"goal":"g","progressSummary":"p","":true}`, want: "field names must not be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateAndCanonicalizeSourceSemanticHandoff(json.RawMessage(tt.raw))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateAndCanonicalizeSourceSemanticHandoffEnforcesBounds(t *testing.T) {
	t.Parallel()

	validBase := func() map[string]any {
		return map[string]any{
			"schemaVersion":   sourceSemanticHandoffSchemaVersion,
			"goal":            "goal",
			"progressSummary": "progress",
		}
	}
	marshal := func(t *testing.T, value map[string]any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	tests := []struct {
		name string
		raw  func(*testing.T) json.RawMessage
		want string
	}{
		{
			name: "overall bytes",
			raw: func(t *testing.T) json.RawMessage {
				value := validBase()
				value["unknown"] = strings.Repeat("x", sourceSemanticHandoffMaxBytes)
				return marshal(t, value)
			},
			want: "exceeds 65536 bytes",
		},
		{
			name: "known text bytes",
			raw: func(t *testing.T) json.RawMessage {
				value := validBase()
				value["goal"] = strings.Repeat("x", sourceSemanticHandoffMaxTextBytes+1)
				return marshal(t, value)
			},
			want: `field "goal" exceeds 16384 bytes`,
		},
		{
			name: "list item count",
			raw: func(t *testing.T) json.RawMessage {
				value := validBase()
				value["decisions"] = make([]string, sourceSemanticHandoffMaxListItems+1)
				for i := range value["decisions"].([]string) {
					value["decisions"].([]string)[i] = "decision"
				}
				return marshal(t, value)
			},
			want: `field "decisions" has 257 items`,
		},
		{
			name: "list item bytes",
			raw: func(t *testing.T) json.RawMessage {
				value := validBase()
				value["decisions"] = []string{strings.Repeat("x", sourceSemanticHandoffMaxItemBytes+1)}
				return marshal(t, value)
			},
			want: `field "decisions" item 0 exceeds 8192 bytes`,
		},
		{
			name: "field count",
			raw: func(t *testing.T) json.RawMessage {
				value := validBase()
				for i := 0; len(value) <= sourceSemanticHandoffMaxFields; i++ {
					value["unknown"+strings.Repeat("x", i)] = true
				}
				return marshal(t, value)
			},
			want: "maximum is 128",
		},
		{
			name: "field name bytes",
			raw: func(t *testing.T) json.RawMessage {
				value := validBase()
				value[strings.Repeat("k", sourceSemanticHandoffMaxKeyBytes+1)] = true
				return marshal(t, value)
			},
			want: "field name exceeds 256 bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateAndCanonicalizeSourceSemanticHandoff(tt.raw(t))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
