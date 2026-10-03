# CLAUDE.md

Read and follow [`AGENTS.md`](AGENTS.md) for repository layout, commands, coding conventions, and hard rules.

## App state lives under `~/.ao` only

All app state, the daemon's data dir, `running.json`, worktrees, and the Electron
supervisor's `userData` (Chromium cache, cookies, local/session storage, crash
dumps), must resolve under `~/.ao` (overridable via `AO_DATA_DIR`/`AO_RUN_FILE`).
Never write AO state to `~/Library/Application Support` or any other OS-default
app-data location. The sole read exception is an explicit user-initiated import
from a validated Chrome, Firefox, or Safari profile: read known source files
without modifying them and keep snapshots and results under `~/.ao`.
`frontend/src/main.ts` pins Electron's `userData` to
`~/.ao/electron`; do not remove that override. See the hard rule in `AGENTS.md`.

## Design system

Always read [`DESIGN.md`](DESIGN.md) before making any visual or UI decision.
`DESIGN.md` is the current design standard and review guide. Use the approved,
repo-local reference files listed in its "Reference implementation map". Do not
infer a visual rule from another screen just because that screen exists today.
When current code conflicts with the guide, preserve functional behavior and
move the surface toward the guide in the smallest safe change. Build new UI
from shadcn primitives (`components/ui/*`) where a component fits, and log a
targeted migration when an existing mismatch needs broader work.

When showing or demoing frontend changes, run `ao preview [url]` from inside the
session so the change renders in the desktop browser panel (the inspector rail's
Browser tab); do not just describe it. `ao preview` updates the panel without
disrupting the current view. It does not steal focus or force the Browser tab open
if the user is looking at something else. It only badges the tab as unseen. If the
Browser tab isn't already the one
the user has open, say so in your reply (e.g. "check the Browser tab") so the change
doesn't go unnoticed behind the badge.
