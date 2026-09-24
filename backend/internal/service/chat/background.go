package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// RunBackgroundTask executes a short provider turn without publishing a Chat
// controller or conversation into AO's durable session history.
func (s *Service) RunBackgroundTask(ctx context.Context, harness domain.AgentHarness, cfg ports.ChatStartConfig, prompt string) (_ string, err error) {
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("background task prompt is empty")
	}
	if s.newID == nil {
		return "", errors.New("background task id factory is unavailable")
	}
	driver, err := s.drivers.Driver(harness)
	if err != nil {
		return "", fmt.Errorf("background task driver for %s: %w", harness, err)
	}

	if cfg.SessionID == "" {
		cfg.SessionID = domain.SessionID("background-" + s.newID())
	}
	cfg.ProviderScopeID = s.newID()
	cfg.ProviderIDsScoped = true
	cfg.Ephemeral = true
	conversation, err := driver.Start(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("start background %s task: %w", harness, err)
	}
	defer func() {
		if cleanupErr := cleanupUnpublishedConversation(conversation, true); err == nil && cleanupErr != nil {
			err = fmt.Errorf("stop background %s task: %w", harness, cleanupErr)
		}
	}()
	if cfg.Mode != "" {
		controller, ok := conversation.(ports.ChatConfigOptionController)
		if !ok {
			return "", fmt.Errorf("background %s task cannot select mode %q", harness, cfg.Mode)
		}
		if _, err := controller.SetConfigOption(ctx, "mode", ports.ChatConfigOptionValue{Select: cfg.Mode}); err != nil {
			return "", fmt.Errorf("set background %s mode %q: %w", harness, cfg.Mode, err)
		}
	}

	turn, err := conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text:   prompt,
		Origin: domain.MessageOriginAutomation,
	})
	if err != nil {
		return "", fmt.Errorf("send background %s task: %w", harness, err)
	}
	if deferred, ok := conversation.(ports.ChatDeferredTurnStarter); ok {
		if err := deferred.StartDeferredTurn(turn.ProviderTurnID); err != nil {
			return "", fmt.Errorf("start deferred background %s task: %w", harness, err)
		}
	}

	var answer string
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-conversation.Events():
			if !ok {
				return "", errors.New("background task provider stopped before completing")
			}
			if event.ProviderTurnID != "" && event.ProviderTurnID != turn.ProviderTurnID {
				continue
			}
			switch event.Kind {
			case ports.ChatEventMessageCompleted:
				if text := strings.TrimSpace(event.Text); text != "" {
					answer = text
				}
			case ports.ChatEventApprovalRequested:
				if cfg.OnApproval == nil {
					return "", errors.New("background task requested approval")
				}
				decision, err := cfg.OnApproval(ctx, event)
				if err != nil {
					return "", err
				}
				if err := conversation.ResolveRequest(ctx, event.RequestID, decision); err != nil {
					return "", fmt.Errorf("resolve background approval: %w", err)
				}
			case ports.ChatEventInputRequested:
				return "", errors.New("background task requested structured user input")
			case ports.ChatEventError:
				if event.Err != nil {
					return "", event.Err
				}
				return "", errors.New("background task provider reported an error")
			case ports.ChatEventTurnCompleted:
				if event.TurnState != domain.TurnStateCompleted {
					return "", fmt.Errorf("background task ended with %s", event.TurnState)
				}
				if answer == "" {
					return "", errors.New("background task returned no text")
				}
				return answer, nil
			}
		}
	}
}

// StopBackgroundTask terminates a detached ephemeral host after daemon restart.
func (s *Service) StopBackgroundTask(ctx context.Context, harness domain.AgentHarness, dataDir string, id domain.SessionID) error {
	driver, err := s.drivers.Driver(harness)
	if err != nil {
		return err
	}
	stopper, ok := driver.(ports.ChatDetachedHostTerminator)
	if !ok {
		return ports.ErrChatUnsupported
	}
	return stopper.StopDetachedHost(ctx, dataDir, id)
}
