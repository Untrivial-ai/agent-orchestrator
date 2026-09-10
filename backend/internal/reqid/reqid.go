// Package reqid reads the HTTP request id off a context.
//
// Telemetry emitters deliberately detach from the request context
// (context.Background) so a cancelled request cannot drop an event for work
// that already happened. Reading the id at the emit site, before that detach,
// is what keeps daemon-side events joinable to the HTTP request that caused
// them.
package reqid

import (
	"context"

	"github.com/go-chi/chi/v5/middleware"
)

// FromContext returns the request id chi's RequestID middleware put on ctx, or
// "" when ctx is not request-scoped (daemon startup, reaper ticks, tests).
func FromContext(ctx context.Context) string { return middleware.GetReqID(ctx) }
