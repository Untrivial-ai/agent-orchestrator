// Package terminal is the live-terminal streaming feature: each WebSocket
// client that opens a pane gets its OWN attach Stream, piped over a
// ch-tagged wire protocol, alongside a session-state channel fed by the CDC
// broadcaster.
//
// The runtime is selected by runtimeselect: tmux on Linux and for legacy macOS
// handles, a detached native PTY host for new macOS handles, and ConPTY on
// Windows. tmux spawns an attach CLI on a local PTY via ptyexec; detached hosts
// use a loopback connection and framing protocol directly (no attach CLI).
//
// Per-client attach (no shared PTY, no replay buffer): the multiplexer owns the
// session's screen state and scrollback, and answers every fresh attach with
// its full init handshake (alt screen, SGR mouse tracking, bracketed paste)
// followed by a faithful repaint. Sharing one PTY and replaying a bounded byte
// ring to late subscribers loses exactly that handshake (it is emitted once, at
// the head of the stream), which left clients without mouse reporting (wheel
// scroll dead). Spawning a fresh attach per client makes the runtime re-send
// it, every time, by construction. The cost is one client process (tmux) or one
// loopback connection (conpty) per open pane per connection.
//
// Boundaries (see docs/architecture.md):
//
//   - This package owns the product workflow: per-client PTY attach, liveness
//     gating, re-attach resilience, and the ch-tagged wire protocol. It is
//     transport-agnostic: it speaks to a small wsConn interface, not to any
//     concrete WebSocket library.
//   - internal/httpd owns the HTTP/WebSocket upgrade and adapts the accepted
//     socket to wsConn; it does not contain stream logic.
//   - The PTY itself is reached through PTYSource (satisfied by the selected
//     runtime adapter's AttachCommand/IsAlive) and spawned through an injectable
//     spawnFunc, so the attach and re-attach logic test without a real process,
//     runtime, or network.
//
// Raw PTY bytes never flow through the CDC change_log; only the session channel
// is fed by cdc.Broadcaster. Terminal output is high-volume ephemeral data and
// goes straight from the PTY to the socket.
package terminal
