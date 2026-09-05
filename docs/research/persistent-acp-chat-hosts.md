# Persistent daemon-independent ACP Chat hosts

Date: 2026-09-01
AO revision: [`2bada3983f294c201578f32463fe0a140e650590`](https://github.com/Untrivial-ai/agent-orchestrator/tree/2bada3983f294c201578f32463fe0a140e650590)
Status: research and implementation recommendation; no product code is changed by this document

## Executive conclusion

AO can make every current ACP Chat provider survive a daemon or desktop restart, but it is **not safe to put the existing ACP connection behind the current Codex raw-stdio host and reconnect a fresh `acp-go-sdk` client**.

ACP v1 makes `session/prompt` one long-lived JSON-RPC request. While that request is outstanding, the agent streams unacknowledged `session/update` notifications and may issue concurrent permission or elicitation requests back to the client. The protocol has no active-turn snapshot, event cursor, delivery acknowledgement, command idempotency key, or way for a replacement client to adopt the outstanding prompt request. `session/load` and `session/resume` recover a provider session after a genuinely new connection; neither adopts an executing turn. See the normative [prompt lifecycle](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/prompt-turn.mdx#L59-L229), [session setup semantics](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/session-setup.mdx#L45-L253), and [notification semantics](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/overview.mdx#L217-L227).

The smallest safe reusable seam is therefore a **host-owned ACP connection core** built under the existing `persistenthost` control plane:

1. The detached host owns the provider child, the downstream JSON-RPC connection, initialization, downstream request IDs, the outstanding prompt, pending agent-to-client requests, and cancellation until each operation reaches a terminal result.
2. The daemon attaches through an AO-private, versioned command/event protocol. Commands have stable idempotency IDs. Events have monotonically increasing sequence numbers, a bounded disk-backed journal, and daemon acknowledgement only after durable projection commits.
3. On attach, the host returns a snapshot of the initialized session, active operation, pending interactions, accepted command outcomes, and journal range. It does not repeat ACP `initialize`, `session/new`, `session/load`, `session/resume`, or `session/prompt` for a live connection.
4. Only a conclusive host/provider death falls back to a fresh provider process plus ACP `session/load` or `session/resume`. The old live AO turn must then be marked interrupted/recovered, never falsely continued or completed.

This can cover Claude Code, Cursor, OpenCode, Droid, Kimchi, Kimi, Pi, and OMP because they all pass through AO's shared ACP driver. Rollout still needs provider-by-provider live gates because public lifecycle evidence and installed-version guarantees differ.

## Scope and source policy

AO currently registers one native Codex driver plus eight ACP Chat drivers: Claude Code, OpenCode, Droid, Kimi, Kimchi, Pi, Cursor, and OMP ([registry](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/registry/registry.go#L55-L77)). Codex is used as the implemented persistence reference but is not an ACP provider.

This report uses only:

- the AO repository at the revision above;
- the official ACP specification and official SDK source;
- first-party provider adapter source or first-party provider documentation.

Provider source revisions inspected:

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

The upstream revisions show current behavior, not necessarily the exact behavior of every minimum version AO accepts. Runtime `initialize` capabilities remain authoritative. Version-floor implications are listed under open questions.

## Verified facts: current AO ownership and recovery

### All ACP processes are daemon-owned today

`acp.Driver.Start` and `Resume` both call `connect`, which launches a provider child and performs `initialize`. Start then calls `session/new`; Resume chooses `session/load` when advertised, otherwise `session/resume` ([AO start/resume](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/acp/driver.go#L129-L303)). The child is an ordinary `exec.Command` with daemon-owned stdio; stopping it closes stdin, waits briefly, then kills the process tree ([process owner](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/acp/process.go#L20-L74)).

The active AO provider-turn ID, prompt waiter, permission/input waiters, and message/thought/tool aggregation maps all live in the daemon's `acp.conversation`. `StartDeferredTurn` installs those maps and starts the long-running `conn.Prompt`; `runTurn` converts the response to terminal AO state ([turn ownership](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/acp/conversation.go#L398-L502)).

`conversation.Close` currently cancels the turn context, fails pending permissions and inputs, sends `session/close`, and stops the child ([close behavior](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/acp/conversation.go#L593-L627)). This must become detach for a persistent ACP conversation; explicit termination must remain destructive.

### Codex persistence already supplies useful lifecycle boundaries

The merged Codex implementation established the right outer control plane: a capability-bearing mode-0600 descriptor, exact protocol-version fencing, exclusive attachment, detach versus explicit terminate, and conservative orphan reconciliation ([ADR 0003](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/docs/adr/0003-persistent-chat-provider-host.md#L16-L81)). The ports already expose the optional seams ACP needs to implement: `ChatProviderPreserver`, `ChatProviderTerminator`, and `ChatLiveReconnector` ([ports](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/ports/chat.go#L929-L948)).

On a live reconnect the service skips orphan settlement and native history import, loads the durable conversation, and restores the controller's busy gate from the latest running provider turn ([service reconnect](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/service/chat/service.go#L468-L537), [busy restoration](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/service/chat/controller.go#L315-L336)). That restores service ownership, but not the ACP driver's active turn, JSON-RPC waiter, pending interactions, or aggregation maps.

Startup reconciliation currently keeps only non-terminated Codex Chat hosts ([keep set](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/daemon/persistent_chat_wiring.go#L15-L34)). ACP rollout must generalize this from a Codex harness check to a compatible persistent-host descriptor/profile check.

### The current raw host is not an acknowledgement journal

The current host retains a 32 MiB in-memory detached frame buffer, pending provider-to-client request frames, and the greatest numeric client request ID ([host state](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/persistenthost/host.go#L513-L534)). On attach it writes buffered frames to the socket and immediately clears the buffer after a successful socket write ([attach replay](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/persistenthost/host.go#L575-L599)). Therefore a replacement daemon can disconnect after receipt but before SQLite projection and permanently lose that frame. ADR 0003 already requires a sequence-numbered disk-backed journal with daemon acknowledgements before protocols need more than the Codex-first bridge ([ADR future requirement](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/docs/adr/0003-persistent-chat-provider-host.md#L67-L81)). ACP does.

The current ACP event channel can also drop deltas immediately when its consumer is behind and can drop lifecycle events after five seconds ([event emission](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/acp/conversation.go#L629-L651)). A persistence claim requires removing this lossy boundary for host-journaled events.

## Verified facts: ACP v1 lifecycle and gaps

### Initialization and session setup

The client must initialize the connection before session setup and negotiate capabilities. Only the baseline session operations are universal; load, resume, close, MCP transport features, prompt content types, and other client/agent capabilities are optional ([initialization](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/initialization.mdx#L24-L110), [capability negotiation](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/initialization.mdx#L243-L267)). A live host must initialize its downstream provider connection once and retain the negotiated response for daemon attachments.

`session/new` creates a unique provider session. `session/load` restores a persisted session and must replay the stored conversation through `session/update` before returning. `session/resume` restores context without transcript replay ([session setup](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/session-setup.mdx#L45-L253)). Neither method adopts an already executing `session/prompt` request.

AO must retain the original setup inputs—cwd, additional directories, MCP servers, metadata/profile, and launch-derived environment—for genuine post-host-death recovery. A live daemon reattach must not invoke setup again.

### Active prompts and updates

One `session/prompt` request remains outstanding for the entire turn. The agent emits `session/update` notifications and agent-to-client requests while it runs; the final prompt response carries the `stopReason` ([prompt turn](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31b089ccef/docs/protocol/v1/prompt-turn.mdx#L59-L229)). Retrying a prompt after an ambiguous daemon disconnect is not idempotent and can duplicate tools, edits, or model work.

JSON-RPC notifications have no response. ACP v1 defines no delivery sequence or acknowledgement cursor for `session/update` ([overview](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/overview.mdx#L6-L39)). Optional provider message IDs are content correlation keys, not transport cursors.

### Bidirectional requests and request IDs

Permission, filesystem, terminal, and elicitation operations can be agent-to-client requests while the client-origin prompt remains pending. The canonical TypeScript SDK owns request counters and pending maps inside one connection and aborts/clears them on connection loss ([SDK request maps](https://github.com/agentclientprotocol/typescript-sdk/blob/5dac09aaae3ebde1eaaf4a11840f7543f4806e20/src/jsonrpc.ts#L853-L873), [request dispatch](https://github.com/agentclientprotocol/typescript-sdk/blob/5dac09aaae3ebde1eaaf4a11840f7543f4806e20/src/jsonrpc.ts#L1107-L1175), [disconnect cleanup](https://github.com/agentclientprotocol/typescript-sdk/blob/5dac09aaae3ebde1eaaf4a11840f7543f4806e20/src/jsonrpc.ts#L1438-L1505)). JSON-RPC IDs are scoped to a connection and may be strings or integers; IDs in the two directions are independent.

AO's pinned Go SDK similarly owns an unexported atomic `nextID`, pending-response map, inbound request cancellation map, and notification queue in each `Connection` ([pinned SDK state](https://github.com/coder/acp-go-sdk/blob/v0.13.5/connection.go#L53-L120)). A fresh connection starts request IDs again and exposes no supported API to seed the counter or adopt an existing pending request; `SendRequest` creates the waiter immediately before sending the request ([request implementation](https://github.com/coder/acp-go-sdk/blob/v0.13.5/connection.go#L623-L681)).

AO currently hides the provider JSON-RPC request identity inside the SDK handler and generates a fresh UUID for each projected permission or input request ([permission identity](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/acp/client.go#L52-L135), [input identity](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/acp/client.go#L202-L235)). Replaying the raw provider request into a new handler would create a duplicate durable approval/input while leaving the old one unresolved.

### Permissions and cancellation

Every permission request must receive a matching selected or cancelled result. When a prompt is cancelled, pending permission requests must be settled as cancelled ([tool-call permissions](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/tool-calls.mdx#L108-L168), [prompt cancellation obligations](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/prompt-turn.mdx#L312-L345)). The host must retain the original downstream request ID/responder and re-publish one stable AO request after attach.

Baseline `session/cancel` is a notification. Cancellation is terminal only when the original `session/prompt` returns `stopReason: cancelled`; generic `$/cancel_request` is optional and cooperative ([cancellation](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v1/cancellation.mdx#L6-L67)). Daemon detach must do nothing. Explicit cancellation must be a host-owned idempotent command, settle pending interactions, retain late updates, and remain `cancelling` until the prompt returns.

### Transport and provider loss

The stable stdio transport assumes the client launches the subprocess and closes stdin/terminates it during teardown; reconnect behavior belongs to custom transports ([stdio transport](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31b089ccef/docs/protocol/v1/transports.mdx#L17-L52)). The official transport RFD explicitly leaves liveness/retry to implementations and does not provide in-flight replay or sequencing ([transport RFD](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31b089ccef/docs/rfds/streamable-http-websocket-transport.mdx#L70-L84)).

If the host or provider actually dies, AO may launch a new process, initialize it, and use its advertised load/resume capability. That is native session recovery, not continuation of the old prompt. If neither capability is advertised, post-host-death recovery is unavailable even though live host reattachment can still work.

## Provider matrix

The table distinguishes what a persistent host can preserve from what a newly launched provider can recover.

| Provider | Process launched by AO Chat | Verified setup/recovery semantics | Active prompt, interactions, and cancellation | Provider-specific constraints |
|---|---|---|---|---|
| Claude Code | AO's pinned `claude-agent-acp` Node runtime, pointed at the user's Claude executable ([AO binding](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/claudeacp/driver.go#L34-L101)) | Adapter advertises load plus close/delete/fork/list/resume; load replays history, resume does not ([initialize/setup](https://github.com/agentclientprotocol/claude-agent-acp/blob/7c6610835f26f18cd162b78dff74a7b7cd74497a/src/acp-agent.ts#L1694-L1804)) | `prompt` creates an in-memory `Turn`, queues it into a persistent Claude SDK query stream, and awaits its deferred result ([prompt](https://github.com/agentclientprotocol/claude-agent-acp/blob/7c6610835f26f18cd162b78dff74a7b7cd74497a/src/acp-agent.ts#L1979-L2055)). Permissions race a typed client request with cancellation and may send `$/cancel_request` ([permission](https://github.com/agentclientprotocol/claude-agent-acp/blob/7c6610835f26f18cd162b78dff74a7b7cd74497a/src/acp-agent.ts#L5854-L5897)). | Disconnect disposal tears down all active sessions ([dispose](https://github.com/agentclientprotocol/claude-agent-acp/blob/7c6610835f26f18cd162b78dff74a7b7cd74497a/src/acp-agent.ts#L5366-L5369)). Same-process preservation is necessary for the query stream, prompt queue, and pending tool/user input. Rich extensions mean the host must park or deliberately decline every advertised agent-to-client request type. |
| Cursor | User's `cursor-agent ... acp`, with AO-managed standing-rule plugin and launch-time permission flags ([AO binding](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/cursoracp/driver.go#L23-L70)) | First-party docs define initialize, new/load, prompt/update/permission/cancel over NDJSON stdio ([Cursor ACP docs](https://cursor.com/docs/cli/acp)). AO's opt-in live test verifies load-based resume, cancellation, tool/approval flow, dynamic options/commands, and a blocking ask-question extension ([live test](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/cursoracp/live_test.go#L17-L103)). | Permission options include allow-once, allow-always, and reject. `cursor/ask_question` and `cursor/create_plan` are blocking provider extensions in AO's client binding. | Provider source is closed; PID/connection continuity, detach behavior, and exact missed-frame behavior require black-box E2E tests. `--auto-review` and `--force` are process-launch modes, so switching into/out of them requires a Chat restart. Cursor remains Chat-only until TUI/ACP conversation identity is proven. |
| OpenCode | User's `opencode acp` ([AO binding](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/opencodeacp/driver.go#L16-L42)) | Advertises load and close/fork/list/resume. Load fetches all native messages and replays them; resume fetches recent messages only to restore model/mode state and does not replay ([initialize/load](https://github.com/anomalyco/opencode/blob/ebece6efd7b11401cf1e7390b5a22991b6608cc4/packages/opencode/src/acp/service.ts#L94-L244), [resume](https://github.com/anomalyco/opencode/blob/ebece6efd7b11401cf1e7390b5a22991b6608cc4/packages/opencode/src/acp/service.ts#L292-L328)). | Prompt awaits the backing OpenCode session inside an event `runUntilIdle` boundary ([prompt](https://github.com/anomalyco/opencode/blob/ebece6efd7b11401cf1e7390b5a22991b6608cc4/packages/opencode/src/acp/service.ts#L494-L528)). Cancel and close abort the backing session ([cancel/close](https://github.com/anomalyco/opencode/blob/ebece6efd7b11401cf1e7390b5a22991b6608cc4/packages/opencode/src/acp/service.ts#L330-L354)). Native permission events become ACP `requestPermission` calls ([permission](https://github.com/anomalyco/opencode/blob/ebece6efd7b11401cf1e7390b5a22991b6608cc4/packages/opencode/src/acp/permission.ts#L38-L99)). | The ACP session registry, directory snapshot, event subscription, and permission queue are process memory. Preserve the connection for a live turn; use native load only after host death. |
| Droid | User's `droid exec --output-format acp-daemon` ([AO binding](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/droidacp/driver.go#L17-L54)) | Public first-party documentation defines the `droid exec`/`acp-daemon` surface but does not publish an implementation-level lifecycle contract ([Factory docs](https://docs.factory.ai/droid-exec/overview)). AO therefore relies on the runtime initialize response for load/resume capability. | AO's opt-in live test currently proves only start, one streaming prompt, and completion ([live test](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/droidacp/live_test.go#L15-L70)). | Treat the provider as a black box. Daemon-restart, pending permission, cancellation, and load/resume tests are mandatory before enabling persistence. Bypass permission is a process flag (`--skip-permissions-unsafe`). |
| Kimchi | User's `kimchi --mode acp` with launch-time model/system/permission flags ([AO binding](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/kimchiacp/driver.go#L16-L71)) | Current source advertises `loadSession`, list, and close—not resume ([initialize](https://github.com/earendil-works/kimchi/blob/abbdff2e3af107746ea904e0633c4c4f960bce7a/src/modes/acp/server.ts#L383-L440)). Load opens the persisted session, installs subscriptions/prompters, seeds stable transcript item counters, and replays before responding ([load](https://github.com/earendil-works/kimchi/blob/abbdff2e3af107746ea904e0633c4c4f960bce7a/src/modes/acp/server.ts#L743-L861)). | Prompt explicitly owns an in-memory turn and rejects a second root prompt; cancellation aborts and clears the follow-up queue ([prompt/cancel](https://github.com/earendil-works/kimchi/blob/abbdff2e3af107746ea904e0633c4c4f960bce7a/src/modes/acp/server.ts#L883-L992)). Permissions use abort-aware ACP requests ([prompter](https://github.com/earendil-works/kimchi/blob/abbdff2e3af107746ea904e0633c4c4f960bce7a/src/modes/acp/acp-prompter.ts#L10-L59)). | On disconnect, Kimchi rejects active prompt results and disposes every session ([shutdown](https://github.com/earendil-works/kimchi/blob/abbdff2e3af107746ea904e0633c4c4f960bce7a/src/modes/acp/server.ts#L1017-L1069)); the host must keep stdio connected. AO accepts Kimchi from 0.0.7, but current upstream has a much richer load implementation; release/version compatibility needs a gate. MCP servers passed by the client are rejected; configure them in Kimchi. |
| Kimi | User's `kimi acp` ([AO binding](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/kimiacp/driver.go#L17-L57)) | Advertises load plus list/resume. New creates a durable Kimi session; load recreates the CLI and replays its wire file; resume recreates context without replay ([initialize](https://github.com/MoonshotAI/kimi-cli/blob/ffb4577c8b1c4cf4235fa635cf013583a03722a2/src/kimi_cli/acp/server.py#L42-L113), [setup/load/resume](https://github.com/MoonshotAI/kimi-cli/blob/ffb4577c8b1c4cf4235fa635cf013583a03722a2/src/kimi_cli/acp/server.py#L214-L296)). | Each prompt creates an in-memory random turn ID, tool map, and cancel event; tool IDs are turn-prefixed ([turn state](https://github.com/MoonshotAI/kimi-cli/blob/ffb4577c8b1c4cf4235fa635cf013583a03722a2/src/kimi_cli/acp/session.py#L79-L162)). Cancellation sets that event ([cancel](https://github.com/MoonshotAI/kimi-cli/blob/ffb4577c8b1c4cf4235fa635cf013583a03722a2/src/kimi_cli/acp/session.py#L307-L317)); approvals are blocking ACP permission requests ([approval](https://github.com/MoonshotAI/kimi-cli/blob/ffb4577c8b1c4cf4235fa635cf013583a03722a2/src/kimi_cli/acp/session.py#L469-L531)). | History replay intentionally omits approval/question request records and creates fresh replay turn IDs ([history](https://github.com/MoonshotAI/kimi-cli/blob/ffb4577c8b1c4cf4235fa635cf013583a03722a2/src/kimi_cli/acp/session.py#L250-L305)). It is a repair source after host death, not a mechanism for adopting live interactions. Kimi exposes only its default permission mode through AO. |
| Pi | User's independently installed `pi-acp`; AO assigns a per-session `PI_ACP_SOCKET_DIR` ([AO binding](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/piacp/driver.go#L35-L94)) | Adapter advertises load plus list/close/resume/fork ([initialize](https://github.com/victor-software-house/pi-acp/blob/0ef24b24c97ac81a5e87a17d8fd74ef97fb34d8b/src/acp/agent.ts#L543-L580)). Load opens Pi's session file and replays history ([load](https://github.com/victor-software-house/pi-acp/blob/0ef24b24c97ac81a5e87a17d8fd74ef97fb34d8b/src/acp/agent.ts#L966-L1024)). Resume can attach to a session already held by another connection in the same pi-acp daemon, otherwise loads from disk without replay ([resume](https://github.com/victor-software-house/pi-acp/blob/0ef24b24c97ac81a5e87a17d8fd74ef97fb34d8b/src/acp/agent.ts#L1135-L1201)). | Pi queues prompts in its connection-local wrapper and cancel aborts the active Pi session plus queued prompts ([prompt/cancel](https://github.com/victor-software-house/pi-acp/blob/0ef24b24c97ac81a5e87a17d8fd74ef97fb34d8b/src/acp/session.ts#L388-L410)). The adapter does not issue ACP permission requests, matching AO's approvals=false admission rule. | `pi-acp` is itself a thin stdio-to-Unix-socket client ([client](https://github.com/victor-software-house/pi-acp/blob/0ef24b24c97ac81a5e87a17d8fd74ef97fb34d8b/src/client/index.ts#L1-L44)). Its daemon disposes a connection handle on socket close ([daemon](https://github.com/victor-software-house/pi-acp/blob/0ef24b24c97ac81a5e87a17d8fd74ef97fb34d8b/src/daemon/index.ts#L91-L121)); when that connection was the sole owner, the agent releases and disposes the underlying Pi session ([dispose](https://github.com/victor-software-house/pi-acp/blob/0ef24b24c97ac81a5e87a17d8fd74ef97fb34d8b/src/acp/agent.ts#L220-L238)). Its daemon alone does not replace AO host continuity: AO must keep the thin client's socket connection alive. Current adapter transport is POSIX Unix-socket-specific. |
| OMP | User's `omp acp`, minimum AO version 15.0.0, with process-launch model/system/approval flags ([AO binding](https://github.com/Untrivial-ai/agent-orchestrator/blob/2bada3983f294c201578f32463fe0a140e650590/backend/internal/adapters/chatdriver/ompacp/driver.go#L18-L69)) | Advertises load plus list/fork/resume/close. Load replays history; resume restores without replay ([initialize/setup](https://github.com/can1357/oh-my-pi/blob/fd6ee563c15413685ac43dfce350902a8d75d997/packages/coding-agent/src/modes/acp/acp-agent.ts#L629-L758)). | OMP maintains an explicit in-memory prompt-turn queue and cancellation cleanup barrier ([prompt](https://github.com/can1357/oh-my-pi/blob/fd6ee563c15413685ac43dfce350902a8d75d997/packages/coding-agent/src/modes/acp/acp-agent.ts#L818-L870), [cancel](https://github.com/can1357/oh-my-pi/blob/fd6ee563c15413685ac43dfce350902a8d75d997/packages/coding-agent/src/modes/acp/acp-agent.ts#L1067-L1101)). It issues blocking ACP permissions ([bridge](https://github.com/can1357/oh-my-pi/blob/fd6ee563c15413685ac43dfce350902a8d75d997/packages/coding-agent/src/modes/acp/acp-client-bridge.ts#L114-L153)). | `omp acp` waits for the stdio connection to close and then disposes sessions ([mode](https://github.com/can1357/oh-my-pi/blob/fd6ee563c15413685ac43dfce350902a8d75d997/packages/coding-agent/src/modes/acp/acp-mode.ts#L39-L62)). Approval mode is fixed at process launch; changing it requires restarting Chat. OMP can queue prompts internally, but AO must retain its own one-root-turn controller invariant. |

## Why the tempting raw-stdio reuse is incorrect

The following failure sequence is deterministic, not theoretical:

1. Old daemon's Go ACP connection sends `session/prompt` with numeric request ID 7 and stores its response waiter in daemon memory.
2. The provider emits several message deltas; AO projects some to SQLite.
3. The daemon exits. The current raw host preserves provider stdio and buffers subsequent frames.
4. A new daemon creates a new Go ACP connection. Its request counter starts at zero and it has no waiter for ID 7. Merely telling it that the next ID is greater than 7 is impossible through the supported SDK API and would still not recreate the waiter.
5. The host replays updates. The new ACP conversation has no `activeTurn`, so updates lack the AO provider-turn ID. Its message/tool aggregation maps also lack the already-projected prefix.
6. A replayed permission request enters a fresh SDK handler, which creates a new AO UUID. The UI now has duplicate or orphaned durable requests.
7. The provider returns response ID 7. The new SDK has no matching pending call, so the terminal prompt result cannot settle the running AO turn.

Even a fork of `acp-go-sdk` that seeds `nextID` solves only the collision in step 4. It does not adopt the old pending prompt, preserve the server-request responder, restore active-turn attribution, rebuild aggregates, or close the receive-to-SQLite acknowledgement gap.

There is a second data-loss window after replay: the current host forgets a frame once it writes it to the daemon socket, while correctness requires forgetting it only after the daemon's durable projection commits. TCP delivery is not a database acknowledgement.

## Recommended smallest safe seam

### 1. Retain the current generic host control plane

Keep these existing properties in `persistenthost`:

- per-session detached host process;
- loopback-only endpoint and private capability token;
- mode-0600 descriptor directory;
- exact host protocol-version match;
- exclusive controller attachment;
- conservative ownership/orphan reconciliation;
- detach versus authenticated explicit shutdown.

Do not create eight provider host implementations. Provider launch/configuration remains in the current `claudeacp`, `cursoracp`, `opencodeacp`, `droidacp`, `kimchiacp`, `kimiacp`, `piacp`, and `ompacp` bindings.

### 2. Add one stateful ACP profile under the host

Add an ACP-specific layer, conceptually `chatdriver/persistenthost/acphost` (name is illustrative), that is the actual downstream JSON-RPC client. It should be a small stateful RPC router rather than a second copy of every provider binding.

The host owns:

- child stdio for its lifetime;
- one downstream initialization result and setup result;
- numeric client request allocation above a retained high-water mark;
- the downstream request/response map, including the active `session/prompt`;
- raw provider request IDs and responders for permission, elicitation, filesystem, terminal, and extension requests;
- active command state: idle, active, cancelling, terminal;
- an idempotent command ledger keyed by AO host-command ID;
- a monotonic event sequence and bounded disk-backed journal;
- enough active-turn raw frames or a compact state snapshot to rebuild the daemon-side ACP normalizer after attach;
- explicit generation/identity for the host and provider process.

The daemon-side shared ACP conversation should call this private transport through a narrow typed interface instead of constructing `acp-go-sdk.Connection` directly. Typed ACP request/response conversion and the current provider-specific policies/extensions can remain in the daemon. The host private protocol needs generic envelopes such as:

```text
attach(after_durable_seq, expected_host_generation)
  -> snapshot(initialized session, active operation, pending interactions,
              accepted command outcomes, journal bounds)

command(command_id, method, params)
  -> accepted/result, idempotently

resolve_interaction(command_id, stable_interaction_id, result)
cancel(command_id, active_operation_id)
ack(durable_seq)

event(seq, operation_id, provider notification/response/request)
```

The names and wire format are open design details; the ownership semantics are not.

### 3. Use host identities, not replay-created daemon identities

Each agent-to-client request gets one host-stable interaction ID, persisted with its original downstream raw JSON-RPC ID and method. A daemon reconnect projects or upserts the same durable approval/input row and returns its decision against that stable ID. The provider sees one response to its original request.

Each daemon-to-host command has a stable command ID. If the daemon dies after the host accepts a prompt/cancel/decision but before the acknowledgement reaches the daemon, retry returns the already accepted outcome and never sends the provider command twice.

### 4. Make journal acknowledgement a durable projection boundary

Every host event has a sequence. The daemon supplies its last durably committed sequence when it attaches, deduplicates by host generation plus sequence, and acknowledges only after the corresponding SQLite transaction commits. The host may compact terminal operations only after all events and interaction outcomes through that operation are acknowledged.

For an active turn, either:

- the host retains the full active-turn event log and the daemon replays the acknowledged prefix in rebuild-only mode to restore message/thought/tool aggregation before projecting unacknowledged events; or
- the host returns a semantic aggregate snapshot that is proven equivalent.

The first option reuses the current ACP normalization code and is the smaller initial seam. It also avoids making the host depend on AO storage or provider-specific transcript shapes.

### 5. Extend existing conversation/service hooks, not service ownership

ACP persistent conversations should implement:

- `PreservesProviderOnClose() == true` when attached through a compatible host;
- `Close()` as attachment detach only;
- `Terminate()` as authenticated host shutdown plus provider/session close;
- `ReconnectedLive() == true` only after attaching the exact same initialized host generation.

The service should continue to own SQLite projection and the one-controller fence. It additionally needs a narrow acknowledgement hook invoked after durable event commit. `restoreLiveTurnOwnership` remains useful, but the ACP proxy must also adopt the host's active operation and bind it to that durable provider-turn ID before replay.

### 6. Fail closed on ambiguous ownership or incompatibility

Do not launch a competing provider when a live descriptor exists but attachment, authentication, version, or ownership is inconclusive. Preserve the host and surface recovery-inconclusive, as Codex does now.

Descriptor compatibility must include the AO private host protocol, ACP host-profile version, and provider launch/config fingerprint. A replacement daemon must not silently apply new launch flags, permission mode, model, environment, or system prompt to an already-running process. Settings that are runtime-mutable can be sent later as ordinary idempotent ACP commands.

## Provider-loss recovery policy

Recovery must distinguish three cases:

| Evidence | Action | AO turn result |
|---|---|---|
| Same compatible host generation attaches | Adopt snapshot/journal; do not initialize/setup/prompt again | Continue the same running turn and provider-turn ID |
| Host/provider conclusively dead; provider advertises load or resume | Launch, initialize, then load when transcript repair is required/available, otherwise resume | Mark old live turn interrupted or recovered according to durable policy; never claim its prompt response survived |
| Ownership inconclusive or provider has neither load nor resume | Preserve possible owner and fail closed; do not launch a rival | Leave recovery explicitly inconclusive/unavailable |

Load history can repair completed durable content after a host crash. It cannot recreate an uncommitted side effect, an in-flight permission decision, the final response to an old prompt request, or the exact ordering of missing live notifications.

## Rollout recommendation

1. Build and prove the generic fake ACP host path first. No provider credentials should be required for the core crash matrix.
2. Enable Claude Code first. AO controls the pinned adapter version, its source documents prompt/disconnect semantics, and AO already has an opt-in full-boundary live test.
3. Enable OpenCode, Kimi, and Kimchi next after real-provider daemon-restart and pending-permission tests. Their source is public and their load semantics are inspectable.
4. Enable OMP after validating its internal prompt queue and cancellation cleanup across attach. Preserve AO's one-root-turn gate despite provider-side queuing.
5. Enable Pi after testing the two-level topology: AO host -> thin `pi-acp` client -> Pi daemon. Verify both thin-client PID/socket continuity and underlying Pi session identity. Keep the current explicit bypass admission because persistence does not add permission enforcement.
6. Enable Cursor and Droid only after black-box live gates cover the source gaps. Cursor's current live test is a strong base; Droid's is not yet sufficient.

Rollout should be capability/profile-gated, not `persistent=true` for every ACP harness at once. If a provider or accepted version fails a required probe, retain current native restart plus load/resume behavior.

## Required automated and live evidence

### Credential-free fake ACP integration suite

The fake provider must expose controllable barriers around every boundary below and record initialize/setup/prompt/cancel/decision counts plus its PID.

1. **Active prompt:** emit updates before and during detach, then return the prompt result. Assert same provider PID, no second initialize/setup/prompt, ordered exact-once durable events, one completed AO turn, and preserved provider-turn ID.
2. **Receive/commit crash:** kill the daemon after an event is received but before SQLite commit. Attach with the prior durable sequence and prove the event is replayed and committed exactly once.
3. **Commit/ack crash:** kill after commit but before host ACK. Prove replay dedupes by host generation/sequence and does not append content twice.
4. **Pending permission:** detach after the provider request, reconnect, show the same durable request ID/options, resolve once, and assert one response with the original downstream JSON-RPC ID.
5. **Pending elicitation/extension:** repeat for structured input and one unknown-but-forwarded extension request; prove capability-specific requests are parked rather than lost.
6. **Cancellation after attach:** cancel immediately after reconnect. Assert one `session/cancel`, all pending permissions settle cancelled, late updates remain ordered, and terminal state waits for the original prompt's cancelled result.
7. **Ambiguous command acceptance:** disconnect after host acceptance of prompt, cancel, and interaction resolution but before command response. Retry each command ID and assert no provider duplicate.
8. **Queued AO turn:** enqueue a follow-up while detached. Assert it stays queued until the adopted prompt reaches a terminal result and is sent once afterward.
9. **Backpressure and bounds:** fill the detached journal beyond configured memory limits. Assert bounded disk/memory behavior, no silent drops, and explicit backpressure/failure policy.
10. **Provider/host crash:** conclusively kill the host. Assert a new process initializes and loads/resumes, the old active turn is interrupted/recovered, and no old prompt is resent.
11. **No native recovery capability:** repeat with an agent advertising neither load nor resume. Live attach succeeds; actual host-death recovery reports unavailable.
12. **Version/config mismatch:** a new daemon with an incompatible host profile or launch fingerprint fails closed and does not start a competing provider.
13. **Bidirectional ID types:** exercise integer and string provider request IDs, independent IDs in both directions, late responses, `$/cancel_request`, and ID reuse only after the original operation is terminal.
14. **Load ordering:** on actual provider relaunch, ensure every load replay notification is durably consumed before the load response makes the controller ready.
15. **Explicit termination:** terminate the AO session and prove host, provider process tree, descriptor, pending interactions, and journal are cleaned up; ordinary daemon close must leave all of them alive.

### Real-provider E2E gate for each enabled binding

For each provider/version gate, run at least:

- graceful daemon replacement during streaming output;
- forced daemon kill during streaming output;
- restart while a permission or input UI is blocked, when the provider supports it;
- restart followed immediately by Stop;
- restart with an AO follow-up already queued;
- real provider PID/process-tree continuity proof;
- explicit Chat termination cleanup;
- host kill followed by native load/resume and transcript reconciliation.

Existing AO live coverage is not this gate. Claude currently proves fresh and resumed completed turns; Cursor additionally proves tool, approval, cancellation, load-based resume, and blocking input. OpenCode, Droid, Kimchi, and OMP currently prove only a fresh completed turn; Kimi and Pi have no provider live test in their Chat binding directories. The current tests are opt-in and credential-dependent, which is appropriate for provider validation but cannot replace the fake crash suite.

Run the fake suite with process and socket variants on macOS, Linux, and Windows where the provider is supported. Pi's current first-party adapter requires a Unix-socket-specific gate and should not be represented as Windows-capable without first-party transport support.

## Inferences and design judgments

The following are conclusions derived from the verified facts above, not ACP guarantees:

1. **The host must be the logical ACP client.** Keeping only the provider PID and raw bytes while rebuilding the SDK in the daemon cannot preserve an in-flight request/response relationship.
2. **A custom AO-private host protocol is smaller than modifying every provider.** All eight providers already share AO's ACP driver; downstream provider code need not know that daemon attachments change.
3. **Forking `acp-go-sdk` alone is insufficient.** Seeded IDs and an adopt-request API would still leave provider-to-client requests, event acknowledgement, active-turn normalization, and idempotent commands unsolved.
4. **Full active-turn replay in rebuild-only mode is the smallest initial normalizer repair.** It reuses current ACP-to-`ChatEvent` logic and durable projection while avoiding a second semantic model in the host.
5. **Provider-side durable history is a repair source, not an exactly-once log.** Provider history formats omit or transform live-only details, and ACP load replay has no shared cursor with AO's SQLite event sequence.
6. **Per-session hosts remain the right isolation unit.** They match AO controller ownership, permission/config launch state, explicit termination, conservative reconciliation, and the existing security boundary.
7. **Live reattachment can support providers with no load/resume.** Those capabilities matter only after actual host/provider death; they are not needed while the same initialized connection survives.
8. **Host persistence must not broaden permissions.** Pi remains approval-incapable in AO; Cursor/OMP/Kimchi process-fixed permission modes remain fixed; a daemon update cannot reinterpret a running host's launch policy.

## Open questions before implementation

1. **Private event representation:** Should the host journal raw provider JSON-RPC frames plus operation metadata, or a minimal parsed envelope? Raw frames minimize semantic duplication, but every entry still needs stable operation/interaction identity and sequence metadata.
2. **Normalizer rebuild contract:** Is full active-turn raw replay with `project=false` sufficient for every ACP aggregate and extension, or should the host persist an explicit aggregate snapshot? This should be proven against current `settleOpenItems`, tool-update merging, nested-agent content, plans, usage, and structured input.
3. **ACK transaction boundary:** Which controller/store method returns the definitive committed event sequence, especially for one provider update that projects multiple rows/events? The ACK API must align with the real SQLite transaction, not channel receipt.
4. **Journal storage:** File WAL versus a host-owned SQLite database; compaction, checksum/corruption handling, fsync policy, maximum detached duration, quota, and secure cleanup need an ADR amendment.
5. **Interaction expiry:** Current daemon-side approvals time out. Should timeout ownership move to the host so daemon downtime does not accidentally extend or cancel it? The answer must be deterministic and visible in the snapshot.
6. **Advertised client capabilities:** The host must be able to service every capability AO advertises while detached. Which filesystem, terminal, auth, elicitation, and vendor extension capabilities should AO continue advertising for the first slice?
7. **Provider launch fingerprint:** Precisely which fields fence live attachment—binary path/version, argv, selected profile, permission mode, environment hash, cwd, MCP servers, system prompt, adapter version, and AO private protocol version?
8. **Accepted provider versions:** AO's minimum Kimchi and Cursor versions may not match the inspected current sources; Droid is closed source. Each rollout gate needs an exact tested version floor or a runtime behavioral probe.
9. **Pi platform support:** The inspected adapter uses Unix sockets. Confirm supported AO desktop platforms and whether a first-party Windows transport is planned before advertising parity.
10. **ACP v2:** Draft v2 introduces accepted prompts and explicit running/requires-action/idle state, but official guidance keeps v1 support mandatory ([migration draft](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v2/migration.mdx#L6-L30), [v2 lifecycle](https://github.com/agentclientprotocol/agent-client-protocol/blob/01b9d6e9c094d31cdea6d88768a9dd31b089ccef/docs/protocol/v2/migration.mdx#L226-L315)). The first implementation should target the harder v1 lifecycle and treat v2 as an optimization after negotiation.
11. **Branch activation:** Resolved by fencing the idle source, terminating its host, and then opening the replacement. Failure restores the source through native resume; AO never launches a concurrent direct provider beside an attached host.
12. **Process logs and diagnostics:** Decide which stderr/log state is journaled or retained across daemon attachment without leaking credentials or making the descriptor a second provider state store.

## Final recommendation

Do not implement ACP persistence as a flag on the current raw bridge. First evolve the persistent host into an acknowledged, stateful ACP connection owner with idempotent commands and stable interactions; then adapt the shared ACP conversation once and gate providers individually with the fake crash suite plus real E2E evidence. This is the smallest design that can truthfully claim that an active ACP Chat turn, including blocked approvals and cancellation, survives AO daemon replacement without duplicate work or silent loss.


## Local implementation evidence

The implementation following this research added an explicit ACP host profile
rather than treating ACP as raw byte forwarding. Credential-free tests exercise
the host/provider process boundary, request-ID remapping and cancellation,
initialize/session snapshot adoption, active-prompt replay, the commit-before-ACK
crash window, stable permission identity and original-response correlation,
bounded mode-0600 journal behavior, and explicit process-tree termination. The
host and shared ACP driver also pass their race-enabled suites and Windows source
cross-compilation.

The real daemon test uses a shell-created marker as the acceptance barrier, kills
the daemon with `SIGKILL` while the tool is sleeping, launches a replacement
daemon, and asserts the original turn completes under the same detached host PID.
It passed locally against the pinned Claude ACP bridge plus the installed Claude
account, native OpenCode 1.18.15, and Cursor 2026.08.11. Droid reached its real
ACP/model boundary but the local account returned HTTP 402 because it has no
active subscription, so no credentialed Droid survival claim is made. Kimi,
Kimchi, Pi's separate `pi-acp` adapter, and OMP were not installed in this
environment; their bindings share the exercised host/driver implementation and
remain covered by credential-free provider-process tests, but their opt-in live
gates still need to run in an authenticated provider matrix.
