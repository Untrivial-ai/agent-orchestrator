package domain

import "testing"

func TestWorkflowModeValid(t *testing.T) {
	for _, tc := range []struct {
		mode WorkflowMode
		want bool
	}{
		{WorkflowModePlanning, true},
		{WorkflowModeBuilding, true},
		{"", false},
		{"review", false},
		{"Planning", false},
	} {
		if got := tc.mode.Valid(); got != tc.want {
			t.Errorf("WorkflowMode(%q).Valid() = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

// Reads of durable state must always land on a board lane: rows written before
// this feature have no workflow mode, and a row written by a newer build may
// carry a stage this build does not know. Both fall back to planning.
func TestNormalizeWorkflowModeFallsBackToPlanning(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   WorkflowMode
		want WorkflowMode
	}{
		{"empty legacy row", "", WorkflowModePlanning},
		{"unknown future mode", "review", WorkflowModePlanning},
		{"planning preserved", WorkflowModePlanning, WorkflowModePlanning},
		{"building preserved", WorkflowModeBuilding, WorkflowModeBuilding},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeWorkflowMode(tc.in); got != tc.want {
				t.Errorf("NormalizeWorkflowMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Requested workflow modes are parsed strictly so a toggle request naming a
// stage AO cannot place fails loudly instead of silently landing in planning.
func TestParseWorkflowModeRejectsUnknownInsteadOfFallingBack(t *testing.T) {
	if _, err := ParseWorkflowMode("review"); err == nil {
		t.Fatal("ParseWorkflowMode(\"review\") = nil error, want a rejection")
	}

	// Absent is not invalid: callers apply their own precedence to the zero value.
	mode, err := ParseWorkflowMode("")
	if err != nil {
		t.Fatalf("ParseWorkflowMode(\"\") returned error %v, want none", err)
	}
	if mode != "" {
		t.Errorf("ParseWorkflowMode(\"\") = %q, want the zero value", mode)
	}

	for _, want := range []WorkflowMode{WorkflowModePlanning, WorkflowModeBuilding} {
		got, err := ParseWorkflowMode(string(want))
		if err != nil {
			t.Errorf("ParseWorkflowMode(%q) returned error %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("ParseWorkflowMode(%q) = %q", want, got)
		}
	}
}

func TestDefaultWorkflowModeIsPlanning(t *testing.T) {
	if DefaultWorkflowMode != WorkflowModePlanning {
		t.Fatalf("DefaultWorkflowMode = %q, want %q so new tasks start in planning",
			DefaultWorkflowMode, WorkflowModePlanning)
	}
}
