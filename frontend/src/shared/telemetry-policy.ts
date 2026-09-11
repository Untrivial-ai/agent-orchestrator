export const AGENT_SWITCH_FAILURE_PRODUCTION_ENABLED = false;

export type TelemetryPolicyDiskRecord = {
	schema_version: 2;
	events_enabled: boolean;
	consent_generation: string;
	consent_production_enabled: boolean;
	updated_at: string;
};

export type TelemetryPolicySnapshot = {
	eventsEnabled: boolean;
	consentGeneration: string;
	updatedAt: string;
	acknowledged: boolean;
	/** The stored opt-in was given while the release gate was closed and the gate is now open. */
	consentRenewalRequired: boolean;
};

export type TelemetryPolicyApplyState = "applied" | "cleanup_pending" | "cleanup_failed";

export type TelemetryPolicyView = TelemetryPolicySnapshot & {
	state: TelemetryPolicyApplyState;
	environmentVeto: boolean;
	durabilitySupported: boolean;
	reason?: "environment_veto" | "durability_unsupported" | "release_blocked" | "invalid_authority" | "daemon_cleanup_pending" | "cleanup_failed";
};

export type RendererTelemetryCapture = {
	consentGeneration: string;
	kind: "exception" | "message" | "breadcrumb";
	message: string;
	level?: "fatal" | "error" | "warning" | "info";
	tags?: Record<string, string>;
};

export type RendererTelemetryCaptureInput = Omit<RendererTelemetryCapture, "consentGeneration">;

export const TELEMETRY_POLICY_CHANGED_CHANNEL = "telemetry:policyChanged";
export const TELEMETRY_CLEAR_RENDERER_QUEUES_CHANNEL = "telemetry:clearRendererQueues";
export const TELEMETRY_RENDERER_QUEUES_CLEARED_CHANNEL = "telemetry:rendererQueuesCleared";

export type RendererTelemetryQueuePurgeRequest = { requestId: string };
export type RendererTelemetryQueuePurgeResult = { requestId: string; ok: boolean };

export type TelemetryPolicyParseResult =
	| { ok: true; record: TelemetryPolicyDiskRecord }
	| { ok: false; reason: "invalid_record" };

const GENERATION = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const RECORD_KEYS_V1 = ["consent_generation", "events_enabled", "schema_version", "updated_at"];
const RECORD_KEYS_V2 = ["consent_generation", "consent_production_enabled", "events_enabled", "schema_version", "updated_at"];

export function parseTelemetryPolicyDiskRecord(raw: string): TelemetryPolicyParseResult {
	if (raw.length === 0 || raw.length > 4096) return { ok: false, reason: "invalid_record" };
	let value: unknown;
	try {
		value = JSON.parse(raw);
	} catch {
		return { ok: false, reason: "invalid_record" };
	}
	if (!value || typeof value !== "object" || Array.isArray(value)) return { ok: false, reason: "invalid_record" };
	const record = value as Record<string, unknown>;
	const expectedKeys = record.schema_version === 1 ? RECORD_KEYS_V1 : record.schema_version === 2 ? RECORD_KEYS_V2 : null;
	const keys = Object.keys(record).sort();
	if (!expectedKeys || keys.length !== expectedKeys.length || keys.some((key, index) => key !== expectedKeys[index])) {
		return { ok: false, reason: "invalid_record" };
	}
	if (typeof record.events_enabled !== "boolean") {
		return { ok: false, reason: "invalid_record" };
	}
	if (record.schema_version === 2 && typeof record.consent_production_enabled !== "boolean") {
		return { ok: false, reason: "invalid_record" };
	}
	if (typeof record.consent_generation !== "string" || !GENERATION.test(record.consent_generation)) {
		return { ok: false, reason: "invalid_record" };
	}
	if (typeof record.updated_at !== "string" || !isCanonicalTimestamp(record.updated_at)) {
		return { ok: false, reason: "invalid_record" };
	}
	return { ok: true, record: {
		schema_version: 2,
		events_enabled: record.events_enabled,
		consent_generation: record.consent_generation,
		// Every version 1 record was written before the release gate ever opened.
		consent_production_enabled: record.schema_version === 2 ? record.consent_production_enabled as boolean : false,
		updated_at: record.updated_at,
	} };
}

function isCanonicalTimestamp(value: string): boolean {
	const timestamp = Date.parse(value);
	return Number.isFinite(timestamp) && new Date(timestamp).toISOString() === value;
}

/**
 * An opt-in given while the release gate was closed is not carried into a
 * release that opens it: the user agreed while reporting read as disabled.
 * The daemon applies the same rule (agentswitch readAuthority).
 */
export function telemetryPolicySnapshot(record: TelemetryPolicyDiskRecord, acknowledged: boolean, productionEnabled: boolean): TelemetryPolicySnapshot {
	return {
		eventsEnabled: record.events_enabled && (!productionEnabled || record.consent_production_enabled),
		consentRenewalRequired: record.events_enabled && productionEnabled && !record.consent_production_enabled,
		consentGeneration: record.consent_generation,
		updatedAt: record.updated_at,
		acknowledged,
	};
}

/**
 * Whether an unsettled policy can still be advanced by retrying. Keyed on
 * durabilitySupported rather than reason: reason is rewritten by the
 * controller's catch and by failClosedTelemetryPolicyView (main.ts).
 */
export function telemetryPolicyRetryable(view: TelemetryPolicyView): boolean {
	return view.state !== "applied" && view.durabilitySupported;
}
