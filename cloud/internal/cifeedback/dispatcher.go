package cifeedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

type Store interface {
	ClaimCIFeedback(context.Context, string, time.Duration) (domain.CIFeedback, bool, error)
	EnsureWorkerAgentTerminal(context.Context, string, string, string, int64, time.Duration) (domain.TerminalSession, error)
	QueueTerminalInput(context.Context, domain.TerminalSession, string, []byte) error
	CompleteCIFeedback(context.Context, string, string) error
	RetryCIFeedback(context.Context, string, string, string, time.Time) error
}

type Config struct {
	Owner         string
	LeaseDuration time.Duration
	PollInterval  time.Duration
	TerminalTTL   time.Duration
	Now           func() time.Time
	Logger        *slog.Logger
}

type Dispatcher struct {
	store  Store
	config Config
}

func New(store Store, config Config) *Dispatcher {
	if config.Owner == "" {
		config.Owner = fmt.Sprintf("ci-feedback-%d", time.Now().UnixNano())
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = 30 * time.Second
	}
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.TerminalTTL <= 0 {
		config.TerminalTTL = 24 * time.Hour
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Dispatcher{store: store, config: config}
}

func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.config.PollInterval)
	defer ticker.Stop()
	for {
		if err := d.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			d.config.Logger.Warn("dispatch CI feedback", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) RunOnce(ctx context.Context) error {
	item, found, err := d.store.ClaimCIFeedback(ctx, d.config.Owner, d.config.LeaseDuration)
	if err != nil || !found {
		return err
	}
	retry := func(cause error) error {
		retryErr := d.store.RetryCIFeedback(ctx, item.ID, d.config.Owner, cause.Error(), d.config.Now().Add(time.Second*time.Duration(min(item.AttemptCount+1, 60))))
		return errors.Join(cause, retryErr)
	}
	if item.WorkerID == "" || item.WorkerEpoch <= 0 {
		return retry(errors.New("session worker is not connected"))
	}
	terminal, err := d.store.EnsureWorkerAgentTerminal(ctx, item.OrgID, item.SessionID, item.WorkerID, item.WorkerEpoch, d.config.TerminalTTL)
	if err != nil {
		return retry(err)
	}
	prompt, err := feedbackPrompt(item.Payload)
	if err != nil {
		return retry(err)
	}
	if err := d.store.QueueTerminalInput(ctx, terminal, item.ApplicationKey, worker.EncodeTerminalInput(prompt)); err != nil {
		return retry(err)
	}
	return d.store.CompleteCIFeedback(ctx, item.ID, d.config.Owner)
}

func feedbackPrompt(payload json.RawMessage) (string, error) {
	var input struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &input) != nil || strings.TrimSpace(input.Message) == "" {
		return "", errors.New("invalid CI feedback payload")
	}
	return strings.TrimSpace(input.Message), nil
}

// Compile-time assertion that the production store satisfies the dispatcher.
var _ Store = (*postgres.Store)(nil)
