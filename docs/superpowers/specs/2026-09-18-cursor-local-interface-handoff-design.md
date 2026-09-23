# Cursor Local TUI–Chat Interface Handoff Design

Status: approved direction; pending written-spec review

Date: 2026-09-18

Assessed baseline: main at 5a84069af

## Goal

Allow one AO-managed Cursor conversation to move safely between the local
interactive TUI and local Chat, in both directions, without changing the AO
session or workspace and without losing, duplicating, or fabricating native
conversation history.

The first deliverable is a live provider-conformance test. Cursor switching is
enabled only if the installed Cursor version proves that its interactive CLI
and ACP server share a stable conversation identity and mutually visible,
structured history.

## Current Behavior

Cursor already has most of the mechanical prerequisites:

- The TUI adapter captures Cursor's native conversation id from hooks.
- TUI restore runs `cursor-agent --resume <id>`.
- Chat runs the user's installed `cursor-agent acp` through the native ACP
  adapter.
- TUI and ACP launches receive the same AO-owned `CURSOR_DATA_DIR`.
- Cursor ACP supports native start, load/resume, tools, approvals, cancellation,
  and persisted context in the existing opt-in live test.
- The generic Session Manager already owns draining, source shutdown, durable
  mode transitions, controller-generation fencing, rollback, restart recovery,
  and transition-message delivery.

Cursor remains Chat-only for interface switching because these facts do not yet
prove that interactive TUI turns appear in ACP `session/load`, or that both
interfaces use stable message and turn identities.

PR #4416 attempts to close this gap by parsing provider transcript files,
reconciling them with ACP history, and rendering durable Chat messages into
terminal scrollback. Its branch conflicts with current main, and its central
Cursor history parser is not protected by direct provider-conformance tests.

The cloud Cursor implementation is also not the local architecture. Cloud Chat
runs a new headless `cursor-agent --print --output-format stream-json` process
per turn and resumes it by native id. Local Chat is a long-lived ACP controller.
Cloud therefore confirms resume-command and session-id behavior, but not ACP ↔
interactive history compatibility.

## Selected Architecture

Use the current local Claude handoff architecture as the primary model, while
keeping the generic coordinator unchanged. Cursor opts into the existing
`AgentInterfaceHandoff` capability only after its provider-specific
conformance and checkpoint components satisfy the same fail-closed contracts.

Reuse from cloud is limited to concepts already compatible with local:

- Preserve the same provider conversation id across controller replacement.
- Stop and await the source process before starting the target controller.
- Resume the interactive CLI through the shared restore-command builder.

Do not port cloud's headless per-turn Chat runner, Postgres terminal model, or
cloud coordinator into the local daemon. Do not port PR #4416's transcript/LCS
reconciliation layer wholesale.

## Provider-Conformance Gate

Add an explicit opt-in live test using an isolated temporary workspace and
AO-owned Cursor data directory. The test consumes the user's authenticated
Cursor account and is excluded from ordinary CI.

The test performs this sequence:

1. Start Cursor ACP and submit a uniquely tagged user turn.
2. Wait for a completed assistant turn and record the native conversation id
   plus structured message identities returned by ACP.
3. Terminate ACP cleanly.
4. Resume the same id through Cursor's interactive CLI, prove that the ACP turn
   is in context, submit a second uniquely tagged turn, and wait for completion.
5. Stop the interactive CLI and wait for provider state to flush.
6. Resume/load the same id through ACP.
7. Assert that both completed turns are returned exactly once, in order, with
   usable stable identities and no conversation-id change.
8. Submit a third ACP turn, stop ACP, resume the TUI again, and prove that the
   third turn is in context without an id flap.

The test must exercise the real interactive process rather than treating
`--print` mode as a TUI substitute. PTY input/output may be used to drive the
interactive CLI, but terminal text is evidence only for the test assertions;
it is never imported as canonical Chat history.

The conformance gate fails if any of the following occurs:

- TUI and ACP return different conversation ids for the same resume.
- ACP cannot load the TUI-created turn.
- A turn is missing, duplicated, reordered, or returned without identities
  sufficient for deterministic reconciliation.
- Provider state needs the user's normal Cursor profile instead of AO's
  isolated `CURSOR_DATA_DIR`.
- The observed behavior is timing-dependent after a bounded flush/retry window.

