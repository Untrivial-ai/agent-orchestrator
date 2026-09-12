-- name: GetAgentSwitchFailurePolicy :one
SELECT singleton, enabled, consent_generation, destination_fingerprint, updated_at
FROM agent_switch_failure_policy WHERE singleton = 1;

-- name: ForceDisableAgentSwitchFailurePolicy :execrows
UPDATE agent_switch_failure_policy SET enabled = 0, updated_at = ? WHERE singleton = 1;

-- name: ApplyAgentSwitchFailurePolicy :execrows
UPDATE agent_switch_failure_policy
SET enabled = ?, consent_generation = ?, destination_fingerprint = ?, updated_at = ?
WHERE singleton = 1;

-- name: InsertAgentSwitchFailureReceipt :execrows
INSERT INTO agent_switch_failure_receipts (
    dedupe_key, switch_id, report_kind, durable_state_fingerprint, recorded_at, retain_until
)
SELECT sqlc.arg(dedupe_key), sqlc.narg(switch_id), sqlc.arg(report_kind),
       sqlc.arg(durable_state_fingerprint), sqlc.arg(recorded_at), sqlc.narg(retain_until)
FROM agent_switch_failure_policy
WHERE singleton = 1 AND enabled = 1
  AND consent_generation = sqlc.arg(consent_generation)
  AND destination_fingerprint = sqlc.arg(destination_fingerprint)
ON CONFLICT DO NOTHING;

-- name: InsertAgentSwitchFailureReceiptForCurrentSwitch :execrows
INSERT INTO agent_switch_failure_receipts (
    dedupe_key, switch_id, report_kind, durable_state_fingerprint, recorded_at, retain_until
)
SELECT sqlc.arg(dedupe_key), s.id, sqlc.arg(report_kind),
       sqlc.arg(durable_state_fingerprint), sqlc.arg(recorded_at), sqlc.narg(retain_until)
FROM agent_switches s
JOIN agent_switch_failure_policy p ON p.singleton = 1
WHERE s.id = sqlc.arg(switch_id) AND s.state = sqlc.arg(expected_state)
  AND s.error_code = sqlc.arg(expected_error_code)
  AND s.failure_point = sqlc.arg(expected_failure_point)
  AND s.updated_at = sqlc.arg(expected_updated_at)
  AND p.enabled = 1 AND p.consent_generation = sqlc.arg(consent_generation)
  AND p.destination_fingerprint = sqlc.arg(destination_fingerprint)
ON CONFLICT DO NOTHING;

-- name: InsertAgentSwitchFailurePayload :execrows
INSERT INTO agent_switch_failure_outbox (
    id, schema_version, envelope_encoding_version, dedupe_key, destination_fingerprint,
    switch_id, report_kind, scope, failure_point, classifier_callsite, phase,
    error_code, fault_code, execution, execution_attempt_id, mode, from_harness,
    target_harness, target_start_mode, runtime_backend, call_outcome, ownership,
    compensation, user_impact, source_stop_confirmed, target_owner_committed,
    gate_retained, requested_at, occurred_at, sanitized_stack, stack_fingerprint,
    canonical_event_json, expires_at, available_at
)
SELECT sqlc.arg(id), sqlc.arg(schema_version), sqlc.arg(envelope_encoding_version),
       sqlc.arg(dedupe_key), sqlc.arg(destination_fingerprint), sqlc.narg(switch_id),
       sqlc.arg(report_kind), sqlc.arg(scope), sqlc.arg(failure_point),
       sqlc.arg(classifier_callsite), sqlc.arg(phase), sqlc.arg(error_code),
       sqlc.arg(fault_code), sqlc.arg(execution), sqlc.arg(execution_attempt_id),
       sqlc.arg(mode), sqlc.arg(from_harness), sqlc.arg(target_harness),
       sqlc.arg(target_start_mode), sqlc.arg(runtime_backend), sqlc.arg(call_outcome),
       sqlc.arg(ownership), sqlc.arg(compensation), sqlc.arg(user_impact),
       sqlc.arg(source_stop_confirmed), sqlc.arg(target_owner_committed),
       sqlc.arg(gate_retained), sqlc.narg(requested_at), sqlc.arg(occurred_at),
       sqlc.arg(sanitized_stack), sqlc.arg(stack_fingerprint),
       sqlc.arg(canonical_event_json), sqlc.arg(expires_at), sqlc.arg(available_at)
