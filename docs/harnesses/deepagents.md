# DeepAgents Code conformance status

DeepAgents Code is **not registered as an Agent Orchestrator harness**. AO has
only a pre-registration conformance gate for a user-supplied `dcode` or
`deepagents-code` executable. No migration, API enum, registry entry, installer,
or UI option exists for it.

## Current decision

The only audited candidate version is DeepAgents Code 0.1.72. Newer versions do
not inherit that audit and must update the pinned gate before they can pass. The
release still does not satisfy AO's runtime contract. The PyPI wheel
`deepagents_code-0.1.72-py3-none-any.whl` has SHA-256
`96a41ecde04f8682210197826a4fbedc94a004cf9e788908c12edb52ce383dc4` and
declares Python `>=3.12,<4`. The corresponding source audit is pinned to commit
`a764619aa8c850bc75e2e916cf53a587637d8c81`. No executable was installed in the
implementation checkout, so no operating system or authenticated-provider
transcript is recorded as passing.

The audited CLI exposes `dcode` and `deepagents-code`, interactive `-m`,
ID-valued `-r`, model selection, Auto, YOLO, ACP, and local auth-status
commands. It does not expose a per-process standing-prompt input or an additive
hook-file input. Hooks are loaded from user, project, and enabled-plugin state.
The experimental `--extension` mechanism is not a stable substitute and is not
wired through the ACP launch path. An explicitly missing resume ID warns and
starts a fresh session instead of failing, and TUI session identity cannot be
captured safely without AO's hook. Consequently 0.1.72 is a documented no-go
for production registration.

The companion `deepagents-acp` 0.0.10 audit is pinned to source commit
`6cf062bfa22d1a34d895e0418e4e32a49f97d8dd` and wheel SHA-256
`08a832e1da7f2be096bd778c710dd1bc2bb5850ad0192fe626771a307d85cc87`.
Its cancellation path does not provide AO's required bounded, reliable
cancellation guarantee: it sets one server-wide Boolean that is observed only
between streamed chunks, without cancelling the running graph/model task or a
blocked permission request. ACP registration therefore remains blocked even
though the CLI exposes an ACP entry point.

Production registration remains blocked until one pinned upstream release
proves all of the following:

- per-process standing instructions supplied through `--system-prompt-file`, or
  another documented append-only overlay that preserves the built-in prompt and
  the user's profile;
- a per-process isolated Hooks v2 input that augments rather than replaces
  user, project, and plugin hooks;
- interactive initial delivery via `-m`/`--message` and exact restore via
  `-r`/`--resume` with a UUID7 thread id;
- the same native thread id in `SessionStart`, `UserPromptSubmit`,
  `PermissionRequest`, `Stop`, and `SessionEnd` events;
- ACP initialization, `session/new`, stable session identity, exact
  `session/load`, typed missing-history and workspace-mismatch errors,
  non-duplicating text/reasoning/tool/plan replay and streaming, permission
  responses, bounded cancellation, restart recovery, model/config selection,
  and truthful capability claims for attachments, MCP, compaction, steering,
  and structured input;
- a local auth-status probe that distinguishes missing, configured, implicit,
  and unknown states without treating credential presence as authorization;
- Python 3.12 or newer and older than Python 4 for the tested distribution; and
- no mutation of user/profile/project configuration during probing or launch.

Replacing `DEEPAGENTS_HOME` in production is not an acceptable substitute for
prompt or hook isolation. Doing so also replaces the user's authentication,
configuration, trust, and session state.

## Running the opt-in gate

The ordinary package suite is credential-free and skips the real executable:

```bash
cd backend
go test ./internal/adapters/agent/deepagents -count=1
```

To inspect a specific locally installed build, provide an absolute binary path:

```bash
cd backend
AO_DEEPAGENTS_CONFORMANCE_BINARY=/absolute/path/to/dcode \
AO_DEEPAGENTS_CONFORMANCE_SHA256=<sha256-of-that-executable> \
  go test ./internal/adapters/agent/deepagents \
  -run TestDeepAgentsUpstreamConformance -count=1 -v
```

The probe creates disposable home, empty DeepAgents profile, AO data, and
workspace directories, proves Python `>=3.12,<4` from the executable's
interpreter shebang, verifies the selected executable against the caller's
explicit SHA-256, runs bounded version/help probes, and asserts that the
disposable profile and workspace did not change. Only an allowlist of
non-secret process settings is inherited; provider API keys, Python loader
overrides, XDG paths, and other ambient credentials/configuration are scrubbed.
A wrapper or native executable whose Python interpreter cannot be tied to its
shebang fails closed.

The caller-provided executable hash binds a probe transcript to one local
wrapper; it does not prove that the imported package came from the audited
wheel. The `ArtifactFingerprint` gate therefore remains false until a future
probe can verify the installed distribution against checked-in provenance.

CLI help is recorded only as a syntactic surface: message, ID-valued restore,
prompt-file, hooks-file, ACP, and auth-status entry points. It does not prove
that initial delivery is race-free, restore selects the exact ID, prompt input
is append-only, hooks merge additively, auth status is truthful, or ACP behaves
correctly. Every behavioral contract field therefore remains false until an
executable interaction proves it. A failed TUI gate stops production adapter
registration; a failed ACP gate independently stops Chat registration.

The probe does not seed guessed credential, configuration, or session
filenames: current DeepAgents builds may use `config.toml`, `auth.json`, and
SQLite-backed session state, and fabricated fixtures would not establish
compatibility with any of them.

## Deliberate exclusions

This conformance package does not implement TUI launch, hooks, auth readiness,
installation, ACP Chat, interface handoff, reviewer support, or visibility of
DeepAgents' internal subagents. Those capabilities must be delivered separately
and only after their preceding contract gates pass.
