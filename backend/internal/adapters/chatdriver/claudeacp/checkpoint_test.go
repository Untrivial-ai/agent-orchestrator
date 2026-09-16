package claudeacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func checkpointFixture(t *testing.T, prompts, answers []string) string {
	t.Helper()
	var lines []string
	parent := ""
	for i, prompt := range prompts {
		userID, assistantID := fmt.Sprintf("user-%d", i), fmt.Sprintf("assistant-%d", i)
		attachmentID := fmt.Sprintf("submission-attachment-%d", i)
		for _, record := range []map[string]any{
			{"type": "user", "sessionId": "native", "uuid": userID, "parentUuid": parent, "promptId": fmt.Sprintf("prompt-%d", i),
				"message": map[string]any{"content": prompt}},
			{"type": "attachment", "sessionId": "native", "uuid": attachmentID, "parentUuid": userID,
				"attachment": map[string]any{"type": "hook_additional_context", "hookEvent": "UserPromptSubmit",
					"content": []string{domain.NativeSubmissionContext(fmt.Sprintf("submission-%d", i))}}},
			{"type": "assistant", "sessionId": "native", "uuid": assistantID, "parentUuid": attachmentID,
				"message": map[string]any{"content": []map[string]string{{"type": "text", "text": answers[i]}}, "stop_reason": "end_turn"}},
		} {
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(data))
		}
		parent = assistantID
	}
	return strings.Join(lines, "\n") + "\n"
}

func checkpointEvidence(events ...domain.NativeCheckpointObservation) string {
	evidence := ""
	submission := 0
	for _, event := range events {
		event.Generation = "launch"
		if event.Submission && event.SubmissionID == "" {
			event.SubmissionID = fmt.Sprintf("submission-%d", submission)
			submission++
		}
		evidence = domain.AppendNativeCheckpoint(evidence, "native", event)
	}
	return evidence
}

func TestNativeCheckpointQueuedSubmissionAndDelayedStops(t *testing.T) {
	submit := func(id, text string) domain.NativeCheckpointObservation {
		return domain.NativeCheckpointObservation{Submission: true, PromptID: id, Text: text}
	}
	stop := func(id, text string) domain.NativeCheckpointObservation {
		return domain.NativeCheckpointObservation{PromptID: id, Text: text}
	}
	tests := []struct {
		name             string
		events           []domain.NativeCheckpointObservation
		prompts, answers []string
		wantError        bool
	}{
		{"real queued ID collision", []domain.NativeCheckpointObservation{
			submit("prompt-0", "A"), submit("prompt-0", "B"), stop("prompt-0", "answer A"), stop("prompt-1", "answer B")},
			[]string{"A", "B"}, []string{"answer A", "answer B"}, false},
		{"delayed duplicate Stop", []domain.NativeCheckpointObservation{
			submit("prompt-0", "A"), stop("prompt-0", "answer A"), submit("prompt-1", "B"), stop("prompt-1", "answer B"), stop("prompt-0", "answer A")},
			[]string{"A", "B"}, []string{"answer A", "answer B"}, false},
		{"repeated queued text cannot use anchor twice", []domain.NativeCheckpointObservation{
			submit("prompt-0", "continue"), submit("prompt-0", "continue"), stop("prompt-0", "Done")},
			[]string{"continue"}, []string{"Done"}, true},
		{"repeated queued text with both boundaries", []domain.NativeCheckpointObservation{
			submit("prompt-0", "continue"), submit("prompt-0", "continue"), stop("prompt-0", "Done"), stop("prompt-1", "Done")},
			[]string{"continue", "continue"}, []string{"Done", "Done"}, false},
		{"two queued submissions require two successors", []domain.NativeCheckpointObservation{
			submit("prompt-0", "continue"), submit("prompt-0", "continue"), submit("prompt-0", "continue"), stop("prompt-0", "Done"), stop("prompt-1", "Done")},
			[]string{"continue", "continue"}, []string{"Done", "Done"}, true},
		{"later arriving old Stop cannot erase unflushed C", []domain.NativeCheckpointObservation{
			submit("prompt-0", "A"), stop("prompt-2", "answer C"), stop("prompt-1", "answer B")},
			[]string{"A", "B"}, []string{"answer A", "answer B"}, true},
		{"cross-turn answer rejected", []domain.NativeCheckpointObservation{
			submit("prompt-0", "A"), stop("prompt-1", "answer A")},
			[]string{"A", "B"}, []string{"answer A", "answer B"}, true},
		{"missing Stop ID is unwaivable", []domain.NativeCheckpointObservation{
			submit("prompt-0", "A"), stop("", "answer A")},
			[]string{"A"}, []string{"answer A"}, true},
		{"lost first hook cannot alias queued repeated input", []domain.NativeCheckpointObservation{
			{Submission: true, PromptID: "prompt-0", SubmissionID: "submission-1", Text: "continue"}, stop("prompt-0", "Done")},
			[]string{"continue"}, []string{"Done"}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := verifyCheckpointTranscript(context.Background(),
				strings.NewReader(checkpointFixture(t, test.prompts, test.answers)),
				ports.NativeCheckpointRequest{ProviderConversationID: "native", Evidence: checkpointEvidence(test.events...)})
			if test.wantError {
				if !errors.Is(err, ports.ErrChatHistoryUnsettled) {
					t.Fatalf("admitted incomplete evidence: %+v, %v", got, err)
				}
			} else if err != nil || got.UserMessageID != fmt.Sprintf("user-%d", len(test.prompts)-1) {
				t.Fatalf("boundary=%+v error=%v", got, err)
			}
		})
	}
}