Failure leaves Cursor interface switching disabled. It does not fall back to
screen scraping, prompt injection, or unverified transcript reconstruction.

## Cursor Handoff Capability

After the conformance gate passes, the Cursor adapter implements the existing
handoff capability and declares how the native conversation id maps between
TUI restore and ACP resume. The capability contains no switching orchestration.

Cursor's initial design assumes one stable native id. If the live test proves
that interactive resume creates a new native id while retaining ancestry, the
design must be amended before implementation to add a Cursor-specific branch
resolver comparable to Codex. AO must not silently replace the durable native
id based only on the latest hook event.

The adapter also exposes the provider-state location or probe required to show
that a captured id exists under the AO-owned Cursor profile before source
ownership is committed away. Absence, ambiguity, or unreadable state is an
unsettled handoff, not proof that a conversation is empty.

## TUI to Chat Flow

The existing Session Manager performs the transition:

1. Gate new TUI input and use Cursor activity/surface evidence to reach the
   existing drain or interrupt boundary.
2. Capture AO-owned hook observations for the current Cursor launch generation.
3. Verify that the observations belong to the durable native conversation and
   resolve to a completed provider checkpoint.
4. Stop the TUI process and wait for shutdown/provider-state flush.
5. Commit the controller epoch and start Cursor ACP with the same native id,
   workspace, permissions, and AO-owned Cursor data directory.
6. Load structured history through ACP and reconcile it with AO's durable Chat
   projection using provider identities and the verified checkpoint boundary.
7. Mark the target ready only after history convergence succeeds.

Cursor conversation facts remain disabled in `ao hooks` until the provider's
documented hook fields and native checkpoint records can be correlated without
recording prompts or answers as authority. Enabling the handoff capability and
enabling Cursor conversation facts occur in the same tested change; neither is
an independent best-effort toggle.

If Cursor offers no trustworthy native checkpoint representation, TUI → Chat
remains unsupported even if ACP appears to remember context internally. AO must
be able to prove where the completed TUI history ends before accepting new Chat
turns.

## Chat to TUI Flow

The existing Session Manager closes Chat intake, drains or interrupts accepted
work, terminates the ACP controller, and resumes `cursor-agent --resume <id>`.
The interactive CLI is responsible for rendering its native history.

AO does not print its durable Chat transcript into terminal scrollback as a
substitute for provider hydration. Such output would be display-only, could
diverge from Cursor's actual context, and cannot prove that future TUI turns
share the same conversation. If Cursor does not render prior messages but does
retain them natively, a later separately designed display-only affordance may
be considered; it is outside this feature and must never be sent back as a
prompt.

The proposed target-ready proof cannot be implemented on the validated provider
build. Cursor `2026.09.18-9a7762b` deliberately omits `sessionStart` whenever
`--resume` is present. Its later `beforeSubmitPrompt` hook reports the expected
native id, but only after input has already reached the resumed controller, so it
cannot serve as a fail-closed pre-delivery readiness boundary. This is an
additional reason Chat-to-TUI switching remains unsupported.

## History and Checkpoint Rules

- Provider-native structured history is authoritative for native continuity.
- AO's durable Chat projection remains authoritative for the local UI and
  delivery idempotency.
- Terminal scrollback and ANSI-rendered text are never canonical history.
- Hook payload text may witness an event only after a provider-specific verifier
  binds it to the native conversation, launch generation, completed turn, and
  structured history boundary.
- Reconciliation is identity-based. Text matching may validate a bound record
  but may not choose between repeated turns.
- Retry is bounded and reserved for provider flush/load convergence. Ambiguous
  ancestry, identity changes, or duplicates fail closed immediately.
- AO never sends reconstructed history as a new Cursor user prompt.

## Failure and Recovery

No new coordinator or durable state machine is introduced. Cursor uses the
existing transition states, compare-and-swap ownership changes, rollback,
startup reconciliation, generation fencing, and message outbox.

Provider-specific failures map into existing unsettled-history or target-start
failures:

- Native checkpoint not flushed: retain source ownership and retry within the
  established settlement window.
- ACP history does not converge: do not activate Chat; restore the TUI when the
  existing rollback rules say ownership is safe.
- Resumed TUI reports a different id: do not mark the target ready; roll back or
  retain the existing recovery-required state.
