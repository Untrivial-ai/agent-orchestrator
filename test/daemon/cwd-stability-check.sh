#!/usr/bin/env bash
#
# Daemon working-directory stability check (regression guard for #2871).
#
# A desktop auto-update relaunches the app from a temp staging dir that Squirrel
# then deletes, leaving the process in a working directory that no longer exists.
# The daemon (and any tmux server it starts) inherited that dead cwd, poisoning
# every subsequent session spawn machine-wide. The fix stabilizes the daemon's
# cwd and guards runtime launch commands. See #2780 (RCA) / #2775.
#
# This exercises the REAL `ao daemon` process and REAL tmux end-to-end. The
# fine-grained, deterministic coverage lives in Go
# (backend/internal/config, .../daemon, .../adapters/runtime/*); this stays
# small and linear, mirroring test/cli/install-check.sh.
#
# Usage:
#   AO_BIN=/path/to/ao test/daemon/cwd-stability-check.sh
#   test/daemon/cwd-stability-check.sh /path/to/ao
#
# Each daemon is discovered via its own AO_RUN_FILE (running.json), so the check
# is immune to port collisions: the daemon falls back to an ephemeral port when
# its configured one is taken, and we read the port it actually bound.
#
# Exits non-zero if any check fails.
set -uo pipefail

AO_BIN="${1:-${AO_BIN:-ao}}"
command -v "$AO_BIN" >/dev/null 2>&1 || [ -x "$AO_BIN" ] || {
  echo "FATAL: ao binary not found (set AO_BIN or pass a path)"; exit 2; }

