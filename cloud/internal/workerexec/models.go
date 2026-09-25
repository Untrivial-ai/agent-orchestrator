package workerexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// DiscoverCodexModels asks the installed Codex app-server for this worker's
// account-entitled catalog. No catalog is guessed or stored by AO.
func DiscoverCodexModels(ctx context.Context, binary, workspace string) ([]worker.ChatModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if binary == "" {
		binary = "codex"
	}
	command := exec.CommandContext(ctx, binary, "app-server")
	command.Dir = workspace
	command.Stderr = io.Discard
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()
	reader := bufio.NewReader(stdout)
	request := func(id int, method string, params any) (json.RawMessage, error) {
		frame, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		if _, err := stdin.Write(append(frame, '\n')); err != nil {
			return nil, err
		}
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return nil, err
			}
			if len(line) > 2<<20 {
				return nil, errors.New("Codex response is too large")
			}
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &response) != nil || response.ID != id {
				continue
			}
			if response.Error != nil {
				return nil, fmt.Errorf("Codex %s: %s", method, response.Error.Message)
			}
			return response.Result, nil
		}
	}
	if _, err := request(1, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "ao-cloud", "title": "AO Cloud", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(stdin, "{\"method\":\"initialized\",\"params\":{}}\n"); err != nil {
		return nil, err
	}
	models := make([]worker.ChatModel, 0)
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, err := request(page+2, "model/list", params)
		if err != nil {
			return nil, err
		}
		pageModels, next, err := parseCodexModelList(result)
		if err != nil {
			return nil, err
		}
		models = append(models, pageModels...)
		if next == "" {
			return models, nil
		}
		cursor = next
	}
	return nil, errors.New("Codex model catalog has too many pages")
}

func parseCodexModelList(raw json.RawMessage) ([]worker.ChatModel, string, error) {
	var response struct {
		Data []struct {
			ID            string `json:"id"`
			Model         string `json:"model"`
			DisplayName   string `json:"displayName"`
			Description   string `json:"description"`
			IsDefault     bool   `json:"isDefault"`
			Hidden        bool   `json:"hidden"`
			DefaultEffort string `json:"defaultReasoningEffort"`
			Efforts       []struct {
				ReasoningEffort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, "", err
	}
	models := make([]worker.ChatModel, 0, len(response.Data))
	for _, entry := range response.Data {
		if entry.Hidden {
			continue
		}
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			id = strings.TrimSpace(entry.Model)
		}
		if id == "" {
			continue
		}
		name := entry.DisplayName
		if name == "" {
			name = id
		}
		model := worker.ChatModel{
			ID: id, DisplayName: name, Description: entry.Description,
			Default: entry.IsDefault, DefaultEffort: entry.DefaultEffort,
		}
		for _, effort := range entry.Efforts {
			if effort.ReasoningEffort != "" {
				model.Efforts = append(model.Efforts, effort.ReasoningEffort)
			}
		}
		models = append(models, model)
	}
	return models, response.NextCursor, nil
}
