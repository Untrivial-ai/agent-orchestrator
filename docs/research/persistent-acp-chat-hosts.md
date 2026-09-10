# Persistent ACP Chat hosts: findings and verification

Updated: 2026-09-08. Implementation decision:
[ADR 0003](../adr/0003-persistent-chat-provider-host.md).

## Why retaining only a PID is insufficient

ACP v1 makes `session/prompt` a long-lived JSON-RPC request. While it is pending,
providers stream notifications and can ask the client for permission or input.
A replacement SDK connection cannot infer the old request mapping or outstanding
responders. Native `session/load`/`session/resume` restores a conversation after
provider loss; it does not adopt an executing prompt on the same connection.
TCP delivery also does not prove that AO committed an event to SQLite.

These conclusions follow from the normative
[prompt lifecycle](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/prompt-turn.mdx),
[session setup](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/session-setup.mdx),
and [notification semantics](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/overview.mdx).

## Chosen seam

The existing `persistenthost` control plane owns one detached process per AO
session. Its ACP profile adds connection snapshots, request remapping, stable
interaction identities, and a bounded prompt journal. The shared ACP driver
retains typed protocol normalization and provider policy. Chat services retain
SQLite projection and controller-generation fencing. There is no second provider
registry, general lifecycle framework, or per-provider persistence switch.

All eight ACP bindings use this path. Their existing admission/version rules,
launch commands, capabilities, configuration setters, and native history repair
remain authoritative. Catalog discovery uses a separate short-lived process.

Live adoption uses stable session ownership, not a hash of mutable launch inputs.
This lets an update reconnect even if a provider installation moved or new
launch credentials would differ. The surviving process keeps its original
environment and process-fixed approval policy. Reconnect must not silently
reinitialize it or broaden its permissions.

## Provider-specific evidence and limits

Sources inspected for the initial research are pinned below. They establish
protocol/topology facts, not authenticated testing of every accepted version.

| Provider | Primary source revision or contract |
|---|---|
| ACP | [`agentclientprotocol/agent-client-protocol@01b9d6e`](https://github.com/agentclientprotocol/agent-client-protocol/tree/01b9d6e9c094d31cdea6d88768a9dd31b089ccef) |
| Claude Code | [`agentclientprotocol/claude-agent-acp@7c66108`](https://github.com/agentclientprotocol/claude-agent-acp/tree/7c6610835f26f18cd162b78dff74a7b7cd74497a); AO pins adapter `0.70.0` ([package](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/frontend/acp-runtime/package.json#L1-L8)) |
| Cursor | [Cursor CLI ACP documentation](https://cursor.com/docs/cli/acp); the provider implementation is not public |
| OpenCode | [`anomalyco/opencode@ebece6e`](https://github.com/anomalyco/opencode/tree/ebece6efd7b11401cf1e7390b5a22991b6608cc4) |
| Droid | [Factory `droid exec` documentation](https://docs.factory.ai/droid-exec/overview); the provider implementation is not public |
| Kimchi | [`earendil-works/kimchi@abbdff2`](https://github.com/earendil-works/kimchi/tree/abbdff2e3af107746ea904e0633c4c4f960bce7a) |
| Kimi | [`MoonshotAI/kimi-cli@ffb4577`](https://github.com/MoonshotAI/kimi-cli/tree/ffb4577c8b1c4cf4235fa635cf013583a03722a2) |
| Pi | [`victor-software-house/pi-acp@0ef24b2`](https://github.com/victor-software-house/pi-acp/tree/0ef24b24c97ac81a5e87a17d8fd74ef97fb34d8b) |
| OMP | [`can1357/oh-my-pi@fd6ee56`](https://github.com/can1357/oh-my-pi/tree/fd6ee563c15413685ac43dfce350902a8d75d997) |

Pi has an extra boundary: AO host → thin ACP client → Pi daemon. The thin-client
connection owns its Pi sessions; retaining it avoids disposal on disconnect
([connection cleanup](https://github.com/victor-software-house/pi-acp/blob/0ef24b24c97ac81a5e87a17d8fd74ef97fb34d8b/src/daemon/index.ts)).
This does not need a second AO ownership abstraction. Actual client, underlying
Pi session, and supported-platform continuity still need vendor E2E coverage.
The adapter uses Unix sockets; persistence does not imply Pi Windows support.
AO's explicit bypass-only admission remains unchanged.

Cursor, Kimchi, and OMP process-fixed permission policies remain fixed.
Persistence does not add approval support or change version floors. Negotiated
provider capabilities decide native recovery after actual host loss.

## Safety boundaries exercised by automated tests

- Real local detached host/provider processes survive repeated attachments for
  all eight harness identities. These use a fake ACP provider, not vendor accounts.
- Live adoption succeeds without native resume capability and without invoking
  a replacement launch that would fail. Changed model, environment, instructions,
  and mutable settings do not redefine ownership.
- Initialize/session/prompt are not repeated on live adoption. Durable AO turn
  ownership is restored before raw replay rebuilds the normalizer.
- Event and interaction IDs survive attachment changes but use a new random
  namespace after host replacement. Reused provider wire IDs retain causal order.
- Successful and failed prompt results have completion receipts. A new prompt
  cannot erase an active/unacknowledged journal.
- SQLite rollback retries in order. Persistent failure or ACK failure detaches
  without acknowledging past missing output or dispatching queued work.
  Completion ACK precedes the next queued dispatch.
- Permissions/input decisions are accepted idempotently by the host before the
  provider responder is released, closing the acceptance/projection crash window.
- Journals are bounded and owner-only. Overflow fails explicitly; incompatible,
  occupied, or ambiguous live ownership never starts a competing process.
- Failed fresh setup destroys its new host; failed adoption preserves the old
  owner. Explicit Stop has a host-shutdown fallback after controller loss;
  daemon-wide shutdown remains detach-only.
- The updater warns from actual daemon-reported ownership, with missing facts
  treated conservatively, rather than maintaining another provider allowlist.

## Authenticated evidence

The opt-in daemon E2E uses a tool-created marker as the acceptance barrier,
replaces the daemon while that tool is sleeping, and asserts completion of the
original durable turn under the same host PID. The provider is a child of that
unchanged host; it is not relaunched by the test.

Previous PR evidence (September 1) passed with Claude, OpenCode 1.18.15, and
Cursor 2026.08.11. Fresh September 8 runs passed for Claude and OpenCode. Cursor's
fresh attempt was blocked during the initial credentialed turn by
`Upgrade your plan to continue`; this is not a fresh Cursor survival pass.
Droid previously returned HTTP 402. No authenticated survival claim is made for
Droid, Kimi, Kimchi, Pi, or OMP. Shared fake-process coverage must not be reported
as vendor E2E coverage. Current commands and exact final-revision results belong
in the PR evidence comment.

## Deliberate non-goals

Host/machine failure is not daemon replacement. Only providers with native
load/resume can repair that loss, and an interrupted operation must not be
reported as surviving. The journal is not a second provider-history database.
TUI↔Chat eligibility remains its separate native-identity contract; persistent
ACP ownership does not enable interface switching for unsupported providers.
Runtime catalogs and provider versions remain provider-specific, and platform
source compilation alone is not a Windows runtime E2E claim.
