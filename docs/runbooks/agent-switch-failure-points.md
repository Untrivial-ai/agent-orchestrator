# Agent-switch failure-point runbook

Status: the implementation is staged behind
`agentSwitchFailureProductionEnabled = false`. Do not enable the production
stream until every release gate in this runbook and `docs/telemetry.md` is
approved.

This runbook is keyed by the stable `failure_point` field. The event is a
failure-only diagnostic, not proof that a runtime is dead. Always establish the
fenced source generation, target generation, and native identity before taking
an ownership-changing action. An `unknown` probe result retains the recovery
gate; it never authorizes source restoration or a second target.

## Reading the table

Durable-phase codes:

- `A`: `preparing_handoff` or the winning `failed` transition.
- `P`: `preparing_handoff`, `stopping_source`, or `failed`.
- `S`: `stopping_source`, `source_stopped`, or `failed`.
- `C`: `preparing_handoff`, `source_stopped`, `starting_target`, or `failed`.
- `T`: `starting_target`, `target_ready`, or `failed`.
- `D`: `target_ready`, `delivering_context`, or `failed`.
- `X`: `stopping_source`, `source_stopped`, `starting_target`, or `failed`.
- `R`: any nonterminal switch phase, or `failed`, while reconciling.
- `P!`: any nonterminal switch phase, or `failed`, for a process panic.
- `M`: `completed` or `failed`; only post-terminal maintenance is involved.
- `V`: any switch phase, `completed`, `failed`, or `not_applicable`; the
  frontend observation never changes durable ownership.
- `N`: `not_applicable`; this is a daemon-scoped incident.
- `L`: any phase, but the point is local-only and must never be serialized.

Ownership/source-restoration codes:

- `SRC`: the source still owns the session. Preserve it; restoration is neither
  necessary nor authorized from this observation alone.
- `STOP?`: source stop is being established. Restore only after the exact source
  generation is proven stopped and any possible target is proven absent or
  cleaned. Unknown evidence retains the gate.
- `PRE-TGT`: the source is stopped and no target owner is yet committed. Restore
  only after exact target absence or successful fenced cleanup.
- `TGT?`: target creation/activation may have taken effect. Read back durable
  ownership and probe the exact generation; never restore on ambiguity.
- `TGT`: the target owns the session. Source restoration is forbidden.
- `REC`: apply the state-specific recovery matrix below; never infer ownership
  from a generic runtime/display probe.
- `NONE`: no product ownership changes are allowed by this incident.

Operator-action codes:

- `RETRY`: leave the current source running, correct the stated precondition,
  and retry the switch.
- `HOLD`: do not start another target or force a restore. Keep the recovery gate,
  restart AO if needed, and use the visible recovery action after fenced facts
  are available.
- `TARGET`: continue in the target session; do not revive the source. If context
  delivery is unconfirmed, inspect the target before manually repeating work.
- `CLEAN`: retry only the fenced cleanup/recovery action; never kill a process by
  an unfenced PID or handle.
- `REFRESH`: keep the durable row unchanged; refocus/reopen the surface or retry
  connectivity/query presentation.
- `LOCAL`: inspect local logs/database health. Do not create a recursive remote
  event.

Release-blocker codes:

- `B0`: possible dual owner, an unidentifiable live target, activation ownership
  unknown, a controller published without durable ownership, a gate without a
  saga, or cleanup that may leave an unidentifiable live target. Stop rollout.
- `B1`: no provable live owner, restore/delivery unconfirmed, startup recovery
  blocked, post-stop panic, or unrecoverable target/context. Stop rollout when
  reproducible. Once enabled, notify on a new issue and escalate at five matching
  events in ten minutes; only `B0` pages immediately.
- `B2`: safe pre-stop or proven-harmless warning. It is not individually a
  release blocker unless the expected fencing/suppression invariant is broken or
  volume indicates a regression.
- `BP`: any raw identifier/path/error/panic value leaving the closed event schema,
  reporting while disabled, renderer-owned network sending, stale-generation
  delivery, or payload surviving acknowledged opt-out. Stop rollout.
