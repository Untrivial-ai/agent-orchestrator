package domain

import (
	"encoding/json"
	"strconv"
	"testing"
)

func TestNativeCheckpointKeepsUnknownStopsAndSubmissionMultiplicity(t *testing.T) {
	submit := NativeCheckpointObservation{Generation: "launch", PromptID: "A", Submission: true, Text: "continue", SubmissionID: "submission-0"}
	encoded := AppendNativeCheckpoint("", "native", submit)
	submit.SubmissionID = "submission-1"
	encoded = AppendNativeCheckpoint(encoded, "native", submit)
	newer := NativeCheckpointObservation{Generation: "launch", PromptID: "C", Text: "Done C"}
	older := NativeCheckpointObservation{Generation: "launch", PromptID: "B", Text: "Done B"}
	encoded = AppendNativeCheckpoint(encoded, "native", newer)
	encoded = AppendNativeCheckpoint(encoded, "native", older)
	encoded = AppendNativeCheckpoint(encoded, "native", older)
	var evidence NativeCheckpointEvidence
	if err := json.Unmarshal([]byte(encoded), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Invalid || len(evidence.Events) != 4 || evidence.Events[2] != newer {
		t.Fatalf("lost witness: %+v", evidence)
	}
}

func TestNativeCheckpointOverflowAndCorruptionStayFailClosed(t *testing.T) {
	for _, initial := range []string{"{bad JSON", `{"nativeId":"other"}`, ""} {
		encoded := initial
		for i := 0; i < 513; i++ {
			encoded = AppendNativeCheckpoint(encoded, "native", NativeCheckpointObservation{Generation: "launch", PromptID: strconv.Itoa(i), Submission: true, SubmissionID: strconv.Itoa(i)})
		}
		var evidence NativeCheckpointEvidence
		if err := json.Unmarshal([]byte(encoded), &evidence); err != nil {
			t.Fatal(err)
		}
		if !evidence.Invalid || len(evidence.Events) > 512 {
			t.Fatalf("unsafe evidence: %+v", evidence)
		}
	}
}