FROM agent_switch_failure_policy p
WHERE p.singleton = 1 AND p.enabled = 1
  AND p.consent_generation = sqlc.arg(consent_generation)
  AND p.destination_fingerprint = sqlc.arg(destination_fingerprint)
  AND EXISTS (SELECT 1 FROM agent_switch_failure_receipts r WHERE r.dedupe_key = sqlc.arg(dedupe_key))
ON CONFLICT DO NOTHING;

-- name: PurgeAgentSwitchFailurePayloads :execrows
DELETE FROM agent_switch_failure_outbox;

-- name: ListCurrentAgentSwitchRecoveryMarkers :many
SELECT a.id, a.session_id, a.requested_at, a.updated_at, a.state, a.error_code,
       a.failure_point, a.from_harness, a.target_harness, a.target_start_mode,
       s.session_mode
FROM agent_switches a
JOIN sessions s ON s.id = a.session_id
WHERE a.state NOT IN ('completed', 'failed')
  AND a.error_code IN ('source_stop_unconfirmed', 'source_restore_unconfirmed', 'target_start_unconfirmed');

-- name: QuarantineAgentSwitchFailureDestinationMismatch :execrows
UPDATE agent_switch_failure_outbox
SET discarded_at = sqlc.arg(now), lease_token = NULL, lease_consent_generation = NULL,
    lease_delivery_epoch = NULL, lease_expires_at = NULL,
    last_delivery_error_class = 'unauthorized'
WHERE delivered_at IS NULL AND discarded_at IS NULL
  AND agent_switch_failure_outbox.destination_fingerprint <> sqlc.arg(destination_fingerprint)
  AND EXISTS (
      SELECT 1 FROM agent_switch_failure_policy p
      WHERE p.singleton = 1 AND p.enabled = 1
        AND p.consent_generation = sqlc.arg(consent_generation)
        AND p.destination_fingerprint = sqlc.arg(destination_fingerprint)
  );

-- name: SelectClaimableAgentSwitchFailure :one
SELECT o.id, o.envelope_encoding_version, o.canonical_event_json,
       o.destination_fingerprint, o.expires_at, o.attempt_count
FROM agent_switch_failure_outbox o
JOIN agent_switch_failure_policy p ON p.singleton = 1
LEFT JOIN agent_switch_failure_delivery_state d ON d.destination_fingerprint = o.destination_fingerprint
WHERE p.enabled = 1 AND p.consent_generation = sqlc.arg(consent_generation)
  AND p.destination_fingerprint = sqlc.arg(destination_fingerprint)
  AND o.destination_fingerprint = sqlc.arg(destination_fingerprint)
  AND o.delivered_at IS NULL AND o.discarded_at IS NULL
  AND o.available_at <= sqlc.arg(now) AND o.expires_at > sqlc.arg(now)
  AND (o.lease_token IS NULL OR o.lease_expires_at <= sqlc.arg(now))
  AND (d.error_not_before IS NULL OR d.error_not_before <= sqlc.arg(now))
  AND (d.all_not_before IS NULL OR d.all_not_before <= sqlc.arg(now))
ORDER BY o.available_at, o.occurred_at, o.id LIMIT 1;

-- name: LeaseAgentSwitchFailure :execrows
UPDATE agent_switch_failure_outbox
SET lease_token = sqlc.arg(lease_token),
    lease_consent_generation = sqlc.arg(consent_generation),
    lease_delivery_epoch = sqlc.arg(delivery_epoch),
    lease_expires_at = sqlc.arg(lease_expires_at)