func TestNativeCheckpointRejectsUnprovenAncestry(t *testing.T) {
	source := checkpointFixture(t, []string{"A", "B"}, []string{"answer A", "answer B"})
	evidence := checkpointEvidence(domain.NativeCheckpointObservation{PromptID: "prompt-1", Text: "answer B"})
	for name, transcript := range map[string]string{
		"lagged tail":                   checkpointFixture(t, []string{"A"}, []string{"answer A"}),
		"sidechain":                     strings.ReplaceAll(source, `"promptId":"prompt-1"`, `"isSidechain":true,"promptId":"prompt-1"`),
		"wrong identity":                strings.ReplaceAll(source, `"sessionId":"native"`, `"sessionId":"other"`),
		"broken parent":                 strings.ReplaceAll(source, `"parentUuid":"user-1"`, `"parentUuid":"missing"`),
		"pending tail":                  strings.ReplaceAll(source, `"stop_reason":"end_turn"`, `"stop_reason":"tool_use"`),
		"partial record":                source + `{"uuid":`,
		"missing next native prompt ID": strings.ReplaceAll(source, `"promptId":"prompt-1",`, ""),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := verifyCheckpointTranscript(context.Background(), strings.NewReader(transcript),
				ports.NativeCheckpointRequest{ProviderConversationID: "native", Evidence: evidence})
			if !errors.Is(err, ports.ErrChatHistoryUnsettled) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestNativeSubmissionCannotAttachToEarlierUserWhenNextPromptIDMissing(t *testing.T) {
	source := checkpointFixture(t, []string{"continue", "continue"}, []string{"Done", "Done"})
	source = strings.ReplaceAll(source, `"promptId":"prompt-1",`, "")
	evidence := checkpointEvidence(
		domain.NativeCheckpointObservation{PromptID: "prompt-0", Submission: true, SubmissionID: "submission-1", Text: "continue"},
		domain.NativeCheckpointObservation{PromptID: "prompt-0", Text: "Done"})
	_, err := verifyCheckpointTranscript(context.Background(), strings.NewReader(source),
		ports.NativeCheckpointRequest{ProviderConversationID: "native", Evidence: evidence})
	if !errors.Is(err, ports.ErrChatHistoryUnsettled) {
		t.Fatalf("admitted wrong native user: %v", err)
	}
}

func TestNativeCheckpointIgnoresUnrelatedStringAttachmentContent(t *testing.T) {
	source := checkpointFixture(t, []string{"A"}, []string{"answer A"})
	source += `{"type":"attachment","sessionId":"native","uuid":"hook-success","parentUuid":"assistant-0","attachment":{"type":"hook_success","hookEvent":"Stop","content":"hook succeeded"}}` + "\n"
	source += `{"type":"attachment","sessionId":"native","uuid":"skills","parentUuid":"hook-success","attachment":{"type":"skill_listing","content":"Available skills"}}` + "\n"
	evidence := checkpointEvidence(
		domain.NativeCheckpointObservation{Submission: true, PromptID: "prompt-0", Text: "A"},
		domain.NativeCheckpointObservation{PromptID: "prompt-0", Text: "answer A"})
	got, err := verifyCheckpointTranscript(context.Background(), strings.NewReader(source),
		ports.NativeCheckpointRequest{ProviderConversationID: "native", Evidence: evidence})
	if err != nil || got.UserMessageID != "user-0" {
		t.Fatalf("boundary=%+v error=%v", got, err)
	}
}
