package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// NotificationHandler receives one NOTIFY payload. Handlers are invoked on
// their own goroutine and may perform store calls.
type NotificationHandler func(payload string)

// Listener holds one dedicated Postgres connection subscribed to NOTIFY
// channels and dispatches payloads to registered handlers. It reconnects
// with backoff; notifications raised while disconnected are lost, which is
// acceptable for the terminal fast path — the polling loops it accelerates
// remain in place as the correctness fallback.
type Listener struct {
	url      string
	logger   *slog.Logger
	handlers map[string]NotificationHandler
}

func NewListener(databaseURL string, logger *slog.Logger) *Listener {
	return &Listener{
		url:      databaseURL,
		logger:   logger,
		handlers: make(map[string]NotificationHandler),
	}
}

// Handle registers a channel handler. All registrations must happen before
// Run is called.
func (l *Listener) Handle(channel string, handler NotificationHandler) {
	l.handlers[channel] = handler
}

// Run listens until ctx is canceled. It never returns a terminal error for
// connection trouble; it logs and redials instead.
func (l *Listener) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		if err := l.listen(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			l.logger.Warn("terminal notify listener disconnected", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (l *Listener) listen(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, l.url)
	if err != nil {
		return fmt.Errorf("connect notify listener: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	for channel := range l.handlers {
		if _, err := conn.Exec(ctx, fmt.Sprintf(`LISTEN %q`, channel)); err != nil {
			return fmt.Errorf("listen %s: %w", channel, err)
		}
	}
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if handler := l.handlers[notification.Channel]; handler != nil {
			go handler(notification.Payload)
		}
	}
}