WHERE id = sqlc.arg(id)
  AND agent_switch_failure_outbox.destination_fingerprint = sqlc.arg(destination_fingerprint)
  AND delivered_at IS NULL AND discarded_at IS NULL AND expires_at > sqlc.arg(now)
  AND (lease_token IS NULL OR lease_expires_at <= sqlc.arg(now))
  AND EXISTS (
      SELECT 1 FROM agent_switch_failure_policy p
      WHERE p.singleton = 1 AND p.enabled = 1
        AND p.consent_generation = sqlc.arg(consent_generation)
        AND p.destination_fingerprint = sqlc.arg(destination_fingerprint)
  )
  AND NOT EXISTS (
      SELECT 1 FROM agent_switch_failure_delivery_state d
      WHERE d.destination_fingerprint = sqlc.arg(destination_fingerprint)
        AND ((d.error_not_before IS NOT NULL AND d.error_not_before > sqlc.arg(now))
          OR (d.all_not_before IS NOT NULL AND d.all_not_before > sqlc.arg(now)))
  );

-- name: BeginAgentSwitchFailureAttempt :execrows
UPDATE agent_switch_failure_outbox
SET attempt_count = attempt_count + 1, last_attempt_at = sqlc.arg(now)
WHERE agent_switch_failure_outbox.id = sqlc.arg(id)
  AND agent_switch_failure_outbox.lease_token = sqlc.arg(lease_token)
  AND agent_switch_failure_outbox.lease_consent_generation = sqlc.arg(consent_generation)
  AND agent_switch_failure_outbox.lease_delivery_epoch = sqlc.arg(delivery_epoch)
  AND agent_switch_failure_outbox.destination_fingerprint = sqlc.arg(destination_fingerprint)
  AND agent_switch_failure_outbox.lease_expires_at IS NOT NULL
  AND agent_switch_failure_outbox.lease_expires_at > sqlc.arg(now)
  AND agent_switch_failure_outbox.expires_at > sqlc.arg(now)
  AND agent_switch_failure_outbox.delivered_at IS NULL
  AND agent_switch_failure_outbox.discarded_at IS NULL
  AND EXISTS (
      SELECT 1 FROM agent_switch_failure_policy p
      WHERE p.singleton = 1 AND p.enabled = 1
        AND p.consent_generation = sqlc.arg(consent_generation)
        AND p.destination_fingerprint = sqlc.arg(destination_fingerprint)
  )
  AND NOT EXISTS (
      SELECT 1 FROM agent_switch_failure_delivery_state d
      WHERE d.destination_fingerprint = sqlc.arg(destination_fingerprint)
        AND ((d.error_not_before IS NOT NULL AND d.error_not_before > sqlc.arg(now))
          OR (d.all_not_before IS NOT NULL AND d.all_not_before > sqlc.arg(now)))
  );

-- name: GetLeasedAgentSwitchFailureExpiresAt :one
SELECT expires_at
FROM agent_switch_failure_outbox
WHERE id = sqlc.arg(id) AND lease_token = sqlc.arg(lease_token)
  AND lease_consent_generation = sqlc.arg(consent_generation)
  AND lease_delivery_epoch = sqlc.arg(delivery_epoch)
  AND destination_fingerprint = sqlc.arg(destination_fingerprint)
  AND delivered_at IS NULL AND discarded_at IS NULL;

-- name: MarkAgentSwitchFailureDelivered :execrows
UPDATE agent_switch_failure_outbox
SET delivered_at = sqlc.arg(settled_at), lease_token = NULL,
    lease_consent_generation = NULL, lease_delivery_epoch = NULL, lease_expires_at = NULL,
    last_delivery_error_class = ''
WHERE id = sqlc.arg(id) AND lease_token = sqlc.arg(lease_token)
  AND lease_consent_generation = sqlc.arg(consent_generation)
  AND lease_delivery_epoch = sqlc.arg(delivery_epoch)
  AND destination_fingerprint = sqlc.arg(destination_fingerprint)
  AND delivered_at IS NULL AND discarded_at IS NULL;

-- name: RetryAgentSwitchFailure :execrows
UPDATE agent_switch_failure_outbox
SET available_at = sqlc.arg(available_at), lease_token = NULL,
    lease_consent_generation = NULL, lease_delivery_epoch = NULL, lease_expires_at = NULL,
    last_delivery_error_class = sqlc.arg(error_class)
WHERE id = sqlc.arg(id) AND lease_token = sqlc.arg(lease_token)
  AND lease_consent_generation = sqlc.arg(consent_generation)
  AND lease_delivery_epoch = sqlc.arg(delivery_epoch)
  AND destination_fingerprint = sqlc.arg(destination_fingerprint)
  AND delivered_at IS NULL AND discarded_at IS NULL;

