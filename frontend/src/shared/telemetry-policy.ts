export type TelemetryPolicyDiskRecord = {
	schema_version: 1;
	events_enabled: boolean;
	consent_generation: string;
	updated_at: string;
};

export type TelemetryPolicySnapshot = {
	eventsEnabled: boolean;
	consentGeneration: string;
	updatedAt: string;
	acknowledged: boolean;
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
const RECORD_KEYS = ["consent_generation", "events_enabled", "schema_version", "updated_at"];

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
	const keys = Object.keys(record).sort();
	if (keys.length !== RECORD_KEYS.length || keys.some((key, index) => key !== RECORD_KEYS[index])) {
		return { ok: false, reason: "invalid_record" };
	}
	if (record.schema_version !== 1 || typeof record.events_enabled !== "boolean") {
		return { ok: false, reason: "invalid_record" };
	}
	if (typeof record.consent_generation !== "string" || !GENERATION.test(record.consent_generation)) {
		return { ok: false, reason: "invalid_record" };
	}
	if (typeof record.updated_at !== "string" || !isCanonicalTimestamp(record.updated_at)) {
		return { ok: false, reason: "invalid_record" };
	}
	return { ok: true, record: record as TelemetryPolicyDiskRecord };
}

function isCanonicalTimestamp(value: string): boolean {
	const timestamp = Date.parse(value);
	return Number.isFinite(timestamp) && new Date(timestamp).toISOString() === value;
}

export function telemetryPolicySnapshot(record: TelemetryPolicyDiskRecord, acknowledged: boolean): TelemetryPolicySnapshot {
	return {
		eventsEnabled: record.events_enabled,
		consentGeneration: record.consent_generation,
		updatedAt: record.updated_at,
		acknowledged,
	};
}

/**
 * Whether an unsettled policy view can still be advanced by retrying.
 *
 * Keyed on `durabilitySupported`, not on `reason`. Durability is a permanent
 * property of the platform — Windows has no durable policy replacement, so
 * `retryPendingReplacement` always throws and every retry is a guaranteed
 * failure, which the 1s timer in `main.ts` would repeat for the life of the
 * process (#5196). `reason` cannot carry that fact reliably: the controller's
 * catch relabels it (`desktop-telemetry-controller.ts`), and
 * `failClosedTelemetryPolicyView` reports `invalid_authority` on a view that
 * already declares no durability support. `durabilitySupported` is set by
 * `toView` on every path and survives those rewrites.
 *
 * Everything else is treated as transient — the daemon may not be listening
 * yet, or a purge may succeed on a later attempt. That is not always true: an
 * authority rejected as unsafe at load time can never become writable, because
 * `load()` short-circuits on `loaded`. Retrying it is a local no-op that still
 * broadcasts policy IPC once a second. Fixing that needs the authority to
 * surface its writability on the view; it is a pre-existing loop this predicate
 * deliberately does not claim to solve.
 */
export function telemetryPolicyRetryable(view: TelemetryPolicyView): boolean {
	return view.state !== "applied" && view.durabilitySupported;
}