PASS=0; FAIL=0
ok()  { echo "  PASS: $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
hr()  { echo "------------------------------------------------------------"; }

DAEMON_PIDS=(); TMUX_SOCKS=(); TMP_DIRS=()
cleanup() {
  for p in "${DAEMON_PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  for s in "${TMUX_SOCKS[@]:-}"; do [ -n "$s" ] && tmux -S "$s" kill-server 2>/dev/null; done
  for d in "${TMP_DIRS[@]:-}"; do [ -n "$d" ] && rm -rf "$d"; done
}
trap cleanup EXIT

jnum() { sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p" <<<"$1"; }
jstr() { echo "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p"; }
real() { cd "$1" 2>/dev/null && pwd -P || echo "$1"; }

# Boot a daemon from $1=cwd with $2=AO_DATA_DIR and $3=AO_RUN_FILE. On success
# sets globals D_PORT / D_PID / D_HEALTH (probe body) / D_LOG; returns 1 on
# timeout. The log lives beside the run-file, never inside $cwd — Test 1 deletes
# $cwd and rmdir needs it empty.
boot_daemon() {
  local cwd="$1" data="$2" runfile="$3" i rf port pid
  D_LOG="$(dirname "$runfile")/.daemon.log"
  (
    cd "$cwd" || exit 1
    unset AO_PROJECT_ID AO_SESSION_ID
    export AO_PORT="${AO_PORT_BASE:-39717}" AO_DATA_DIR="$data" AO_RUN_FILE="$runfile"
    exec "$AO_BIN" daemon
  ) >"$D_LOG" 2>&1 &
  DAEMON_PIDS+=("$!")
  for i in $(seq 1 60); do
    if [ -f "$runfile" ]; then
      rf="$(cat "$runfile" 2>/dev/null)"
      port="$(jnum "$rf" port)"; pid="$(jnum "$rf" pid)"
      if [ -n "$port" ]; then
        D_HEALTH="$(curl -fsS "http://127.0.0.1:${port}/healthz" 2>/dev/null)" || { sleep 0.2; continue; }
        D_PORT="$port"; D_PID="$pid"; [ -n "$pid" ] && DAEMON_PIDS+=("$pid")
        return 0
      fi
    fi
    sleep 0.2
  done
  return 1
}

########################################################################
hr; echo "TEST 1: daemon survives a deleted launch cwd + reports identity"; hr
BASE="$(mktemp -d)"; TMP_DIRS+=("$BASE")
LAUNCH="$BASE/launch-doomed"; DATA="$BASE/data"; mkdir -p "$LAUNCH" "$DATA"
if ! boot_daemon "$LAUNCH" "$DATA" "$BASE/running.json"; then
  bad "daemon never became healthy"; sed 's/^/     /' "$D_LOG" 2>/dev/null | tail -20
else
  ok "daemon healthy on :$D_PORT (pid $D_PID)"
  rmdir "$LAUNCH" 2>/dev/null && ok "deleted launch cwd while daemon runs" || bad "could not delete launch cwd"
  B2="$(curl -fsS "http://127.0.0.1:${D_PORT}/healthz" 2>/dev/null)"
  [ -n "$B2" ] && ok "daemon still serving after launch cwd deleted" || bad "daemon died after launch cwd deleted"
  WD="$(jstr "$B2" workingDirectory)"; SWD="$(jstr "$B2" startupWorkingDirectory)"
  echo "     workingDirectory        = $WD"
  echo "     startupWorkingDirectory = $SWD"
  [ "$(real "$WD")" = "$(real "$DATA")" ] && ok "chdir'd to data dir (off the doomed cwd)" \
     || bad "workingDirectory is not the data dir (got $WD)"
  [ "$(real "$SWD")" = "$(real "$LAUNCH")" ] && ok "startupWorkingDirectory preserved for dev-daemon identity" \
     || bad "startupWorkingDirectory != launch dir (got $SWD)"
fi

########################################################################
hr; echo "TEST 2: relative AO_DATA_DIR is absolutized (no double-nest)"; hr
BASE="$(mktemp -d)"; TMP_DIRS+=("$BASE"); mkdir -p "$BASE/cwd"
if ! boot_daemon "$BASE/cwd" "reldata" "$BASE/cwd/rf.json"; then
  bad "daemon with relative AO_DATA_DIR never became healthy"; sed 's/^/     /' "$D_LOG" 2>/dev/null | tail -20
else
  WD="$(jstr "$D_HEALTH" workingDirectory)"
  echo "     workingDirectory = $WD"
  case "$WD" in /*) ok "workingDirectory is absolute" ;; *) bad "workingDirectory not absolute (got $WD)" ;; esac
  [ "$(real "$WD")" = "$(real "$BASE/cwd/reldata")" ] && ok "data dir at <cwd>/reldata (not nested)" \
     || bad "data dir mis-resolved (got $WD)"
  [ -d "$BASE/cwd/reldata/reldata" ] && bad "double-nested reldata/reldata exists (regression!)" \
     || ok "no double-nested reldata/reldata"
fi

########################################################################
hr; echo "TEST 3: tmux 'cd <ws> || exit' guard lands pane in workspace"; hr
if ! command -v tmux >/dev/null 2>&1; then
  echo "  SKIP: tmux not installed"
else
  BASE="$(mktemp -d)"; TMP_DIRS+=("$BASE")
  POISON="$BASE/poison"; WS="$BASE/workspace"; SOCK="$BASE/sock"; TMUX_SOCKS+=("$SOCK")
  mkdir -p "$POISON" "$WS"
  ( cd "$POISON" && tmux -S "$SOCK" new-session -d -s holder "sleep 60" )
  rmdir "$POISON"
  HOLDER_CWD="$(tmux -S "$SOCK" display-message -p -t holder '#{pane_current_path}')"
  [ ! -d "$HOLDER_CWD" ] && ok "tmux server cwd is deleted (server poisoned)" \
     || echo "     note: this tmux still resolves the deleted server cwd; -c-ignored failure may not reproduce here"
  tmux -S "$SOCK" new-session -d -s guarded -c "$WS" \
     "sh -c 'cd \"$WS\" || exit; pwd > \"$BASE/out\" 2>\"$BASE/err\"; sleep 30'"
  sleep 0.5
  PANE="$(tmux -S "$SOCK" display-message -p -t guarded '#{pane_current_path}')"
  SHELL_PWD="$(cat "$BASE/out" 2>/dev/null)"; SHELL_ERR="$(cat "$BASE/err" 2>/dev/null)"
  echo "     guarded pane_current_path = $PANE"
  echo "     guarded shell getcwd      = ${SHELL_PWD}${SHELL_ERR}"
  [ "$(real "$PANE")" = "$(real "$WS")" ] && ok "pane landed in the workspace" \
     || bad "pane did NOT land in workspace (got $PANE)"
  [ -n "$SHELL_PWD" ] && [ "$(real "$SHELL_PWD")" = "$(real "$WS")" ] && ok "shell getcwd() works and is the workspace" \
     || bad "shell getcwd() wrong/failed: ${SHELL_PWD}${SHELL_ERR}"
fi

hr; echo "RESULT: $PASS passed, $FAIL failed"; hr
[ "$FAIL" -eq 0 ]
