// Package requestscope carries transport facts that cannot be trusted from
// client-controlled headers. The LAN listener marks requests before they enter
// the shared API router.
package requestscope

import "context"

type lanKey struct{}

// WithLAN marks a request context as arriving through the authenticated LAN
// listener rather than the loopback desktop listener.
func WithLAN(ctx context.Context) context.Context {
	return context.WithValue(ctx, lanKey{}, true)
}

// IsLAN reports whether the physical LAN listener marked this request.
func IsLAN(ctx context.Context) bool {
	value, _ := ctx.Value(lanKey{}).(bool)
	return value
}
