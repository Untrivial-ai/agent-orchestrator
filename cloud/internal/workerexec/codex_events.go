package workerexec

import (
	"encoding/json"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// projectCodexNotification keeps provider frames at the worker boundary. Only
// fields understood by the shared Chat timeline cross into the control plane.
func projectCodexNotification(frame codexFrame, threadID string) []Output {
	var envelope struct {
		ThreadID     string `json:"threadId"`
		ItemID       string `json:"itemId"`
		Delta        string `json:"delta"`
		SummaryIndex int    `json:"summaryIndex"`
		Diff         string `json:"diff"`
		Item         struct {
			ID               string          `json:"id"`
			Type             string          `json:"type"`
			Command          string          `json:"command"`
			Cwd              string          `json:"cwd"`
			AggregatedOutput string          `json:"aggregatedOutput"`
			ExitCode         *int            `json:"exitCode"`
			Summary          []string        `json:"summary"`
			Server           string          `json:"server"`
			Tool             string          `json:"tool"`
			Namespace        string          `json:"namespace"`
			Query            string          `json:"query"`
			Arguments        json.RawMessage `json:"arguments"`
			Result           json.RawMessage `json:"result"`
			Success          *bool           `json:"success"`
			Error            *struct {
				Message string `json:"message"`
			} `json:"error"`
			Changes []struct {
				Path string          `json:"path"`
				Diff string          `json:"diff"`
				Kind json.RawMessage `json:"kind"`
			} `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal(frame.Params, &envelope) != nil || envelope.ThreadID != threadID {
		return nil
	}
	activity := func(id, kind, status, summary string, detail map[string]any) []Output {
		if id == "" {
			return nil
		}
		return []Output{{Activity: &worker.ChatActivity{ID: id, Kind: kind, Status: status, Summary: summary, Detail: detail}}}
	}
	switch frame.Method {
	case "item/agentMessage/delta":
		if envelope.Delta != "" {
			var outputs []Output
			for _, chunk := range codexChunks(envelope.Delta) {
				outputs = append(outputs, Output{Stream: "stdout", Text: chunk, ItemID: envelope.ItemID})
			}
			return outputs
		}
	case "item/reasoning/summaryTextDelta":
		if envelope.Delta != "" {
			var outputs []Output
			for _, chunk := range codexChunks(envelope.Delta) {
				outputs = append(outputs, activity(envelope.ItemID, "reasoning", "running", "Reasoning", map[string]any{"textDelta": chunk})...)
			}
			return outputs
		}
	case "item/reasoning/summaryPartAdded":
		if envelope.SummaryIndex > 0 {
			return activity(envelope.ItemID, "reasoning", "running", "Reasoning", map[string]any{"textDelta": "\n\n"})
		}
	case "item/commandExecution/outputDelta":
		if envelope.Delta != "" {
			var outputs []Output
			for _, chunk := range codexChunks(envelope.Delta) {
				outputs = append(outputs, activity(envelope.ItemID, "command", "running", "Ran a command", map[string]any{"outputDelta": chunk})...)
			}
			return outputs
		}
	case "turn/diff/updated":
		diff := envelope.Diff
		truncated := len(diff) > 8<<10
		if truncated {
			diff = diff[:8<<10]
		}
		return activity("turn-diff", "turn_diff", "completed", "Changed files", map[string]any{"diff": diff, "truncated": truncated})
	case "item/started", "item/completed":
		status := "running"
		if frame.Method == "item/completed" {
			status = "completed"
		}
		item := envelope.Item
		switch item.Type {
		case "commandExecution":
			if item.ExitCode != nil && *item.ExitCode != 0 {
				status = "failed"
			}
			detail := map[string]any{"command": boundedCodexText(item.Command), "rawCommand": boundedCodexText(item.Command), "cwd": boundedCodexText(item.Cwd)}
			if frame.Method == "item/completed" {
				if item.AggregatedOutput != "" {
					detail["output"] = boundedCodexText(item.AggregatedOutput)
					detail["outputSource"] = "aggregate"
					detail["outputMayBePartial"] = true
				}
				if item.ExitCode != nil {
					detail["exitCode"] = *item.ExitCode
				}
			}
			summary := strings.TrimSpace(item.Command)
			if summary == "" {
				summary = "Ran a command"
			}
			return activity(item.ID, "command", status, summary, detail)
		case "reasoning":
			detail := map[string]any{}
			if len(item.Summary) > 0 {
				detail["text"] = boundedCodexText(strings.Join(item.Summary, "\n\n"))
			}
			return activity(item.ID, "reasoning", status, "Reasoning", detail)
		case "fileChange":
			files := make([]map[string]any, 0, min(len(item.Changes), 8))
			for _, change := range item.Changes {
				if change.Path == "" || len(files) >= 8 {
					break
				}
				kind := "modified"
				var shape struct {
					Type     string `json:"type"`
					MovePath string `json:"move_path"`
				}
				_ = json.Unmarshal(change.Kind, &shape)
				switch shape.Type {
				case "add":
					kind = "added"
				case "delete":
					kind = "deleted"
				}
				path := boundedCodexField(change.Path)
				file := map[string]any{"path": path, "status": kind}
				if shape.MovePath != "" {
					file["oldPath"] = path
					file["path"] = boundedCodexField(shape.MovePath)
					file["status"] = "renamed"
				}
				adds, deletes := codexPatchCounts(change.Diff, kind)
				file["additions"], file["deletions"] = adds, deletes
				file["patch"] = boundedCodexPatch(change.Diff)
				if len(change.Diff) > 512 {
					file["patchTruncated"] = true
				}
				files = append(files, file)
			}
			return activity(item.ID, "file_change", status, "Edited files", map[string]any{"files": files, "filesTruncated": len(item.Changes) > len(files)})
		case "mcpToolCall":
			if item.Success != nil && !*item.Success || item.Error != nil {
				status = "failed"
			}
			detail := map[string]any{"server": boundedCodexField(item.Server), "toolName": boundedCodexField(item.Tool), "namespace": boundedCodexField(item.Namespace)}
			if len(item.Arguments) > 0 {
				detail["arguments"] = boundedCodexJSON(item.Arguments)
			}
			if len(item.Result) > 0 {
				detail["result"] = boundedCodexJSON(item.Result)
			}
			if item.Error != nil {
				detail["error"] = boundedCodexText(item.Error.Message)
			}
			if item.Success != nil {
				detail["success"] = *item.Success
			}
			return activity(item.ID, "mcp_tool", status, "Called "+boundedCodexField(item.Server+"/"+item.Tool), detail)
		case "webSearch":
			summary := "Searched the web"
			if item.Query != "" {
				summary += " for " + boundedCodexField(item.Query)
			}
			return activity(item.ID, "command", status, summary, map[string]any{"text": boundedCodexField(item.Query)})
		}
	}
	return nil
}

func boundedCodexText(value string) string {
	if len(value) > 8<<10 {
		return value[:8<<10]
	}
	return value
}

func boundedCodexField(value string) string {
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func boundedCodexPatch(value string) string {
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func boundedCodexJSON(raw json.RawMessage) any {
	if len(raw) > 4096 {
		return map[string]any{"truncated": true, "bytes": len(raw)}
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func codexChunks(value string) []string {
	const max = 8 << 10
	var chunks []string
	for len(value) > max {
		chunks = append(chunks, value[:max])
		value = value[max:]
	}
	if value != "" {
		chunks = append(chunks, value)
	}
	return chunks
}

func codexPatchCounts(patch, status string) (int, int) {
	if patch == "" {
		return 0, 0
	}
	if !strings.Contains(patch, "@@") {
		lines := strings.Count(strings.TrimSuffix(patch, "\n"), "\n") + 1
		if status == "added" {
			return lines, 0
		}
		if status == "deleted" {
			return 0, lines
		}
		return 0, 0
	}
	adds, deletes, hunk := 0, 0, false
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			hunk = true
			continue
		}
		if !hunk {
			continue
		}
		if strings.HasPrefix(line, "+") {
			adds++
		}
		if strings.HasPrefix(line, "-") {
			deletes++
		}
	}
	return adds, deletes
}