- Source stop is unconfirmed: never start a second Cursor controller.
- Daemon restart mid-transition: reconcile from the durable committed mode and
  generation exactly as for Claude/Codex.

Errors exposed to clients should state that Cursor's native history or identity
could not be verified. They must not leak prompts, responses, provider payloads,
credentials, or absolute provider-state paths.

## Testing Strategy

### Provider live tests

- Add the ACP → TUI → ACP → TUI conformance sequence described above.
- Pin a minimum verified Cursor version only after observing the passing
  behavior; do not assume all versions above the current ACP floor satisfy the
  cross-interface contract.
- Document the tested Cursor version and platform. Other authenticated
  platforms remain explicit validation gaps until exercised.

### Adapter and verifier tests

- TUI and ACP construct commands/environment against the same `CURSOR_DATA_DIR`.
- Handoff rejects missing, malformed, mismatched, duplicated, and stale native
  identities.
- Checkpoint verification covers completed turns, pending tails, repeated text,
  wrong launch generations, partial writes, and bounded retry.
- Structured ACP load maps each native message once and preserves stable ids.

### Session Manager tests

- Cursor TUI → Chat and Chat → TUI happy paths use the existing coordinator.
- Target preflight failure leaves the source untouched.
- Source-stop uncertainty never starts the target.
- Checkpoint/history mismatch prevents target activation and restores source
  ownership when safe.
- Target id mismatch, daemon restart, stale hooks/events, queued lifecycle
  messages, and repeated transition requests preserve the one-controller
  invariant.

### Regression checks

- Claude and Codex handoff behavior is unchanged.
- Cursor Chat without interface switching remains unchanged.
- Non-switching harnesses still cannot report trusted conversation facts.
- Focused adapter/session-manager tests run first, followed by the repository's
  backend build, test, race, vet, lint, and frontend typecheck/build gates.

## Rollout

Implementation is staged so an unproven provider behavior cannot accidentally
ship:

1. Land the opt-in live conformance harness and record evidence from the minimum
   supported Cursor version/platform.
2. If it passes, implement the smallest Cursor capability, native checkpoint
   verifier, structured history reconciliation, and unit/integration tests.
3. Enable Cursor in the switching allowlist only in the same change that makes
   all fail-closed tests pass.
4. Update architecture/status documentation to list Cursor as supported and
   record platform/version evidence and remaining validation gaps.

If step 1 fails, stop. Preserve Cursor's current Chat and TUI behavior and file
the observed provider incompatibility; do not proceed to steps 2–4.

## Alternatives Rejected

### Copy the cloud Cursor implementation

Rejected because it would replace local ACP Chat with a headless per-turn
process and duplicate local lifecycle, persistence, and controller ownership
machinery.

### Port PR #4416 wholesale

Rejected because it is based on an older main, conflicts with the hardened
checkpoint/history contracts, and introduces a large unproven transcript
reconciliation surface.

### Use terminal or provider transcript text as canonical history

Rejected because text lacks reliable message identity, tool-event structure,
branch ancestry, and completion boundaries. It can produce duplicate or
fabricated Chat context.

### Enable only Chat to TUI

Rejected for this feature because the approved product goal is bidirectional
handoff. Shipping one direction would create a session that users cannot safely
return to Chat. The implementation remains entirely disabled until both
directions pass.

## Out of Scope

- Replacing Cursor ACP with cloud-style `--print` Chat execution.
- Changing the generic interface-transition state machine.
- Importing the user's normal Cursor profile.
- Supporting transitions involving a currently executing tool call.
- Synthesizing terminal output into Chat messages.
- A frontend redesign beyond exposing the capability and existing transition
  failures through current surfaces.
- Enabling other ACP harnesses for interface switching.

## Acceptance Criteria

- The live conformance test proves one Cursor native conversation survives ACP
  → TUI → ACP → TUI with all tagged turns present once and stable identity.
- Local switching uses the existing coordinator and local ACP driver; no cloud
  coordinator or headless Chat execution is introduced.
- TUI → Chat activates only after trusted checkpoint and structured-history
  convergence.
- Chat → TUI activates only after the resumed TUI reports the expected native
  id for the current launch generation.
- Every uncertainty fails closed without two live controllers or synthetic
  prompts.
- Claude/Codex switching and ordinary Cursor Chat/TUI behavior remain covered
  and unchanged.