- `BO`: a local observability invariant or recursive outbox event. Keep reporting
  disabled until fixed.

Log/test selectors used below are stable starting points. A row without a
dedicated branch log is diagnosed by its persisted `failure_point` and the
referenced deterministic test:

- `SM-L`: local log message `agent switch failed`; tests in
  `backend/internal/session_manager/agent_switch_faults_test.go` and
  `agent_switching_test.go`.
- `SM-C`: ordinary Chat worker failures use `agent switch failed`; the narrower
  persistence fault uses `Chat agent switch: failed to persist terminal failure`.
  Tests live in `agent_switch_faults_test.go`, `agent_switching_test.go`, and
  `agent_switching_chat_integration_test.go`.
- `SM-R`: local log message `agent switch recovery failed`; recovery matrix in
  `agent_switching_test.go`.
- `SM-P`: local messages `agent switch failed` or
  `agent switch recovery panicked`; panic/cardinality cases in
  `agent_switch_faults_test.go` and `agent_switching_test.go`.
- `SM-M`: local message `agent switch: handoff artifact cleanup failed`;
  maintenance cases in `agent_switch_faults_test.go`.
- `DA-S`: local message `agent switch worker shutdown`; daemon coverage in
  `backend/internal/daemon/wiring_test.go`.
- `FE-V`: local diagnostic
  `agent switch visibility diagnostic: visibility_delivery_failed`; the
  main-owned FSM is covered in
  `frontend/src/main/agent-switch-observability.test.ts` and the renderer
  boundary tests named in the visibility section below.
- `OBS-D`: local messages `claim agent switch failure`,
  `begin agent switch failure attempt`, or
  `settle agent switch failure delivery`; tests in
  `backend/internal/observe/agentswitch/dispatcher_test.go`.
- `OBS-I`: local message `agent switch telemetry local invariant`; taxonomy and
  store tests in `backend/internal/domain/agent_switch_observability_test.go` and
  `backend/internal/storage/sqlite/store/agent_switch_failure_store_test.go`.

## Admission

| Failure point | Phase | Ownership / restore | Log / test | User action | Blocker |
| --- | --- | --- | --- | --- | --- |
| <a id="agent-switch-admission_saga_create"></a>`admission_saga_create` | A | SRC | SM-L | RETRY | B0 if a gate/session mutation exists without the saga; otherwise B2 |
| <a id="agent-switch-admission_commit_readback"></a>`admission_commit_readback` | A | SRC | SM-L | HOLD until read-back proves commit or absence | B0 if admission outcome stays ambiguous |
| <a id="agent-switch-admission_chat_handoff_arm"></a>`admission_chat_handoff_arm` | A | SRC | SM-C | RETRY only after the pending boundary is known absent | B0 if the boundary may exist without its saga |
| <a id="agent-switch-worker_start_refused"></a>`worker_start_refused` | A | SRC | SM-L | RETRY after shutdown/resource pressure clears | B2 unless the durable admitted saga is not settled |

## Preflight and handoff

