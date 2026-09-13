# CLAUDE.md

Read and follow [`AGENTS.md`](AGENTS.md) for repository layout, commands, coding conventions, and hard rules.

## App state lives under `~/.ao` only

All app state, the daemon's data dir, `running.json`, worktrees, and the Electron
supervisor's `userData` (Chromium cache, cookies, local/session storage, crash
dumps), must resolve under `~/.ao` (overridable via `AO_DATA_DIR`/`AO_RUN_FILE`).
Never write to or read from `~/Library/Application Support` or any other OS-default
app-data location. `frontend/src/main.ts` pins Electron's `userData` to
`~/.ao/electron`; do not remove that override. See the hard rule in `AGENTS.md`.

## Design System

Always read [`DESIGN.md`](DESIGN.md) before making any visual or UI decision —
**start with the "clone agent-orchestrator verbatim" banner at the top**, which
governs the current look.

The renderer **clones the agent-orchestrator web app verbatim**
(`~/Projects/agent-orchestrator/packages/web/src`) in looks and design, with a
refined-blue accent and the terminal keeping its own palette. This **supersedes the
older design-reference framing** in DESIGN.md (per explicit user decision 2026-06-10).
Build new UI from shadcn primitives (`components/ui/*`) where a component fits. Do not
deviate without explicit user approval. In QA/review, flag any renderer code that
diverges from **agent-orchestrator** — do **not** re-flag old design-reference mismatches.

When showing or demoing frontend changes, run `ao preview [url]` from inside the
session so the change renders in the desktop browser panel (the inspector rail's
Browser tab); do not just describe it. `ao preview` and `ao browser open` are explicit
"show this page" requests, so they reveal the Browser tab for the session the user is
viewing. They never steal OS window focus and never change which session or project the
user is on — a background worker previewing its own session only badges that session.
Passive agent browser activity (`snapshot`, `click`, `fill`, `act`, `errors`, …) still
badges the tab as unseen rather than revealing it, so mention it in your reply when only
that has happened.
