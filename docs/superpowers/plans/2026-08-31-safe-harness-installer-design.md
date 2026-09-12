# Safe Harness Installer Design

## Goal

Make PR #4221's Settings installer trustworthy on macOS and recoverable across UI and daemon restarts without weakening AO's no-sudo policy or moving installer policy into the frontend.

## Decisions

### Daemon-owned durable jobs

Harness installation is a daemon job, not a React effect. The daemon persists the latest job for each harness in SQLite. A job records the harness, selected server-owned method, lifecycle status, expected binary destination, captured diagnostics, error text, and timestamps.

The lifecycle is `installing -> verifying -> succeeded|failed`. Jobs left in `installing` or `verifying` when the daemon starts become `interrupted`; AO never assumes they succeeded and never silently reruns them. The Settings page hydrates all current jobs from the daemon and polls while work is active.

### Adapter-backed verification

An install is successful only after AO resolves the installed harness through the canonical agent adapter and runs a bounded, non-authenticating version/launch probe against the exact resolved executable. Authentication checks remain part of the existing agent probe flow and are not installation verification.

`Verify again` runs only this verifier. `Reinstall` starts the selected installation recipe again.

### Explicit, server-owned methods

The daemon returns every viable method with a stable method identifier and a recommended flag. The UI presents the recommended method by default and lets the user choose another method when multiple methods are viable. The client submits only the harness and method identifier; argv, environment, destination expectations, and help text remain daemon-owned.

Automatic installers never use `sudo`, never inherit interactive stdin, and use noninteractive flags and environment where supported. Method preflight checks both presence and viability:

- npm methods require a supported Node/npm and a writable global prefix.
- Python harnesses prefer `uv tool`, then `pipx`; raw global `pip` is not an automatic fallback.
- Homebrew methods require a usable writable Homebrew installation.
- Official HTTPS installer scripts may run automatically, but AO must download the complete response into its own bounded temporary directory first, record its SHA-256 digest, execute the saved file with a fixed interpreter argv and closed stdin, then remove it. AO never evaluates `curl | shell` pipelines.

### Diagnostics and recovery

Settings displays polling failures instead of silently dropping them. Failed and interrupted jobs expose an expandable diagnostics area containing the method, expected destination, output/error text, and a copy action. Verification and installation are separate user actions.

### Droid lifecycle safety

Before installing or reinstalling Droid, the daemon checks durable session facts. If any non-terminated Droid session exists, the request fails with a typed conflict and directs the user to end that session first. The installer never reaches into the runtime to kill a Droid process.

### Conflict resolution

The branch is updated from canonical `main`. For conflicts, current `main` owns the Settings shell, navigation, daemon wiring, storage migrations, and generated API structure. PR #4221's harness section is reapplied through those current extension points. No conflict is resolved by accepting an entire old file when `main` has evolved.

## API shape

- `GET /api/v1/agents/installers` returns harnesses with viable methods, recommended method, availability reason, and manual instructions only when no supported package-manager or official-installer method is available.
- `GET /api/v1/agents/install-jobs` returns the latest durable job per harness.
- `POST /api/v1/agents/{agent}/install` accepts `{ "method": "<server method id>" }`.
- `GET /api/v1/agents/{agent}/install` remains available for a single job.
- `POST /api/v1/agents/{agent}/verify` starts verification without reinstalling.

Requests retain the existing API error envelope and request IDs. Concurrent work for the same harness returns conflict; different harnesses may install concurrently.

## Testing

Service tests cover method preflight, explicit method validation, no-stdin execution, state transitions, adapter-backed verification, interruption recovery, diagnostic persistence, concurrency, and active-Droid rejection. Controller tests cover DTOs, batch hydration, verification, conflicts, and error envelopes. Frontend tests cover hydration, method selection, polling errors, diagnostics/copy, interrupted jobs, and separate verify/reinstall actions. Generated API artifacts are regenerated together and the native Electron app is launched for the final local check.