| Failure point | Phase | Ownership / restore | Log / test | User action | Blocker |
| --- | --- | --- | --- | --- | --- |
| <a id="agent-switch-source_native_preserve"></a>`source_native_preserve` | P | SRC | SM-L | RETRY after native identity is available | B2; B0 if switching proceeds without fenced source identity |
| <a id="agent-switch-target_preflight"></a>`target_preflight` | P | SRC | SM-L | RETRY after binary/auth/config correction | B2 |
| <a id="agent-switch-target_resume_lookup"></a>`target_resume_lookup` | P | SRC | SM-L | RETRY; a failed optional lookup must not imply a target | B2; B0 if an ambiguous owner is treated as absent |
| <a id="agent-switch-handoff_directory_prepare"></a>`handoff_directory_prepare` | P | SRC | SM-L | RETRY or use the documented safe fallback | B2 |
| <a id="agent-switch-handoff_collection"></a>`handoff_collection` | P | SRC | SM-L | RETRY; preserve the source and its transcript | B2 |
| <a id="agent-switch-handoff_settlement"></a>`handoff_settlement` | P | SRC | SM-L | HOLD until durable settlement is read back | B1 if context ownership is ambiguous |
| <a id="agent-switch-decision_input_close"></a>`decision_input_close` | P | SRC | SM-L | RETRY after input can be quiesced | B2; B0 if target launch continues while source input is live |
| <a id="agent-switch-source_handoff_interrupt"></a>`source_handoff_interrupt` | P | SRC | SM-L | RETRY only after the exact source remains active | B2; B1 if source availability is unknown |
| <a id="agent-switch-chat_source_quiesce"></a>`chat_source_quiesce` | P | SRC | SM-C | RETRY after controller quiescence is proven | B0 if two controller generations may accept input |
| <a id="agent-switch-target_launch_gate_prepare"></a>`target_launch_gate_prepare` | P | SRC | SM-L | HOLD; do not create a target without the durable gate | B0 |
| <a id="agent-switch-stopping_source_commit"></a>`stopping_source_commit` | P | STOP? | SM-L | HOLD and read back the transition | B0 if source stopping begins without durable ownership state |

## Source stop

| Failure point | Phase | Ownership / restore | Log / test | User action | Blocker |
| --- | --- | --- | --- | --- | --- |
| <a id="agent-switch-source_runtime_destroy"></a>`source_runtime_destroy` | S | STOP? | SM-L | HOLD; probe the exact source generation | B1, or B0 if a later target is allowed on unknown evidence |
| <a id="agent-switch-source_runtime_probe"></a>`source_runtime_probe` | S | STOP? | SM-L | HOLD; retain `source_stop_unconfirmed` | B1 |
| <a id="agent-switch-source_controller_stop"></a>`source_controller_stop` | S | STOP? | SM-C | HOLD; fence the controller generation | B1; B0 if a target controller is published concurrently |
| <a id="agent-switch-source_controller_drain"></a>`source_controller_drain` | S | STOP? | SM-C | HOLD until accepted work is drained or ownership is recovered | B1 |
| <a id="agent-switch-source_stop_commit"></a>`source_stop_commit` | S | STOP? | SM-L | HOLD and read back session plus switch ownership | B0 if side effects occurred without the durable commit |
| <a id="agent-switch-source_stop_readback"></a>`source_stop_readback` | S | STOP? | SM-L | HOLD; do not infer success from timeout/response loss | B0 while ownership is ambiguous |

## Context and artifact construction

| Failure point | Phase | Ownership / restore | Log / test | User action | Blocker |
| --- | --- | --- | --- | --- | --- |
| <a id="agent-switch-source_metadata_refresh"></a>`source_metadata_refresh` | C | SRC before stop; PRE-TGT after stop | SM-L | RETRY before stop, otherwise CLEAN | B2 before stop; B1 after stop |
| <a id="agent-switch-semantic_artifact_verify"></a>`semantic_artifact_verify` | C | SRC before stop; PRE-TGT after stop | SM-L | RETRY/CLEAN according to phase | B1 if no valid continuation can be recovered |
| <a id="agent-switch-source_transcript_capture"></a>`source_transcript_capture` | C | SRC before stop; PRE-TGT after stop | SM-L | RETRY/CLEAN according to phase | B2 before stop; B1 after stop |
| <a id="agent-switch-continuation_build"></a>`continuation_build` | C | SRC before stop; PRE-TGT after stop | SM-L | RETRY/CLEAN according to phase | B1 after source stop |
| <a id="agent-switch-final_artifact_publish"></a>`final_artifact_publish` | C | SRC in `preparing_handoff`; PRE-TGT after stop | SM-L | RETRY before stop; otherwise CLEAN, then restore only after exact target absence | B1 after stop |
| <a id="agent-switch-final_artifact_verify"></a>`final_artifact_verify` | C | SRC in `preparing_handoff`; PRE-TGT after stop | SM-L | RETRY before stop; otherwise CLEAN and do not launch from unverified context | B1 after stop |
| <a id="agent-switch-final_artifact_commit"></a>`final_artifact_commit` | C | SRC in `preparing_handoff`; PRE-TGT after stop | SM-L | HOLD and read back artifact plus switch revision | B1 after stop |
| <a id="agent-switch-target_prompt_prepare"></a>`target_prompt_prepare` | C | SRC in `preparing_handoff`; PRE-TGT after stop | SM-L | RETRY before stop; otherwise CLEAN before creating target | B1 after stop |
| <a id="agent-switch-target_workspace_prepare"></a>`target_workspace_prepare` | C | SRC in `preparing_handoff`; PRE-TGT after stop | SM-L | RETRY before stop; otherwise CLEAN the fenced workspace, then recover source | B1; B0 if cleanup may contain a live target |

