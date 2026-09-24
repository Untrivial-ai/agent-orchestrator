# Cloud TUI ↔ Chat interface transition

## Intent and scope

Port the cloud Chat UI and working two-way TUI ↔ Chat switching from [PR #4683](https://github.com/Untrivial-ai/agent-orchestrator/pull/4683) onto `cloud/chatui`. A user must be able to open a cloud session in its current interface, switch to Chat without losing the session or native conversation, and switch back to the terminal. Only one agent controller may be active for a session at a time. Local-only sessions and their existing handoff must keep working.

The source PR is an implementation reference, not a unit to merge wholesale. Exclude unrelated changes to deployment, GitHub connection, browser fetch, telemetry, updater, credentials, model/effort selection, and general workspace UI. Bring in a source-PR fix outside the transition files only when a test or direct dependency shows that it is necessary for this flow.

## Approach

Use the source PR's final behavior as the reference and selectively apply its commits or hunks in dependency order. Literal cherry-picking of its feature commit and follow-ups would import unrelated changes and conflict with newer `main` migrations. A frontend-only copy would show a Chat panel but could not safely stop/start worker controllers or preserve conversation identity. The selected approach ports the minimal complete vertical slice: durable transition state, worker handoff, API/client contract, and renderer integration.

## Architecture and data flow

The cloud control plane owns each session's committed `interfaceMode` and one durable transition record. The POST transition endpoint validates the requested opposite mode and records an idempotent transition. A coordinator drives checkpoints under a service context, draining or interrupting the current controller as requested, waiting for it to exit, carrying the native conversation identity and relevant transcript facts, starting the target controller, then committing the new mode. A process restart resumes from the last durable checkpoint; it must never start both controllers for one session. Failed transitions remain visible and recoverable, rather than silently changing the displayed mode.

The worker transport performs the mode-specific stop/start/inspect operations. Chat turns use the existing cloud message/event stream, with worker output projected to stable chat events. Terminal transport remains available only in TUI mode. Switching Chat → TUI must finish or explicitly interrupt an in-flight turn before starting the terminal agent. Switching TUI → Chat must wait for terminal exit and preserve native conversation identity. The provider-specific behavior for Claude Code, Codex, and Cursor follows the source PR where supported; unsupported harnesses report a clear refusal.

The Cloud API exposes the session's committed interface mode and transition state, plus transition start/read/cancel actions and any worker messages needed for coordination. Contract changes originate in `contracts/cloud/openapi.yaml`; regenerate `packages/cloud-client/src/schema.ts` and add only the corresponding typed client methods. Do not alter the local daemon API contract unless a shared, pure handoff vocabulary is directly needed by both implementations.

The desktop renderer resolves cloud sessions from the Cloud control plane. Its existing interface switch invokes Cloud transitions for cloud sessions and the local daemon path for local sessions. In Chat mode it renders the shared `ChatWorkspace` through a cloud adapter that loads durable events and sends/cancels turns. In TUI mode it renders the existing terminal. Transition progress, failure, and both switch directions remain visible without showing a stale interface as ready.

## Persistence and compatibility

Add new cloud migration versions after the current migration sequence; never edit a migration already merged on `main`. Migrate existing sessions to TUI by default. Preserve organization scoping and RLS for transition and held-message records. Reconcile session reads, worker requests, and event persistence so retries and reconnects are safe. Do not import the source PR's migration numbers blindly, because they were authored before later `main` migrations.

## Verification

Port or adapt focused tests from the source PR for both directions, duplicate requests, unsupported agents, in-flight turns, worker exit ordering, native conversation identity, restart recovery, transition failures, API authorization/shape, and renderer cloud/local routing. Then run cloud Go tests, API schema generation/drift checks, frontend tests/typecheck/build, and relevant repository CI checks. Review the final diff to ensure no unrelated source-PR files entered the branch. For visual review, render the frontend in `ao preview` as repository guidance requires. Do not publish or deploy as part of verification.

## Delivery

Keep commits reviewable by boundary: cloud transition state and coordinator; worker execution/transport; Cloud API and typed client; renderer Chat UI and switch; focused tests/fixes. No push or PR update is implied by this spec; those remain separate user requests.
