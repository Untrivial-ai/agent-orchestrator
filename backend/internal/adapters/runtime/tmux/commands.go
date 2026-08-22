package tmux

import "fmt"

// newSessionArgs builds args for `tmux new-session -d -s <id> -x 220 -y 50
// -c <cwd> <shell> -c <launchCmd>`. The shell -c form runs the launch command
// inside the configured shell so exported env vars and quoting work correctly.
func newSessionArgs(id, cwd, shellPath, launchCmd string) []string {
	return []string{
		"new-session", "-d",
		"-s", id,
		"-x", "220",
		"-y", "50",
		"-c", cwd,
		shellPath, "-c", launchCmd,
	}
}

// respawnPaneArgs replaces the process in the session's agent pane while keeping
// the tmux session and terminal handle intact.
//
// The target must be both index-independent and deterministic, which rules out
// the two obvious spellings. A literal ":0.0" assumes tmux's default base-index
// and pane-base-index of 0, so it fails with "can't find pane: 0" for anyone
// whose tmux.conf sets them to 1. The bare session name is index-independent but
// resolves to whatever pane is *currently active*, so once the user opens a
// second window or splits one (AO hands out a normal attach client with the
// default prefix key), respawn -k would kill the user's shell and leave the dead
// agent pane behind. ":^.{top-left}" names the lowest-numbered window's top-left
// pane by position rather than by index — the pane AO launched the agent in —
// under any base-index. Both tokens predate AO's minimum tmux 3.2.
func respawnPaneArgs(id, cwd, shellPath, launchCmd string) []string {
	return []string{
		"respawn-pane", "-k",
		"-t", agentPaneTarget(id),
		"-c", cwd,
		shellPath, "-c", launchCmd,
	}
}

// setStatusOffArgs hides the tmux status bar for the given session.
// set-option uses pane-targeting syntax which does not accept the `=` prefix,
// so we pass the session name directly.
func setStatusOffArgs(id string) []string {
	return []string{"set-option", "-t", id, "status", "off"}
}

// setMouseOnArgs enables tmux mouse mode so the terminal's SGR mouse-wheel
// reports scroll the pane via copy-mode; without it, wheel scrolling no-ops.
// Pane-targeting, so no `=` prefix (see setStatusOffArgs).
func setMouseOnArgs(id string) []string {
	return []string{"set-option", "-t", id, "mouse", "on"}
}

// setWindowSizeLargestArgs makes tmux size the session's window to the LARGEST
// attached client rather than the most recently active one (the default is
// "latest"). A session can be viewed by several clients at once — e.g. the
// desktop app and the phone. Under "latest", a small phone attaching (or
// becoming active on a session switch) shrinks the shared window for the desktop
// too, giving the desktop a stripped-down view. "largest" ignores smaller
// viewers while a bigger one is attached, so a secondary client can never strip
// down the primary's view; when the big client detaches, tmux recomputes and the
// window follows the remaining largest client. Pane-targeting, so no `=` prefix
// (see setStatusOffArgs).
func setWindowSizeLargestArgs(id string) []string {
	return []string{"set-option", "-t", id, "window-size", "largest"}
}

// agentPaneTarget addresses the pane AO launched the agent in: the lowest-
// numbered window's top-left pane, named by position so it holds under any
// base-index/pane-base-index (see respawnPaneArgs for why the alternatives do
// not). Known ceiling: a user who splits the agent pane with `split-window -b`
// puts a new pane above/left of it, which would move the anchor. Addressing the
// pane by the id tmux assigns at creation would be exact, but that means
// persisting new per-session state; positional targeting covers the splits
// users actually make.
func agentPaneTarget(id string) string {
	return id + ":^.{top-left}"
}

// panePIDArgs returns the pid of tmux's direct pane process. AO walks its
// descendants to find the exact supervisor for the current launch, so this must
// name the agent's pane specifically: resolving to a user-opened pane instead
// yields a pid with no agent descendants, and IsSupervisedProcessAlive then
// reports a perfectly healthy agent as dead.
func panePIDArgs(id string) []string {
	return []string{"display-message", "-p", "-t", agentPaneTarget(id), "#{pane_pid}"}
}

// paneCurrentPathArgs prints tmux's cwd for the session's active pane. Create
// uses this after new-session so a poisoned tmux server that ignores -c fails
// loudly instead of silently starting the agent in the wrong directory.
func paneCurrentPathArgs(id string) []string {
	return []string{"display-message", "-p", "-t", id, "#{pane_current_path}"}
}

// killSessionArgs builds args for `tmux kill-session -t =<id>`. The `=` prefix
// requests exact-name matching so a session "foo" does not accidentally match
// "foobar" (tmux otherwise does unique-prefix matching).
func killSessionArgs(id string) []string {
	return []string{"kill-session", "-t", exactSessionTarget(id)}
}

// hasSessionArgs builds args for `tmux has-session -t =<id>`. The `=` prefix
// requests exact-name matching (see killSessionArgs).
func hasSessionArgs(id string) []string {
	return []string{"has-session", "-t", exactSessionTarget(id)}
}

// exactSessionTarget wraps id in tmux's exact-match prefix `=` so session-
// selection commands (-t) target only the session with that precise name.
// Session-selection commands like kill-session, has-session, and list-panes
// support this prefix; pane-targeting commands (send-keys, capture-pane,
// set-option) use a plain session name.
func exactSessionTarget(id string) string {
	return "=" + id
}

// listPanePIDsArgs builds args for `tmux list-panes -s -t =<id> -F #{pane_pid}`.
// -s lists every pane in the whole session (not just the active window); the
// exact-match target `=` avoids prefix collisions (see killSessionArgs). Each
// #{pane_pid} is the pane's session-leader pid, used to reap the pane's
// descendants when the session is destroyed.
func listPanePIDsArgs(id string) []string {
	return []string{"list-panes", "-s", "-t", exactSessionTarget(id), "-F", "#{pane_pid}"}
}

// sendKeysLiteralArgs builds args for `tmux send-keys -t <id> -l <chunk>`.
// The -l flag stops tmux interpreting words like "Enter" as key names so the
// text is sent verbatim.
func sendKeysLiteralArgs(id, chunk string) []string {
	return []string{"send-keys", "-t", id, "-l", chunk}
}

// sendEnterArgs builds args for `tmux send-keys -t <id> Enter` to submit the
// queued input.
func sendEnterArgs(id string) []string {
	return []string{"send-keys", "-t", id, "Enter"}
}

// sendInterruptArgs builds args for `tmux send-keys -t <id> C-c` to interrupt
// the foreground process without killing the terminal session.
func sendInterruptArgs(id string) []string {
	return []string{"send-keys", "-t", id, "C-c"}
}

// capturePaneArgs builds args for `tmux capture-pane -t <id> -p -S -<lines>`.
// -p prints to stdout; -S -<n> starts n lines back in history.
func capturePaneArgs(id string, lines int) []string {
	return []string{"capture-pane", "-t", id, "-p", "-S", fmt.Sprintf("-%d", lines)}
}

// capturePaneStyledArgs preserves SGR sequences so callers can distinguish a
// dim TUI placeholder from normal human-authored composer text.
func capturePaneStyledArgs(id string, lines int) []string {
	return []string{"capture-pane", "-e", "-t", id, "-p", "-S", fmt.Sprintf("-%d", lines)}
}
