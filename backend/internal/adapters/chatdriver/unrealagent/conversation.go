package unrealagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const eventBuffer = 4096

type conversation struct {
	cfg          providerConfig
	transport    *persistenthost.Transport
	log          *slog.Logger
	reconnected  bool
	shutdownHost func(context.Context, string, string) error

	events chan ports.ChatEvent
	ready  chan error

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan error
	closed  bool

	closeOnce sync.Once
	closeErr  error
}

func newConversation(
	cfg providerConfig,
	transport *persistenthost.Transport,
	log *slog.Logger,
	shutdownHost func(context.Context, string, string) error,
) *conversation {
	if shutdownHost == nil {
		shutdownHost = persistenthost.Shutdown
	}
	c := &conversation{
		cfg: cfg, transport: transport, log: log, reconnected: transport.Reconnected,
		shutdownHost: shutdownHost,
		events:       make(chan ports.ChatEvent, eventBuffer), ready: make(chan error, 1),
		pending: make(map[string]chan error),
	}
	go c.pump()
	return c
}

var _ ports.ChatConversation = (*conversation)(nil)
var _ ports.ChatProviderPreserver = (*conversation)(nil)
var _ ports.ChatProviderTerminator = (*conversation)(nil)
var _ ports.ChatLiveReconnector = (*conversation)(nil)
var _ ports.ChatProviderEventAcknowledger = (*conversation)(nil)

func (c *conversation) ProviderConversationID() string     { return c.cfg.ProviderConversationID }
func (*conversation) Capabilities() ports.ChatCapabilities { return capabilities() }
func (c *conversation) Events() <-chan ports.ChatEvent     { return c.events }
func (*conversation) PreservesProviderOnClose() bool       { return true }
func (c *conversation) ReconnectedLive() bool              { return c.reconnected }

func (c *conversation) SendTurn(ctx context.Context, msg ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	if len(msg.Content) != 0 {
		return ports.ChatTurnRef{}, errors.New("unreal agent does not accept image or resource attachments")
	}
	if model := strings.TrimSpace(msg.Settings.Model); model != "" && model != c.cfg.Model {
		return ports.ChatTurnRef{}, errors.New("unreal agent cannot change model inside a running conversation")
	}
	if approval := msg.Settings.Approval; approval != "" && ports.NormalizePermissionMode(approval) != ports.PermissionModeBypassPermissions {
		return ports.ChatTurnRef{}, errors.New("unreal agent turns require bypass-permissions")
	}
	effort := strings.TrimSpace(msg.Settings.Effort)
	if effort != "" && normalizeEffort(effort) != effort {
		return ports.ChatTurnRef{}, fmt.Errorf("unsupported Unreal Agent effort %q", effort)
	}
	turnID := strings.TrimSpace(msg.ClientMessageID)
	if turnID == "" {
		turnID = uuid.NewString()
	}
	if err := c.request(ctx, command{
		Type: "turn", ProviderTurnID: turnID, MessageID: turnID,
		Text: msg.Text, Effort: effort,
	}); err != nil {
		return ports.ChatTurnRef{}, err
	}
	return ports.ChatTurnRef{ProviderTurnID: turnID}, nil
}

func (c *conversation) Interrupt(ctx context.Context, providerTurnID string) error {
	return c.request(ctx, command{Type: "interrupt", ProviderTurnID: strings.TrimSpace(providerTurnID)})
}

func (*conversation) ResolveRequest(context.Context, string, ports.ChatDecision) error {
	return errors.New("unreal agent has no interactive approval requests")
}

func (c *conversation) AcknowledgeProviderEvent(ctx context.Context, eventID string) error {
	if strings.TrimSpace(eventID) == "" {
		return nil
	}
	return c.request(ctx, command{Type: "ack", EventID: eventID})
}

func (c *conversation) waitReady(ctx context.Context) error {
	select {
	case err := <-c.ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *conversation) request(ctx context.Context, cmd command) error {
	cmd.Version = protocolVersion
	cmd.RequestID = uuid.NewString()
	result := make(chan error, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return io.ErrClosedPipe
	}
	c.pending[cmd.RequestID] = result
	c.mu.Unlock()

	encoded, err := json.Marshal(cmd)
	if err == nil {
		encoded = append(encoded, '\n')
		c.writeMu.Lock()
		_, err = c.transport.Stdin.Write(encoded)
		c.writeMu.Unlock()
	}
	if err != nil {
		c.finishRequest(cmd.RequestID, err)
		return err
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, cmd.RequestID)
		c.mu.Unlock()
		return ctx.Err()
	}
}

func (c *conversation) pump() {
	defer close(c.events)
	reader := bufio.NewReader(c.transport.Stdout)
	readyDelivered := false
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) != 0 {
			var incoming frame
			if decodeErr := json.Unmarshal(line, &incoming); decodeErr != nil {
				c.log.Warn("discarding invalid Unreal Agent frame", "error", decodeErr)
			} else if incoming.Version != protocolVersion {
				versionErr := fmt.Errorf("unreal agent protocol version %d is incompatible with AO version %d", incoming.Version, protocolVersion)
				if !readyDelivered {
					c.ready <- versionErr
				}
				c.fail(versionErr)
				c.emit(ports.ChatEvent{
					Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerStopped,
					Err: versionErr,
				})
				return
			} else {
				switch incoming.Type {
				case "ready":
					if !readyDelivered {
						readyDelivered = true
						c.ready <- nil
					}
				case "result":
					var resultErr error
					if incoming.Error != "" {
						resultErr = errors.New(incoming.Error)
					}
					c.finishRequest(incoming.RequestID, resultErr)
				case "event":
					if incoming.Event != nil {
						c.emit(incoming.Event.chatEvent(incoming.EventID))
					}
				}
			}
		}
		if err != nil {
			if !readyDelivered {
				c.ready <- err
			}
			c.failPending(err)
			c.emit(ports.ChatEvent{
				Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerStopped,
				Err: err,
			})
			return
		}
	}
}

func (c *conversation) emit(event ports.ChatEvent) {
	switch event.Kind {
	case ports.ChatEventMessageDelta, ports.ChatEventReasoningDelta, ports.ChatEventCommandOutputDelta, ports.ChatEventActivityText:
		select {
		case c.events <- event:
		default:
		}
	default:
		c.events <- event
	}
}

func (c *conversation) finishRequest(id string, err error) {
	c.mu.Lock()
	result := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if result != nil {
		result <- err
	}
}

func (c *conversation) failPending(err error) {
	c.mu.Lock()
	c.closed = true
	pending := c.pending
	c.pending = make(map[string]chan error)
	c.mu.Unlock()
	for _, result := range pending {
		result <- err
	}
}

func (c *conversation) fail(err error) {
	c.failPending(err)
	_ = c.transport.Stdin.Close()
}

func (c *conversation) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.closeErr = c.transport.Stdin.Close()
	})
	return c.closeErr
}

func (c *conversation) Terminate() error {
	_ = c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.shutdownHost(ctx, c.cfg.DataDir, c.cfg.AOSessionID)
}