## TUI target start

| Failure point | Phase | Ownership / restore | Log / test | User action | Blocker |
| --- | --- | --- | --- | --- | --- |
| <a id="agent-switch-target_native_prepare"></a>`target_native_prepare` | T | PRE-TGT | SM-L | CLEAN; restore only after exact absence | B1 |
| <a id="agent-switch-target_native_commit"></a>`target_native_commit` | T | TGT? | SM-L | HOLD and read back native identity | B0 while commit outcome is ambiguous |
| <a id="agent-switch-target_runtime_create"></a>`target_runtime_create` | T | TGT? | SM-L | HOLD; exact alive/dead/unknown probe decides recovery | B0 if possibly live but unidentifiable; otherwise B1 |
| <a id="agent-switch-target_handle_commit"></a>`target_handle_commit` | T | TGT? | SM-L | HOLD and read back handle plus generation | B0 |
| <a id="agent-switch-target_generation_probe"></a>`target_generation_probe` | T | TGT? | SM-L | HOLD; unknown retains the gate | B0 if unknown is treated as dead |
| <a id="agent-switch-target_native_identity_wait"></a>`target_native_identity_wait` | T | TGT? | SM-L | HOLD; never adopt or kill without matching identity | B0 |
| <a id="agent-switch-target_activation_commit"></a>`target_activation_commit` | T | TGT? | SM-L | HOLD and prove the complete committed-target tuple | B0 |
| <a id="agent-switch-target_activation_readback"></a>`target_activation_readback` | T | TGT? | SM-L | HOLD; target is TGT only after complete tuple proof | B0 |

## Chat target start

| Failure point | Phase | Ownership / restore | Log / test | User action | Blocker |
| --- | --- | --- | --- | --- | --- |
| <a id="agent-switch-chat_provider_start"></a>`chat_provider_start` | T | TGT? | SM-C | HOLD; reconcile provider result with durable generation | B0 on ambiguous provider ownership; otherwise B1 |
| <a id="agent-switch-chat_provider_resume"></a>`chat_provider_resume` | T | TGT? | SM-C | HOLD; do not start a second conversation branch | B0 |
| <a id="agent-switch-chat_native_identity_commit"></a>`chat_native_identity_commit` | T | TGT? | SM-C | HOLD and read back identity | B0 |
| <a id="agent-switch-chat_provider_boundary_commit"></a>`chat_provider_boundary_commit` | T | TGT? | SM-C | HOLD; preserve the deterministic provider boundary | B0 |
| <a id="agent-switch-chat_target_activation_commit"></a>`chat_target_activation_commit` | T | TGT? | SM-C | HOLD and prove session, conversation, switch, and generation atomically | B0 |
| <a id="agent-switch-chat_target_activation_readback"></a>`chat_target_activation_readback` | T | TGT? | SM-C | HOLD; restore is forbidden if the target tuple committed | B0 |
| <a id="agent-switch-chat_controller_publish"></a>`chat_controller_publish` | T | TGT | SM-C | TARGET; never restore the source after publication | B0 if publish occurred without durable target ownership |

## Delivery and completion

