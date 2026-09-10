# Interface handoff: patterns from Superset, ACP, Claude, and Codex

Research snapshot: 2026-08-14. Sources are pinned to commits so the cited line numbers remain stable.

## Verdict

The two review blockers have the same architectural cause: AO is asking a rendered terminal frame to stand in for provider state and provider history. Other implementations keep these separate.

| Blocker | Correct invariant |
| --- | --- |
| An approval menu is reported as `DRAIN_DRAFT_PRESENT` | A typed or positively recognized interaction (`awaiting_permission`, `awaiting_answer`) outranks composer detection. It is never an unsent draft. |
| Output visible before a switch is absent after it | Do not commit the switch until the source reaches a provider completion/checkpoint, the target replays through that checkpoint, and AO has applied that replay idempotently. |

The safe PR-sized repair is therefore: fix interaction precedence, refuse to hand off an unsettled turn, and make history replay prove settlement instead of silently omitting a running turn. A truly lossless mid-turn switch additionally needs a daemon-owned event journal or an authoritative provider snapshot; PTY text alone cannot provide that guarantee.

## What Superset actually does

Superset does not infer approvals from terminal prose. Its chat protocol has a dedicated `approval_request` item with a target item, options, and `pending`/`answered`/`stale` state ([`packages/chat/src/protocol/items.ts:137-167`](https://github.com/superset-sh/superset/blob/4e18e1fa794be7969d517bea86d082105e44c836/packages/chat/src/protocol/items.ts#L137-L167)). Its Codex adapter converts app-server approval RPCs into that item and changes the session to `awaiting_input` ([`packages/chat-runtime/src/harness/codex/codexAdapter/codexAdapter.ts:442-498`](https://github.com/superset-sh/superset/blob/4e18e1fa794be7969d517bea86d082105e44c836/packages/chat-runtime/src/harness/codex/codexAdapter/codexAdapter.ts#L442-L498)). This is protocol state, not composer state.

Superset also separates:

- durable item/turn/session events, which carry a cursor;
- transient text/tool/terminal deltas, which do not.

That split is explicit in [`packages/chat/src/protocol/envelope.ts:5-14,52-115`](https://github.com/superset-sh/superset/blob/4e18e1fa794be7969d517bea86d082105e44c836/packages/chat/src/protocol/envelope.ts#L5-L115). Durable events are transactionally appended with an epoch and monotonically increasing sequence before publication ([`packages/chat-runtime/src/journal/journal/journal.ts:57-99`](https://github.com/superset-sh/superset/blob/4e18e1fa794be7969d517bea86d082105e44c836/packages/chat-runtime/src/journal/journal/journal.ts#L57-L99), [`packages/chat-runtime/src/sessions/liveSession/liveSession.ts:129-165,232-235`](https://github.com/superset-sh/superset/blob/4e18e1fa794be7969d517bea86d082105e44c836/packages/chat-runtime/src/sessions/liveSession/liveSession.ts#L129-L165)). A subscriber first replays from its cursor and is then registered for live delivery ([`packages/chat-runtime/src/stream/subscriptions/subscriptions.ts:84-120`](https://github.com/superset-sh/superset/blob/4e18e1fa794be7969d517bea86d082105e44c836/packages/chat-runtime/src/stream/subscriptions/subscriptions.ts#L84-L120)); the client advances its cursor only on durable envelopes ([`packages/chat/src/client/subscribeToSession/subscribeToSession.ts:56-105`](https://github.com/superset-sh/superset/blob/4e18e1fa794be7969d517bea86d082105e44c836/packages/chat/src/client/subscribeToSession/subscribeToSession.ts#L56-L105)).

There is an important limitation: Superset deliberately drops deltas when no subscriber exists ([`subscriptions.ts:113-120`](https://github.com/superset-sh/superset/blob/4e18e1fa794be7969d517bea86d082105e44c836/packages/chat-runtime/src/stream/subscriptions/subscriptions.ts#L113-L120)). It recovers at an item boundary because Codex's completed command item contains authoritative aggregate output ([`mapThreadItem.ts:151-231`](https://github.com/superset-sh/superset/blob/4e18e1fa794be7969d517bea86d082105e44c836/packages/chat-runtime/src/harness/codex/mapThreadItem/mapThreadItem.ts#L151-L231)). Thus Superset is evidence for **cursor replay plus final snapshots**, not evidence that disconnecting at an arbitrary token is lossless. It also does not implement AO's native-TUI-to-chat controller switch.

## What stable ACP guarantees—and does not

ACP v1 models approval as `session/request_permission`, a typed RPC containing the session, referenced tool call, explicit options, and a selected/cancelled outcome ([`docs/protocol/v1/tool-calls.mdx:108-208`](https://github.com/agentclientprotocol/agent-client-protocol/blob/e446783993e5d3df5c88c629d0794a7755a74768/docs/protocol/v1/tool-calls.mdx#L108-L208)). Agent output arrives as ordered `session/update` notifications; message chunks can share an opaque `messageId`, although that ID is optional in v1 ([`docs/protocol/v1/prompt-turn.mdx:104-170`](https://github.com/agentclientprotocol/agent-client-protocol/blob/e446783993e5d3df5c88c629d0794a7755a74768/docs/protocol/v1/prompt-turn.mdx#L104-L170)).

For reconnection, the distinction is load-bearing:

- `session/load` must replay the entire conversation as ordinary updates and return only after replay completes ([`docs/protocol/v1/session-setup.mdx:83-188`](https://github.com/agentclientprotocol/agent-client-protocol/blob/e446783993e5d3df5c88c629d0794a7755a74768/docs/protocol/v1/session-setup.mdx#L83-L188)).
- `session/resume` restores model context but explicitly must not replay old history ([`session-setup.mdx:190-253`](https://github.com/agentclientprotocol/agent-client-protocol/blob/e446783993e5d3df5c88c629d0794a7755a74768/docs/protocol/v1/session-setup.mdx#L190-L253)).
- ACP does not supply persistence itself; the official RFD describes a proxy persisting intercepted events and implementing `load` over `resume` ([`docs/rfds/session-resume.mdx:38-44`](https://github.com/agentclientprotocol/agent-client-protocol/blob/e446783993e5d3df5c88c629d0794a7755a74768/docs/rfds/session-resume.mdx#L38-L44)).

ACP v1 has no event cursor, acknowledgment, controller lease, takeover transaction, or in-flight replay guarantee. Full `load` is a barrier for history the agent has already committed; it cannot recover a partial chunk that neither the agent nor AO persisted.

ACP v2 points toward the right model, but it is experimental: explicit `running` / `requires_action` / `idle` state ([`docs/protocol/v2/prompt-lifecycle.mdx:18-60`](https://github.com/agentclientprotocol/agent-client-protocol/blob/e446783993e5d3df5c88c629d0794a7755a74768/docs/protocol/v2/prompt-lifecycle.mdx#L18-L60)), required message IDs with keyed upserts and chunks ([`prompt-lifecycle.mdx:206-264`](https://github.com/agentclientprotocol/agent-client-protocol/blob/e446783993e5d3df5c88c629d0794a7755a74768/docs/protocol/v2/prompt-lifecycle.mdx#L206-L264)), and live terminal chunks plus an authoritative replacement snapshot for resynchronization ([`docs/rfds/v2/terminal-output.mdx:70-115`](https://github.com/agentclientprotocol/agent-client-protocol/blob/e446783993e5d3df5c88c629d0794a7755a74768/docs/rfds/v2/terminal-output.mdx#L70-L115)). AO can copy the semantics without claiming v2 compatibility.

## First-party connector evidence

### Claude Agent ACP

The official Claude connector receives permissions through the Claude Agent SDK's typed `canUseTool` callback, ensures the referenced tool call is emitted before requesting permission, and propagates cancellation with an `AbortSignal` ([`src/acp-agent.ts:5274-5353`](https://github.com/agentclientprotocol/claude-agent-acp/blob/e4dba808eaf280379a1218280081fdf0346632e1/src/acp-agent.ts#L5274-L5353), [`5356-5578`](https://github.com/agentclientprotocol/claude-agent-acp/blob/e4dba808eaf280379a1218280081fdf0346632e1/src/acp-agent.ts#L5356-L5578)). It does not scan text such as “Do you want to proceed?”.

For output, it associates partial chunks with a replay-stable provider message ID and reconciles them against the final consolidated assistant message so only missing content is forwarded ([`src/acp-agent.ts:4147-4275`](https://github.com/agentclientprotocol/claude-agent-acp/blob/e4dba808eaf280379a1218280081fdf0346632e1/src/acp-agent.ts#L4147-L4275), [`7695-7740`](https://github.com/agentclientprotocol/claude-agent-acp/blob/e4dba808eaf280379a1218280081fdf0346632e1/src/acp-agent.ts#L7695-L7740)). `loadSession` replays SDK-maintained history; `resumeSession` does not ([`1828-1848`](https://github.com/agentclientprotocol/claude-agent-acp/blob/e4dba808eaf280379a1218280081fdf0346632e1/src/acp-agent.ts#L1828-L1848), [`5204-5261`](https://github.com/agentclientprotocol/claude-agent-acp/blob/e4dba808eaf280379a1218280081fdf0346632e1/src/acp-agent.ts#L5204-L5261)).

### Codex app-server and Codex ACP

Codex exposes approval as an explicit active flag, independently of whether work is running ([`codex-rs/app-server-protocol/src/protocol/v2/thread.rs:1600-1621`](https://github.com/openai/codex/blob/636e505c5cd809bdce37314f77130ffb4e45c46b/codex-rs/app-server-protocol/src/protocol/v2/thread.rs#L1600-L1621), [`codex-rs/app-server/src/thread_status.rs:429-459`](https://github.com/openai/codex/blob/636e505c5cd809bdce37314f77130ffb4e45c46b/codex-rs/app-server/src/thread_status.rs#L429-L459)). Approval requests carry thread, turn, item, and optional distinct approval IDs ([`item.rs:1444-1488`](https://github.com/openai/codex/blob/636e505c5cd809bdce37314f77130ffb4e45c46b/codex-rs/app-server-protocol/src/protocol/v2/item.rs#L1444-L1488)); the documented lifecycle ends with `serverRequest/resolved` and an authoritative completed item ([`codex-rs/app-server/README.md:1679-1706`](https://github.com/openai/codex/blob/636e505c5cd809bdce37314f77130ffb4e45c46b/codex-rs/app-server/README.md#L1679-L1706)).

The output lifecycle is `item/started` → deltas → authoritative `item/completed`, bounded by `turn/completed` ([`README.md:1583-1624`](https://github.com/openai/codex/blob/636e505c5cd809bdce37314f77130ffb4e45c46b/codex-rs/app-server/README.md#L1583-L1624)). `thread/resume` reconstructs turns by default and exposes durable-history cursors for paginated history ([`README.md:359-365`](https://github.com/openai/codex/blob/636e505c5cd809bdce37314f77130ffb4e45c46b/codex-rs/app-server/README.md#L359-L365)); only one app-server process may own a paginated thread for writing, while read-only requests remain available. This directly supports AO's one-controller rule.

The official Codex ACP adapter follows the same pattern: it maps typed app-server approvals into ACP permission RPCs ([`src/CodexApprovalHandler.ts:72-163`](https://github.com/agentclientprotocol/codex-acp/blob/b51bedf60050c60fef78fc669e6ccf2ff61e3f47/src/CodexApprovalHandler.ts#L72-L163)), uses Codex item IDs for streamed output ([`src/CodexEventHandler.ts:484-486`](https://github.com/agentclientprotocol/codex-acp/blob/b51bedf60050c60fef78fc669e6ccf2ff61e3f47/src/CodexEventHandler.ts#L484-L486)), and makes `load` perform `threadRead(includeTurns: true)` plus normalized replay while `resume` only attaches ([`src/CodexAcpClient.ts:389-439`](https://github.com/agentclientprotocol/codex-acp/blob/b51bedf60050c60fef78fc669e6ccf2ff61e3f47/src/CodexAcpClient.ts#L389-L439), [`src/CodexAcpServer.ts:1493-1516`](https://github.com/agentclientprotocol/codex-acp/blob/b51bedf60050c60fef78fc669e6ccf2ff61e3f47/src/CodexAcpServer.ts#L1493-L1516)). On command completion it carries aggregate `rawOutput` and emits that aggregate when no live terminal delta was observed ([`CodexEventHandler.ts:852-883`](https://github.com/agentclientprotocol/codex-acp/blob/b51bedf60050c60fef78fc669e6ccf2ff61e3f47/src/CodexEventHandler.ts#L852-L883)).

Google's Gemini CLI independently uses the same design: native confirmation details become typed ACP permission requests ([`packages/cli/src/acp/acpSession.ts:720-809`](https://github.com/google-gemini/gemini-cli/blob/c0d192452b4e2df7efb6d62a60385f475bfd6779/packages/cli/src/acp/acpSession.ts#L720-L809)), streamed model events become session updates ([`acpSession.ts:405-442`](https://github.com/google-gemini/gemini-cli/blob/c0d192452b4e2df7efb6d62a60385f475bfd6779/packages/cli/src/acp/acpSession.ts#L405-L442)), and load replays persisted conversations ([`packages/cli/src/acp/acpSessionManager.ts:164-228`](https://github.com/google-gemini/gemini-cli/blob/c0d192452b4e2df7efb6d62a60385f475bfd6779/packages/cli/src/acp/acpSessionManager.ts#L164-L228)).

## Concrete application to PR #3953

### 1. Approval is not a draft

AO's Claude inspector already has independent work and composer facts, but its confirmation recognizer is tied to one exact question and one exact footer (`backend/internal/adapters/agent/claudecode/terminal_surface.go:99-118`). More importantly, handoff currently returns `errDrainDraftPresent` before it evaluates `WorkBlocked` (`backend/internal/session_manager/interface_transition.go:599-618`). Therefore even a correctly recognized approval can lose to a false draft.

Required behavior:

1. Evaluate `WaitingInput` or `Blocked` before `ComposerDraft`; a provider-owned
   interaction is not user-authored composer text. `Active` may coexist with a
   real draft, so either fact must still preserve the source controller.
2. Prefer provider-native typed state wherever the controller exposes it. For a pure native TUI, recognize the structural menu frame (question/header, selectable option rows, instruction/footer) with provider fixtures; do not depend on one English sentence. Partial or unfamiliar frames must return `Unknown`, not `Draft`.
3. A normal drain waits for the user to resolve the approval in the source interface. An interrupt switch must be explicit and described as cancelling the pending request/turn—not “discarding a draft.” Never synthesize an approval response by sending composer text.

This matches AO's existing chat boundary: `ResolveRequest` is already a typed action that is never derived from a message (`backend/internal/ports/chat.go:796-818`).

### 2. Output requires a commit-and-replay barrier

AO's existing `ChatHistoryReader` contract is a strong foundation: settled events have stable `ProviderEventID` values so repeated imports are idempotent (`backend/internal/ports/chat.go:821-831`). The immediate hole is Codex history: `ReadHistory` silently skips running and queued turns (`backend/internal/adapters/chatdriver/codexappserver/history.go:102-124`). That turns “history is not settled yet” into apparent success with missing output.

Use this handoff transaction:

1. **Freeze source input.** Only the current controller may write.
2. **Drain to a provider boundary.** Chat providers wait for their typed `turn/completed`/stop response. A TUI fallback requires durable idle plus a stable empty composer and no recognized interaction; it must then wait for provider history to converge.
3. **Record a checkpoint.** Persist the source's last settled provider turn/item identity and AO's normalized high-water sequence. Streamed chunks are responsive previews; a completed item/message/terminal snapshot is the repair authority.
4. **Close the source, then attach the target.** Do not run two writers. For Codex, read/resume native thread history; for ACP, use `session/load` when advertised. `session/resume` alone is context recovery, not transcript recovery.
5. **Replay and acknowledge.** Upsert by stable provider item/event ID, verify replay contains the checkpoint, then commit the new interface mode. If a running/queued turn appears, return a retryable “history not settled” result—never skip it. If replay cannot reach the checkpoint, fail the transition and recover the source interface.

For a normal switch requested during streaming, step 2 means the UI remains `draining` until the turn completes; output is not cut at an arbitrary token. If AO wants an explicit interrupt-and-switch path, it must first persist every accepted normalized event and mark the turn interrupted/incomplete. Provider-native context resumption cannot recreate bytes that were never committed.

PTY scrollback should remain a display/reattachment artifact. It contains redraws, chrome, approval options, and possibly uncommitted chunks, so it must not be parsed into canonical Chat messages. For native TUI visual continuity, keep a bounded terminal snapshot separately, analogous to ACP v2's replacement terminal snapshot.

## Verification matrix

- **Approval variants:** command, file edit, AskUserQuestion, wrapped/ANSI-rendered choices, redraw fragments, and localized/changed wording. Expected: waiting/blocked or unknown; never `DRAIN_DRAFT_PRESENT`; source remains alive.
- **Real draft:** idle frame with non-placeholder composer text. Expected: draft error; explicit discard/interrupt remains available.
- **Active stream in both directions:** request a switch during a long assistant response and during command output. Expected: transition stays draining through the provider completion marker; every completed message/tool output appears exactly once after the switch and after switching back.
- **History lag:** make the screen appear idle before native history exposes the final item. Expected: no successful transition until replay contains the checkpoint.
- **Crash windows:** restart after source close but before target replay and after replay but before mode commit. Expected: idempotent replay, no gap/duplicate, deterministic recovery to one controller.
- **ACP capabilities:** test full `session/load`, resume-only ACP, and failed/partial load. Resume-only must not promise transcript continuity unless AO's own journal already contains the authoritative snapshot.
- **Codex unsettled history:** return a running/queued turn from `thread/read`. Expected: retry/fail closed, not a successful empty import.
- **No styled capture / Windows ConPTY:** retain the durable legacy fallback; the new checkpoint rules must not turn lack of styled output into an infinite drain.

## Design boundary

The “once and for all” architecture is one daemon-owned normalized conversation stream with TUI and Chat as replaceable projections. Superset, Claude Agent ACP, Codex ACP, and Gemini all converge on typed interactions plus replayable provider history; none treats terminal text as the source of truth. Until AO's native TUI controllers can feed that same structured stream, the honest guarantee is **lossless at settled provider boundaries**, with explicit interruption semantics for everything else.
