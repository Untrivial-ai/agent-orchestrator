# Reasonix Harness Design

## Goal

Add Reasonix as a production-selectable AO worker/orchestrator harness after a
released Reasonix version exposes a process-scoped, system-role channel for AO's
standing instructions. Keep Reasonix user-owned, preserve its configuration and
hooks, and use its native session identity for exact restoration.

This design covers the terminal harness only. Reasonix ACP Chat, reviewer use,
interface handoff, and first-class representation of Reasonix subagents are
separate capabilities and are outside this change.

## Pinned upstream evidence

The initial audit targets Reasonix `v1.39.0` at
`6845b6de3c5e079737a3b010ebcdd87f6cd0228c`.

- The executable is `reasonix`; `reasonix --version` reports
  `reasonix v1.39.0`.
- Official release archives cover macOS, Linux, and Windows on amd64 and arm64.
  The audited macOS arm64 archive matched the published SHA-256 checksum.
- Official installation paths include npm, Homebrew on macOS, and GitHub release
  archives.
- `--dir`, `--model`, `--permission-mode`, `--resume`, `--add-dir`, and
  `--events-jsonl` are documented CLI surfaces.
- `reasonix session ... --json` and native hooks expose a stable opaque machine
  session ID. Fresh and resumed commands can select that exact ID.
- `reasonix doctor --json` reports local credential configuration, but does not
  prove that a provider has authorized it.
- Native project hooks in `.reasonix/settings.json` are merged with installed
  plugin and global hook sources. Reasonix supports the lifecycle events AO
  needs, including `SessionStart`, `UserPromptSubmit`, `PreToolUse`,
  `PostToolUse`, `PermissionRequest`, `Stop`, and `SessionEnd`.

The audit found no supported CLI flag or environment variable that supplies a
per-process system prompt. Existing `system_prompt_file`, `REASONIX.md`,
`AGENTS.md`, and `CLAUDE.md` mechanisms are project or profile configuration.
`SessionStart` hook output is injected into a subsequent user turn as
`<hook-context>`, so it is not a private system-role channel.

## Production gate

Reasonix must first ship a tagged release with this command-line contract:

```text
reasonix --append-system-prompt-file <path> [...]
reasonix run --append-system-prompt-file <path> [...]
```

The name may change upstream, but the behavior must remain equivalent:

1. Read one explicitly supplied UTF-8 file for this process only.
2. Append its contents as system-role standing guidance while retaining the
   built-in Reasonix prompt, user configuration, output style, core policies,
   and project instruction hierarchy.
3. Apply the same input on fresh launch and exact resume.
4. Do not turn the content into a user message or hook context.
5. Do not write the value or path into `reasonix.toml`, Reasonix home, project
   instruction files, or another persistent configuration surface.
6. Fail before a model turn when the file is missing, unreadable, invalid
   UTF-8, or empty; do not silently launch without AO policy.
7. Keep the prompt contents out of diagnostic output, structured event output,
   and errors. Session persistence may retain the composed system message only
   where Reasonix already persists system prompts for exact restore.
8. Accept an absolute AO-owned path outside the workspace without expanding it
   through a shell.

AO will not register Reasonix from an unreleased commit. After this contract is
merged upstream, a live conformance test must pass against the published binary,
and the adapter documentation will record the minimum supported Reasonix
version and executable fingerprint.

## Rejected workarounds

- Replacing `REASONIX_HOME` with a shadow profile risks losing or copying user
  authentication, configuration, sessions, plugins, and trust state.
- Writing `reasonix.toml` or a root instruction document would overwrite or
  compete with user-owned project configuration.
- A `SessionStart` hook exposes AO guidance as next-turn user context.
- Launching from a synthetic child workspace and adding the real checkout with
  `--add-dir` changes Reasonix's workspace identity, config discovery, memory,
  trust, sandbox, and session semantics.

These paths remain prohibited even if they appear to work in a narrow launch
test.

## AO adapter after the gate passes

Create `backend/internal/adapters/agent/reasonix/` following current registered
adapter boundaries. The adapter owns binary resolution, launch and restore argv,
prompt readiness, hook merging, native identity parsing, activity derivation,
authentication reporting, and model input metadata. AO continues to own the
PTY, worktree, lifecycle, and durable session record.

Fresh launch will use the resolved executable with an explicit workspace,
permission preset, optional model, and AO-owned prompt file:

```text
reasonix --dir <workspace> --permission-mode <preset>
  --append-system-prompt-file <ao-system-file> [--model <model>]
```

The initial user task will be delivered only after an authoritative TUI-ready
signal or a pinned, bounded readiness pattern proves the composer can accept
input. AO will not place arbitrary prompt text into shell argv. The adapter must
prove multibyte text, leading dashes, multiline input, and startup-dialog
behavior against the released binary.

Restore will use the exact native machine session ID and reapply all launch-time
policy:

