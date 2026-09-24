package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CardTextSystemPrompt is shared by every harness. The model is used as a
// detached editor, never as the worker, and must return only the requested JSON.
const CardTextSystemPrompt = `You are AO's Kanban progress editor. Convert a batch of worker prose and tool activity into one concise present-progress phrase for a task card. You are not the worker: never continue the task or use tools. The activity batch is the sole source of the current action and topic. A task brief is optional disambiguating context only; never summarize the task brief when the batch identifies what the worker just did. Every phrase must name the current subsystem, behavior, or artifact precisely enough to distinguish it from unrelated concurrent cards. Never use broad subjects such as "the task", "the codebase", "the integration", "the implementation", or "the system". Describe the single dominant intent behind the batch, never the literal command, filename, path, protocol, or output. Use 4 to 10 natural words beginning with exactly one present-participle action verb. Never combine multiple actions. Never copy source text, markdown, XML, JSON, first-person language, or terminal punctuation. Return only the requested JSON object.`

const summaryPrefix = "ao-card-summary:"

// GenerateCardTitle asks the session's configured harness/model for a title.
func (s *Service) GenerateCardTitle(ctx context.Context, id domain.SessionID, prompt string) (string, error) {
	text, err := s.generateCardText(ctx, id, fmt.Sprintf("Return JSON only: {\"title\":\"3 to 7 word title in Title Case\"}.\nTask brief:\n%s", prompt), "title")
	if err != nil {
		return "", err
	}
	return normalizeCardText(text, true), nil
}

// GenerateCardTitleWithConfig is used for TUI sessions, whose worker is not
// backed by a live Chat controller but still has a configured harness/model.
func (s *Service) GenerateCardTitleWithConfig(ctx context.Context, harness domain.AgentHarness, cfg ports.ChatStartConfig, prompt string) (string, error) {
	text, err := s.generateCardTextWithConfig(ctx, harness, cfg, fmt.Sprintf("Return JSON only: {\"title\":\"3 to 7 word title in Title Case\"}.\nTask brief:\n%s", prompt))
	if err != nil {
		return "", err
	}
	return normalizeCardText(text, true), nil
}

// GenerateCardSummary asks the configured model to summarize the latest worker
// response. The call uses a fresh read-only conversation so it cannot pollute
// the worker's transcript or alter its workspace.
func (s *Service) GenerateCardSummary(ctx context.Context, id domain.SessionID, taskBrief, evidence string) (string, error) {
	text, err := s.generateCardText(ctx, id, fmt.Sprintf("Summarize this complete activity batch. Return JSON only: {\"summary\":\"4 to 10 word present-progress action phrase\"}.\nActivity batch (authoritative):\n%s\nOptional task context (use only if the batch has no topic):\n%s", evidence, taskBrief), "summary")
	if err != nil {
		return "", err
	}
	summary := normalizeCardText(text, false)
	if !safeCardSummary(summary) {
		return "", fmt.Errorf("configured model returned rejected card summary %q", summary)
	}
	return summary, nil
}

func safeCardSummary(summary string) bool {
	if summary == "" || strings.ContainsAny(summary, "`<>\n,;:|/") {
		return false
	}
	words := strings.Fields(summary)
	if len(words) < 3 || len(words) > 10 {
		return false
	}
	first, _ := utf8.DecodeRuneInString(summary)
	if !unicode.IsUpper(first) {
		return false
	}
	lower := strings.ToLower(summary)
	for _, generic := range []string{
		"working on the task",
		"working through the requested changes",
		"implementing the requested changes",
		"investigating an implementation issue",
		"inspecting the gmail integration",
		"inspecting the database security model",
		"reviewing the visual system",
		"inspecting the navigation structure",
		"inspecting the authentication flow",
		"inspecting the codebase",
		"inspecting application routes",
		"inspecting route behavior",
	} {
		if lower == generic {
			return false
		}
	}
	// A card is a concise description of active work, not an internal
	// deliberation or a sentence about the agent's next decision.
	for _, unsuitable := range []string{"reconsidering", "considering", "deciding", "planning", "thinking"} {
		if strings.HasPrefix(lower, unsuitable+" ") {
			return false
		}
	}
	for _, marker := range []string{"grep ", "glob ", "read file", "find ", "ls ", "/users/", "tool call", "json", "**", " | ", "\t"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	actions := 0
	for _, word := range words {
		word = strings.ToLower(strings.Trim(word, "()[]{}.!?\"'"))
		if strings.HasSuffix(word, "ing") {
			actions++
		}
	}
	if actions != 1 || !strings.HasSuffix(strings.ToLower(words[0]), "ing") {
		return false
	}
	return true
}

func (s *Service) generateCardText(ctx context.Context, id domain.SessionID, request, field string) (string, error) {
	s.mu.RLock()
	base, ok := s.startConfigs[domain.SessionConversationOwner(id)]
	s.mu.RUnlock()
	if !ok {
		return "", errors.New("chat session configuration unavailable")
	}
	return s.generateCardTextWithConfig(ctx, base.Harness, ports.ChatStartConfig{SessionID: base.SessionID, DataDir: base.DataDir, WorkspacePath: base.WorkspacePath, Env: base.Env, Model: base.Model, Effort: base.Effort, Permissions: base.Permissions, ReadOnly: base.ReadOnly, SystemPrompt: base.SystemPrompt, AdditionalDirectories: base.AdditionalDirectories, MCPServers: base.MCPServers}, request)
}

func (s *Service) generateCardTextWithConfig(ctx context.Context, harness domain.AgentHarness, base ports.ChatStartConfig, request string) (string, error) {
	driver, err := s.drivers.Driver(harness)
	if err != nil {
		return "", err
	}
	// Card text requests may overlap when a worker remains active. Give each
	// detached editor its own provider host and ownership scope, then explicitly
	// terminate it. Reusing one -card-text identity races the active editor and
	// causes persistent ACP to reject every later request as already attached.
	requestID := uuid.NewString()
	base.SessionID = domain.SessionID(string(base.SessionID) + "-card-text-" + requestID)
	base.ProviderScopeID = "card-text-" + requestID
	base.ProviderIDsScoped = true
	base.ReadOnly = true
	base.Permissions = ports.PermissionModeAuto
	base.SystemPrompt = CardTextSystemPrompt
	conv, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: base.SessionID, DataDir: base.DataDir, WorkspacePath: base.WorkspacePath,
		Env: base.Env, Model: base.Model, Effort: base.Effort, Permissions: base.Permissions,
		ReadOnly: true, SystemPrompt: CardTextSystemPrompt, AdditionalDirectories: base.AdditionalDirectories,
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
				return "", errors.New("card text provider ended before completion")
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
					return "", errors.New("card text provider returned no message")
				}
				return out.String(), nil
			}
		}
	}
}

func normalizeCardText(raw string, title bool) string {
	raw = strings.TrimSpace(raw)
	var obj map[string]string
	if json.Unmarshal([]byte(raw), &obj) == nil {
		if title {
			raw = obj["title"]
		} else {
			raw = obj["summary"]
		}
	}
	raw = strings.TrimSpace(strings.Trim(raw, "`\"'"))
	if i := strings.IndexByte(raw, '\n'); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	for strings.HasSuffix(raw, ".") {
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "."))
	}
	if title {
		raw = domain.TitleCaseSessionTitle(raw)
	}
	return raw
}
