package domain

import (
	"encoding/json"
	"strings"
)

// NativeCheckpointEvidence retains observations, not an inferred prompt/answer
// pair. Provider hooks may be delayed, duplicated, or emitted when queuing input.
// It is persisted atomically with the activity projection and its revision CAS.
type NativeCheckpointEvidence struct {
	NativeID string                        `json:"nativeId"`
	Events   []NativeCheckpointObservation `json:"events"`
	Invalid  bool                          `json:"invalid,omitempty"`
}

// NativeCheckpointObservation names the native execution boundary reported by
// one owned hook. PromptID on a submission may identify its queue anchor.
type NativeCheckpointObservation struct {
	Generation   string `json:"generation"`
	PromptID     string `json:"promptId"`
	SubmissionID string `json:"submissionId,omitempty"`
	Submission   bool   `json:"submission,omitempty"`
	Text         string `json:"text,omitempty"`
	Coordination bool   `json:"coordination,omitempty"`
}

// AppendNativeCheckpoint preserves every unresolved observation. In particular,
// a later Stop must not overwrite an unknown newer Stop. The bound fails closed
// rather than evicting evidence that a truncated transcript could omit.
func AppendNativeCheckpoint(encoded, nativeID string, observation NativeCheckpointObservation) string {
	evidence := NativeCheckpointEvidence{NativeID: nativeID}
	if encoded != "" && json.Unmarshal([]byte(encoded), &evidence) != nil {
		evidence.Invalid = true
	}
	if evidence.NativeID != nativeID || observation.Generation == "" ||
		(observation.Submission && observation.SubmissionID == "") ||
		(!observation.Submission && observation.PromptID == "") {
		evidence.Invalid = true
	}
	if !evidence.Invalid {
		duplicate := false
		for _, old := range evidence.Events {
			if old == observation {
				duplicate = true
				break
			}
		}
		if !duplicate {
			if len(evidence.Events) >= 512 {
				evidence.Invalid = true
			} else {
				evidence.Events = append(evidence.Events, observation)
			}
		}
	}
	data, _ := json.Marshal(evidence) // Only strings/bools: encoding cannot fail.
	return string(data)
}

// NativeSubmissionContext is metadata carried by the provider's prompt hook.
// Its exact attachment to the native user UUID, not hook prompt_id or text,
// identifies a queued submission. It grants no instructions or permissions.
func NativeSubmissionContext(id string) string {
	return "AO transcript correlation ID: " + id + "."
}

// LatestObservedAssistantText is the newest non-submission reply retained in a
// native evidence journal. Claude's TUI stop hook journals that reply and
// withholds it from LatestAssistantUpdate, because a reused prompt id must not
// become a trusted history pair. The text is still what the agent last said,
// so a display summary can read it. Invalid or unreadable evidence yields "".
func LatestObservedAssistantText(encoded string) string {
	if strings.TrimSpace(encoded) == "" {
		return ""
	}
	var evidence NativeCheckpointEvidence
	if json.Unmarshal([]byte(encoded), &evidence) != nil || evidence.Invalid {
		return ""
	}
	for i := len(evidence.Events) - 1; i >= 0; i-- {
		event := evidence.Events[i]
		if event.Submission || event.Coordination {
			continue
		}
		if text := strings.TrimSpace(event.Text); text != "" {
			return text
		}
	}
	return ""
}

// NativeCheckpointTextMatches understands the bounded head/tail representation
// used by hook clients without treating repeated text as a turn identity.
func NativeCheckpointTextMatches(checkpoint, replayed string) bool {
	checkpoint = strings.TrimSpace(checkpoint)
	replayed = strings.TrimSpace(SanitizeControlChars(replayed))
	if checkpoint == replayed {
		return true
	}
	parts := strings.Split(checkpoint, "\n[... truncated by AO ...]\n")
	return len(parts) == 2 && strings.HasPrefix(replayed, parts[0]) && strings.HasSuffix(replayed, parts[1])
}
