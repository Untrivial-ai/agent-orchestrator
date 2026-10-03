package cline

import (
	"encoding/json"
	"strings"
)

const maxHookSessionIDLen = 256

type hookTaskMetadata struct {
	ULID string `json:"ulid"`
}

type hookTaskLifecycle struct {
	TaskMetadata hookTaskMetadata `json:"taskMetadata"`
}

// SessionIDFromHook returns the native root session id accepted by `cline
// --id`. Current Cline CLI hooks expose it as sessionContext.rootSessionId;
// older lifecycle payloads exposed the same resumable id as taskMetadata.ulid.
//
// The top-level taskId is deliberately ignored. In Cline CLI 3.x it identifies
// an agent conversation (a conv_ value), which is not a resumable CLI session.
func SessionIDFromHook(payload []byte) (string, bool) {
	var input struct {
		SessionContext struct {
			RootSessionID string `json:"rootSessionId"`
		} `json:"sessionContext"`
		TaskStart    hookTaskLifecycle `json:"taskStart"`
		TaskResume   hookTaskLifecycle `json:"taskResume"`
		TaskCancel   hookTaskLifecycle `json:"taskCancel"`
		TaskComplete hookTaskLifecycle `json:"taskComplete"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return "", false
	}
	for _, candidate := range []string{
		input.SessionContext.RootSessionID,
		input.TaskStart.TaskMetadata.ULID,
		input.TaskResume.TaskMetadata.ULID,
		input.TaskCancel.TaskMetadata.ULID,
		input.TaskComplete.TaskMetadata.ULID,
	} {
		id := strings.TrimSpace(candidate)
		if id != "" && len(id) <= maxHookSessionIDLen {
			return id, true
		}
	}
	return "", false
}