| Failure point | Phase | Ownership / restore | Log / test | User action | Blocker |
| --- | --- | --- | --- | --- | --- |
| <a id="agent-switch-delivery_open_commit"></a>`delivery_open_commit` | D | TGT | SM-L | TARGET; inspect target before repeating context | B1 |
| <a id="agent-switch-tui_target_hook_wait"></a>`tui_target_hook_wait` | D | TGT | SM-L | TARGET; wait/recover without replaying unconfirmed argv context | B1 |
| <a id="agent-switch-tui_target_ack_commit"></a>`tui_target_ack_commit` | D | TGT | SM-L | TARGET; read back ack before declaring failure | B1 |
| <a id="agent-switch-chat_continuation_relay"></a>`chat_continuation_relay` | D | TGT | SM-C | TARGET; do not blindly replay after an ambiguous relay | B1 |
| <a id="agent-switch-chat_target_ack_commit"></a>`chat_target_ack_commit` | D | TGT | SM-C | TARGET; read back deterministic message acknowledgement | B1 |
| <a id="agent-switch-completion_commit"></a>`completion_commit` | D | TGT | SM-L | TARGET; read back completion, suppress if it won | B1 only while outcome remains ambiguous |

## Compensation and recovery

| Failure point | Phase | Ownership / restore | Log / test | User action | Blocker |
| --- | --- | --- | --- | --- | --- |
| <a id="agent-switch-target_runtime_cleanup"></a>`target_runtime_cleanup` | X | TGT? | SM-L | CLEAN; restore only after exact target absence | B0 if a possible live target remains |
| <a id="agent-switch-target_workspace_cleanup"></a>`target_workspace_cleanup` | X | TGT? | SM-L | CLEAN; preserve evidence and the gate | B0 if workspace cleanup can orphan a live target; otherwise B1 |
| <a id="agent-switch-source_runtime_restore"></a>`source_runtime_restore` | X | PRE-TGT only after exact target absence | SM-L | HOLD and retry fenced restore | B1 |
| <a id="agent-switch-source_controller_restore"></a>`source_controller_restore` | X | PRE-TGT only after exact target absence | SM-C | HOLD and retry the exact controller generation | B1; B0 if both generations may own input |
| <a id="agent-switch-recovery_session_load"></a>`recovery_session_load` | R | REC | SM-R | HOLD; repair/read durable state before side effects | B1 |
| <a id="agent-switch-recovery_runtime_probe"></a>`recovery_runtime_probe` | R | REC | SM-R | HOLD; unknown retains the gate | B0 if unknown authorizes cleanup/restore; otherwise B1 |
| <a id="agent-switch-recovery_native_identity"></a>`recovery_native_identity` | R | REC | SM-R | HOLD; require exact generation and start identity | B0 |
| <a id="agent-switch-recovery_artifact_verify"></a>`recovery_artifact_verify` | R | REC | SM-R | HOLD; never resume from unverified context | B1 |
| <a id="agent-switch-recovery_activation"></a>`recovery_activation` | R | TGT? | SM-R | HOLD and prove the complete target activation tuple | B0 |
| <a id="agent-switch-recovery_settlement"></a>`recovery_settlement` | R | REC | SM-R | HOLD and read back the winning semantic CAS | B1; B0 if a gate is released on ambiguity |
| <a id="agent-switch-recovery_existing_marker"></a>`recovery_existing_marker` | R | REC | SM-R | HOLD; this is current-state opt-in enrollment, not historical replay | BP if timestamp/consent generation predates opt-in; otherwise B1 |

Recovery phase rules:

- `preparing_handoff`: source owns; close with `daemon_restart_pre_stop`.
- `stopping_source`: exact source alive preserves source; exact dead permits
  compensation; unknown retains `source_stop_unconfirmed` and the gate.
- `source_stopped`: clean any target preparation, restore source once, then
  terminalize; uncertain cleanup/restore retains `source_restore_unconfirmed`.
- `starting_target`: exact dead permits cleanup/restore; exact alive plus matching
  identity may be adopted by fenced activation; unknown retains the gate.
- `target_ready`: never replay unconfirmed TUI argv context; Chat may relay the
  one deterministic, not-yet-attempted message.
