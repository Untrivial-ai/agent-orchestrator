// Package clibrowser is the shared `ao browser` verb tree used by the desktop
// CLI and the cloud in-VM CLI. Hosts supply the transport; everything else
// (verbs, argument validation, output shaping, untrusted-content markers) is
// identical everywhere.
package clibrowser

import (
	"context"
	"time"
)

// Status is the transport-level browser runtime state.
type Status struct {
	SessionID   string    `json:"sessionId"`
	Connected   bool      `json:"connected"`
	ConnectedAt time.Time `json:"connectedAt,omitempty"`
	Transport   string    `json:"transport"`
}

// Response is one executed browser command's correlated result.
type Response struct {
	RequestID string         `json:"requestId"`
	SessionID string         `json:"sessionId"`
	Action    string         `json:"action"`
	Result    map[string]any `json:"result"`
}

// CommandRequest is the wire request body both hosts send. It exists as a
// typed struct (not an inline map) so wire-shape tests keep asserting the
// exact json tags against the daemon contract.
type CommandRequest struct {
	SessionID string         `json:"sessionId"`
	Action    string         `json:"action"`
	Args      map[string]any `json:"args,omitempty"`
}

// Transport carries browser commands to the session's browser service.
type Transport interface {
	BrowserAction(ctx context.Context, action string, args map[string]any) (Response, error)
	BrowserStatus(ctx context.Context) (Status, error)
}

// Clock supplies the timestamp used for default screenshot filenames.
type Clock func() time.Time
