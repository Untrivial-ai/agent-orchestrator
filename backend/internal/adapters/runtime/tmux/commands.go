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

// respawnPaneArgs replaces the process in the session's only pane while keeping
// the tmux session and terminal handle intact. The bare session target resolves
// to the active window/pane regardless of the user's base-index /
// pane-base-index (a hardcoded :0.0 misses when either is 1, see #4656).
func respawnPaneArgs(id, cwd, shellPath, launchCmd string) []string {
	return []string{
		"respawn-pane", "-k",
		"-t", id,
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

// setDetachOnDestroyOnArgs makes tmux detach (exit) the attached client when
// the session is destroyed instead of moving it onto another session. That is
// tmux's default, but a user tmux.conf with `set -g detach-on-destroy off`
// applies to AO's server too, and AO's attach client would then be reparented
// onto one of the user's own sessions when an AO session is destroyed: the
// embedded terminal keeps streaming and every keystroke leaks into that
// session (issue #4223). A session-scoped option overrides the global one for
// AO-owned sessions only.
//
// Unlike the other pane-targeting calls in this file, this one uses the exact-
// match target `=<id>:` rather than a plain session name. This call can run
// long after the session was created (on every Destroy, and on first legacy-
// socket adoption), by which point the session may already be gone: a plain
// target then falls back to tmux's unique-prefix matching and can silently
// re-target a different, unrelated session whose name happens to start with
// this one (verified against real tmux: after killing session "foo", `tmux
// set-option -t foo ...` silently retargeted the unrelated session "foobar").
// `=<id>` alone is rejected by pane-targeting commands like set-option (it
// needs a window/pane component); appending the empty `:` supplies one and
// keeps the match exact, letting the window/pane default to the current one.
func setDetachOnDestroyOnArgs(id string) []string {
	return []string{"set-option", "-t", exactSessionTarget(id) + ":", "detach-on-destroy", "on"}
}

// showDetachOnDestroyArgs reads back the session-scoped detach-on-destroy
// value tmux actually holds for id, so callers can confirm setDetachOnDestroyOnArgs
// took effect instead of trusting its exit code alone (see enforceDetachOnDestroy).
// Same exact-match target as setDetachOnDestroyOnArgs, for the same reason.
func showDetachOnDestroyArgs(id string) []string {
	return []string{"show-options", "-t", exactSessionTarget(id) + ":", "-v", "detach-on-destroy"}
}

// panePIDArgs returns the pid of tmux's direct pane process. AO walks its
// descendants to find the exact supervisor for the current launch. The bare
// session target keeps this independent of base-index / pane-base-index
// (see respawnPaneArgs, #4656).
func panePIDArgs(id string) []string {
	return []string{"display-message", "-p", "-t", id, "#{pane_pid}"}
}

// paneDeadArgs includes every pane so a retained dead pane cannot hide another
// running child in the same runtime. Session targeting requires an exact match.
func paneDeadArgs(id string) []string {
	return []string{"list-panes", "-s", "-t", exactSessionTarget(id), "-F", "#{pane_dead}"}
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
