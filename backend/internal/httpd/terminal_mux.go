package httpd

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/terminal"
)

// terminalMuxReadLimit caps a single inbound frame. Client→server frames are small
// (keystrokes, resize, control), so a generous 1 MiB is ample headroom while
// still bounding memory per message.
const terminalMuxReadLimit = 1 << 20

// mountTerminalMux registers the long-lived terminal-multiplexing WebSocket at /mux. It
// is intentionally outside the per-request Timeout middleware (the connection is
// long-lived). When mgr is nil the route is not mounted — the daemon simply has
// no terminal surface yet.
func mountTerminalMux(r chi.Router, mgr *terminal.Manager, log *slog.Logger) {
	if mgr == nil {
		return
	}
	r.Get("/mux", terminalMuxHandler(mgr, log))
}

// terminalMuxHandler upgrades the request to a WebSocket and hands the connection to the
// terminal manager. httpd owns only the upgrade and the transport adaptation;
// all stream logic lives in internal/terminal.
func terminalMuxHandler(mgr *terminal.Manager, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Same-origin check for ambient credentials (cookie, identity).
		// Bearer auth and the loopback-only desktop renderer both use InsecureSkipVerify.
		insecure := true
		authKind := AuthKind(r)
		if authKind == authKindWebSession || authKind == authKindTailscaleIdentity {
			// These are ambient credentials with no SameSite protection.
			// Enforce same-origin to prevent cross-site WebSocket attacks.
			insecure = false
		}

		opts := &websocket.AcceptOptions{InsecureSkipVerify: insecure}

		// Subprotocol handling: if the client sent ao.bearer.<token>, echo it
		// back as the selected subprotocol. This is the escape hatch if
		// cookies don't survive the proxy.
		if raw := r.Header.Get("Sec-WebSocket-Protocol"); bearerFromSubprotocol(raw) != "" {
			opts.Subprotocols = []string{raw}
		}

		c, err := websocket.Accept(w, r, opts)
		if err != nil {
			log.Warn("terminal mux: websocket upgrade failed", "err", err)
			return
		}
		c.SetReadLimit(terminalMuxReadLimit)
		mgr.Serve(r.Context(), &terminalMuxConn{c: c})
	}
}

// bearerFromSubprotocol extracts the bearer token from a Sec-WebSocket-Protocol
// header value. The format is "ao.bearer.<token>". Returns empty if not present.
func bearerFromSubprotocol(proto string) string {
	if strings.HasPrefix(proto, "ao.bearer.") {
		return strings.TrimPrefix(proto, "ao.bearer.")
	}
	return ""
}

// terminalMuxConn adapts a coder/websocket connection to terminal.wsConn. JSON framing
// uses wsjson (text messages); Ping is a control frame; Close sends a normal
// closure.
type terminalMuxConn struct{ c *websocket.Conn }

func (a *terminalMuxConn) ReadJSON(ctx context.Context, v any) error { return wsjson.Read(ctx, a.c, v) }
func (a *terminalMuxConn) WriteJSON(ctx context.Context, v any) error {
	return wsjson.Write(ctx, a.c, v)
}
func (a *terminalMuxConn) Ping(ctx context.Context) error { return a.c.Ping(ctx) }
func (a *terminalMuxConn) Close(reason string) error {
	return a.c.Close(websocket.StatusNormalClosure, reason)
}
