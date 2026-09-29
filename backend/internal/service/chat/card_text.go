package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CardTitleSystemPrompt is used only for a new TUI card's one-time title.
// Live progress summaries are inferred locally and never start a model host.
const CardTitleSystemPrompt = `You write concise task titles. Convert the task brief into a specific 3 to 7 word title in Title Case. Do not copy a long input verbatim. Do not carry out the task or use tools. Return only JSON with a title field.`

// GenerateCardTitleWithConfig is used for TUI sessions, whose worker is not
// backed by a live Chat controller but still has a configured harness/model.
func (s *Service) GenerateCardTitleWithConfig(ctx context.Context, harness domain.AgentHarness, cfg ports.ChatStartConfig, prompt string) (string, error) {
	text, err := s.generateCardTitleWithConfig(ctx, harness, cfg, fmt.Sprintf("Return JSON only: {\"title\":\"3 to 7 word title in Title Case\"}.\nTask brief:\n%s", prompt))
	if err != nil {
		return "", err
	}
	return normalizeCardTitle(text), nil
}

func (s *Service) generateCardTitleWithConfig(ctx context.Context, harness domain.AgentHarness, base ports.ChatStartConfig, request string) (string, error) {
	driver, err := s.drivers.Driver(harness)
	if err != nil {
		return "", err
	}
	// Title requests may overlap when workers start together. Give each
	// detached editor its own provider host and ownership scope, then explicitly
	// terminate it. Reusing one title identity races the active editor and
	// causes persistent ACP to reject every later request as already attached.
	requestID := uuid.NewString()
	base.SessionID = domain.SessionID(string(base.SessionID) + "-card-title-" + requestID)
	base.ProviderScopeID = "card-title-" + requestID
	base.ProviderIDsScoped = true
	base.Permissions = ports.PermissionModeAuto
	conv, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: base.SessionID, DataDir: base.DataDir, WorkspacePath: base.WorkspacePath,
		Env: base.Env, Model: base.Model, Effort: base.Effort, Permissions: base.Permissions,
		ReadOnly: true, SystemPrompt: CardTitleSystemPrompt, AdditionalDirectories: base.AdditionalDirectories,
		MCPServers: base.MCPServers, ProviderScopeID: base.ProviderScopeID, ProviderIDsScoped: base.ProviderIDsScoped,
	})
	if err != nil {
		return "", err
	}
	defer func() {
		if terminator, ok := conv.(ports.ChatProviderTerminator); ok {
			_ = terminator.Terminate()
			return
		}
		_ = conv.Close()
	}()
	turn, err := conv.SendTurn(ctx, ports.ChatUserMessage{Text: request, Origin: domain.MessageOriginDaemon})
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-conv.Events():
			if !ok {
				return "", errors.New("card title provider ended before completion")
			}
			if event.ProviderTurnID != "" && turn.ProviderTurnID != "" && event.ProviderTurnID != turn.ProviderTurnID {
				continue
			}
			if event.Kind == ports.ChatEventMessageDelta {
				out.WriteString(event.Delta)
			}
			if event.Kind == ports.ChatEventMessageCompleted {
				if event.Text != "" {
					out.Reset()
					out.WriteString(event.Text)
				}
				return out.String(), nil
			}
			if event.Kind == ports.ChatEventTurnCompleted {
				if event.Err != nil {
					return "", event.Err
				}
				if strings.TrimSpace(out.String()) == "" {
					return "", errors.New("card title provider returned no message")
				}
				return out.String(), nil
			}
		}
	}
}

func normalizeCardTitle(raw string) string {
	raw = strings.TrimSpace(raw)
	var obj map[string]string
	if json.Unmarshal([]byte(raw), &obj) == nil {
		raw = obj["title"]
	}
	raw = strings.TrimSpace(strings.Trim(raw, "`\"'"))
	if i := strings.IndexByte(raw, '\n'); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	for strings.HasSuffix(raw, ".") {
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "."))
	}
	return domain.TitleCaseSessionTitle(raw)
}