-- name: DiscardAgentSwitchFailure :execrows
UPDATE agent_switch_failure_outbox
SET discarded_at = sqlc.arg(settled_at), lease_token = NULL,
    lease_consent_generation = NULL, lease_delivery_epoch = NULL, lease_expires_at = NULL,
    last_delivery_error_class = sqlc.arg(error_class)
WHERE id = sqlc.arg(id) AND lease_token = sqlc.arg(lease_token)
  AND lease_consent_generation = sqlc.arg(consent_generation)
  AND lease_delivery_epoch = sqlc.arg(delivery_epoch)
  AND destination_fingerprint = sqlc.arg(destination_fingerprint)
  AND delivered_at IS NULL AND discarded_at IS NULL;

-- name: ReleaseAgentSwitchFailureLease :execrows
UPDATE agent_switch_failure_outbox
SET lease_token = NULL, lease_consent_generation = NULL,
    lease_delivery_epoch = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND lease_token = sqlc.arg(lease_token)
  AND lease_consent_generation = sqlc.arg(consent_generation)
  AND lease_delivery_epoch = sqlc.arg(delivery_epoch)
  AND destination_fingerprint = sqlc.arg(destination_fingerprint)
  AND delivered_at IS NULL AND discarded_at IS NULL;

-- name: UpsertAgentSwitchFailureThrottle :exec
INSERT INTO agent_switch_failure_delivery_state (
    destination_fingerprint, error_not_before, all_not_before
) VALUES (?, ?, ?)
ON CONFLICT(destination_fingerprint) DO UPDATE SET
    error_not_before = CASE
      WHEN excluded.error_not_before IS NULL THEN agent_switch_failure_delivery_state.error_not_before
      WHEN agent_switch_failure_delivery_state.error_not_before IS NULL OR excluded.error_not_before > agent_switch_failure_delivery_state.error_not_before THEN excluded.error_not_before
      ELSE agent_switch_failure_delivery_state.error_not_before END,
    all_not_before = CASE
      WHEN excluded.all_not_before IS NULL THEN agent_switch_failure_delivery_state.all_not_before
      WHEN agent_switch_failure_delivery_state.all_not_before IS NULL OR excluded.all_not_before > agent_switch_failure_delivery_state.all_not_before THEN excluded.all_not_before
      ELSE agent_switch_failure_delivery_state.all_not_before END;

-- name: ExpireAgentSwitchFailurePayloads :execrows
DELETE FROM agent_switch_failure_outbox WHERE expires_at <= ?;

-- name: ResolveAgentSwitchFailureReceipts :execrows
UPDATE agent_switch_failure_receipts
SET retain_until = sqlc.arg(retain_until)
WHERE switch_id = sqlc.arg(switch_id) AND retain_until IS NULL
  AND durable_state_fingerprint <> sqlc.arg(durable_state_fingerprint);

-- name: DeleteExpiredAgentSwitchFailureReceipts :execrows
DELETE FROM agent_switch_failure_receipts WHERE retain_until IS NOT NULL AND retain_until <= ?;

-- name: AgentSwitchFailureBacklog :one
SELECT
  CAST(COALESCE(SUM(CASE WHEN delivered_at IS NULL AND discarded_at IS NULL AND lease_token IS NULL THEN 1 ELSE 0 END),0) AS INTEGER) AS pending,
  CAST(COALESCE(SUM(CASE WHEN delivered_at IS NULL AND discarded_at IS NULL AND lease_token IS NOT NULL THEN 1 ELSE 0 END),0) AS INTEGER) AS leased,
  CAST(COALESCE(SUM(CASE WHEN delivered_at IS NOT NULL THEN 1 ELSE 0 END),0) AS INTEGER) AS delivered,
  CAST(COALESCE(SUM(CASE WHEN discarded_at IS NOT NULL THEN 1 ELSE 0 END),0) AS INTEGER) AS discarded,
  MIN(CASE WHEN delivered_at IS NULL AND discarded_at IS NULL AND available_at <= sqlc.arg(now) THEN available_at END) AS oldest_due
FROM agent_switch_failure_outbox;