```text
reasonix --dir <workspace> --permission-mode <preset>
  --append-system-prompt-file <ao-system-file> [--model <model>]
  --resume <native-session-id>
```

Missing or malformed native identity returns no restore command; AO must not use
`--continue`, a recent-session picker, or fuzzy matching as a substitute.

## Permissions

Reasonix exposes three presets. The initial AO mapping is explicit:

| AO mode | Reasonix preset | Contract |
| --- | --- | --- |
| `default` | `workspace-write` | Reasonix's documented CLI default |
| `accept-edits` | `workspace-write` | Closest native preset; the UI/help text must disclose that Reasonix also permits its normal workspace-scoped operations |
| `auto` | `workspace-write` | Automatic operation inside the Reasonix workspace sandbox |
| `bypass-permissions` | `danger-full-access` | Removes the workspace sandbox and requires AO's existing high-risk confirmation |

The adapter will not use legacy aliases such as `auto` or `yolo`. If live
conformance shows that `workspace-write` exceeds AO's accepted semantics in a
way that cannot be disclosed safely, `accept-edits` must be rejected rather than
silently weakened.

## Hooks, activity, and identity

`GetAgentHooks` will atomically merge uniquely identifiable AO observer commands
into `<workspace>/.reasonix/settings.json`. It must preserve unknown keys and
every user-defined hook in array order. Repeated installation is idempotent,
and AO-created files are covered by the AO-managed sibling `.gitignore`.

AO hooks are observation-only: they always preserve Reasonix behavior and never
approve, deny, suppress, replace, or inject content. Hook payloads are decoded
defensively and reduced to session identity plus lifecycle facts. Raw prompts,
tool arguments, model output, credentials, and environment values are not
stored in AO metadata.

The activity mapping is conservative:

- accepted prompt or pre-tool event: `active`;
- permission-request event: `blocked` only when a later correlated event can
  clear it safely;
- post-tool event: active while the turn continues;
- successful stop: `waiting_input`;
- session/process end: exited/terminated through existing runtime lifecycle.

The native machine session ID is captured from a documented hook field or the
redacted machine session commands. `SessionInfo` persists it under AO's standard
agent-session metadata key. Reasonix-internal subagents remain opaque activity
inside the parent AO session.

## Authentication, models, and installation

Local doctor output may yield `configured`, `unauthorized`, or `unknown` only as
supported by positive evidence. Credential presence alone never produces
`authorized`. A launch-time validator may report `authorized` only after a safe,
bounded provider round trip is identified and tested; otherwise actual launch
remains authoritative.

Reasonix accepts free-form model names through `--model` but does not expose a
stable bounded catalog suitable for AO. `GetConfigSpec` therefore uses text
selection with direct custom entry and no invented model list.

Installation metadata will use official, platform-appropriate methods only.
AO may offer Homebrew on macOS and npm where supported, or direct users to the
official releases. It will not execute a remote shell installer. Binary
resolution covers `reasonix`, `reasonix.exe`, npm Windows shims, PATH, and
documented package-manager locations; identity verification must parse
`reasonix --version` rather than trusting a name-only match.

## Registration

Only after upstream and adapter conformance pass:

1. Add the Reasonix harness value and production registry constructor.
2. Add a new SQLite migration widening only the current session harness check,
   plus inverse and burned-migration coverage.
3. Extend worker/orchestrator/session/delegation DTO enums, excluding reviewer
   enums.
4. Regenerate OpenAPI and frontend TypeScript artifacts with `npm run api`.
5. Add product identity, labels, licensed artwork, settings, and picker support.
6. Verify create, launch, restore, message delivery, cancellation, kill,
   cleanup, daemon restart, and project-default flows.

Reasonix will not enter `domain.AllHarnesses`, the registry, storage constraints,
API enums, or UI pickers while any production gate remains unproven.

## Testing and verification

Upstream tests must cover both interactive and `run` parsing, composition order,
fresh launch, exact resume, invalid files, and redaction. A published-binary
conformance test will additionally prove prompt role, native session identity,
TUI readiness, cancellation, hook payloads, hook preservation, and restore.

AO unit tests will cover manifest identity; binary resolution; launch and
restore argv; prompt readiness; model and permission mapping; hook merge,
idempotence, preservation, and cleanup footprint; session-ID extraction;
activity transitions; auth timeout/malformed/configured behavior; and platform
paths. Network and authenticated-provider checks remain opt-in live tests using
temporary AO/workspace data.

Before handoff, run focused Reasonix adapter and registry tests followed by:

```bash
cd backend && go test ./...
cd backend && go test -race ./...
cd backend && go vet ./...
npm run api
npm run lint
npm run frontend:typecheck
cd frontend && npm run build
```

Visually verify the identity, settings, project default, and task creation flows
through AO preview or the isolated desktop lab. Remote CI remains authoritative
for unavailable OS and authenticated-provider coverage; publishing is never a
validation step.