- `delivering_context`: durable acknowledgement completes; otherwise never
  replay and settle `delivery_unconfirmed`.
- terminal states: no switch side effects and no second semantic event.

## Process, maintenance, visibility, and local-only points

| Failure point | Phase | Ownership / restore | Log / test | User action | Blocker |
| --- | --- | --- | --- | --- | --- |
| <a id="agent-switch-live_worker_panic"></a>`live_worker_panic` | P! | REC | SM-P | HOLD; allow one bounded reconciliation | B1 after source stop; BP if panic value/raw stack data escapes |
| <a id="agent-switch-recovery_worker_panic"></a>`recovery_worker_panic` | P! | REC | SM-P | HOLD; keep the gate and retry recovery after diagnosis | B1; BP if one panic plus semantic settlement becomes two incidents |
| <a id="agent-switch-shutdown_worker_timeout"></a>`shutdown_worker_timeout` | N | NONE | DA-S | HOLD; let the aggregate worker incident settle without per-switch copies | B1; BP if shutdown bypasses consent or duplicates per switch |
| <a id="agent-switch-terminal_artifact_cleanup"></a>`terminal_artifact_cleanup` | M | NONE | SM-M | CLEAN the artifact only; do not change terminal classification | B2 unless cleanup can leave a live target, then B0 |
| <a id="agent-switch-visibility_transport"></a>`visibility_transport` | V | NONE | FE-V; `event-transport.test.ts` | REFRESH after network/daemon recovery | B2; BP if renderer sends or local IDs enter bytes |
| <a id="agent-switch-visibility_query"></a>`visibility_query` | V | NONE | FE-V; `useWorkspaceQuery.test.tsx` | REFRESH after a successful typed query | B2; BP if it reports while transport owns the outage |
| <a id="agent-switch-visibility_presentation"></a>`visibility_presentation` | V | NONE | FE-V; `agent-switch-visibility.test.ts` and `useAgentSwitchVisibility.test.tsx` | REFRESH/refocus the required recovery or failure surface | B2; BP if token/switch ID/revision/route leaves the device |
| <a id="agent-switch-outbox_delivery"></a>`outbox_delivery` | L | NONE | OBS-D | LOCAL; inspect policy, lease, network, throttle, and TTL | BO if recursively enqueued or remotely serialized |
| <a id="agent-switch-classification_unknown"></a>`classification_unknown` | L | NONE | OBS-I | LOCAL; fix the invalid classifier input while core saga behavior continues | BO |

## Universal suppression and escalation checks

Do not treat the presence of an error log alone as a reportable incident. The
following must create no new receipt, outbox row, or remote event: matching
idempotent requests, expected validation/conflicts, a repeated marker, a
terminal row seen again, response loss followed by a winning read-back, an
effect error followed by exact proof of the intended side effect, stale or
duplicate lifecycle hooks, a timeout CAS that loses to acknowledgement,
startup wrappers around an already-owned incident, ordinary shutdown
cancellation, successful cleanup retry, normal failed-row rendering, and every
successful switch.

Before enabling production, verify:

1. Every supported runtime returns exact `alive`, exact `dead`, or deliberate
   `unknown` for the fenced generation; no registry/probe error collapses to
   absence.
2. Chat activation atomically commits session, conversation, switch, native
   identity, provider boundary, and controller generation from the expected
   source generation.
3. The same failure is owned exactly once across saga, HTTP, renderer, startup,
   panic, and acknowledgement races.
4. Disabling consent closes/drains main, closes/drains the daemon gate, durably
   writes off, makes the daemon reread/mirror/purge every payload status, purges
   desktop cache/renderer queues, and only then acknowledges. An unavailable or
   incomplete cleanup stays `cleanup_pending`; later opt-in never exports
   terminal history.
5. Windows fails closed and rejects enable acknowledgement until a tested native
   write-through replacement meets the policy-file durability contract.
6. Sentry IP handling/scrubbing, data residency, retention, and disabled
   automatic contexts have dated privacy approval.
7. The production flag stays false until all preceding checks pass.
