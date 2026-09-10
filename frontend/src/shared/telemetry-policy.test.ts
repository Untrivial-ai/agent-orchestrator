import { describe, expect, it } from "vitest";
import { parseTelemetryPolicyDiskRecord, telemetryPolicyRetryable, type TelemetryPolicyView } from "./telemetry-policy";

describe("telemetry policy wire record", () => {
	it("accepts only the exact snake_case versioned record", () => {
		expect(parseTelemetryPolicyDiskRecord(JSON.stringify({
			schema_version: 1,
			events_enabled: false,
			consent_generation: "7f80c8a9-ec67-4a16-a067-a444ffcc5cca",
			updated_at: "2026-08-28T10:15:30.000Z",
		}))).toEqual({ ok: true, record: {
			schema_version: 1,
			events_enabled: false,
			consent_generation: "7f80c8a9-ec67-4a16-a067-a444ffcc5cca",
			updated_at: "2026-08-28T10:15:30.000Z",
		} });
	});

	it.each([
		"{}",
		'{"schema_version":2,"events_enabled":false,"consent_generation":"7f80c8a9-ec67-4a16-a067-a444ffcc5cca","updated_at":"2026-08-28T10:15:30.000Z"}',
		'{"schema_version":1,"events_enabled":false,"consent_generation":"not-a-uuid","updated_at":"2026-08-28T10:15:30.000Z"}',
		'{"schema_version":1,"events_enabled":false,"consent_generation":"7f80c8a9-ec67-4a16-a067-a444ffcc5cca","updated_at":"yesterday"}',
		'{"schema_version":1,"events_enabled":false,"consent_generation":"7f80c8a9-ec67-4a16-a067-a444ffcc5cca","updated_at":"2026-08-28T10:15:30.000Z","extra":true}',
		'{"schemaVersion":1,"eventsEnabled":false,"consentGeneration":"7f80c8a9-ec67-4a16-a067-a444ffcc5cca","updatedAt":"2026-08-28T10:15:30.000Z"}',
	])("fails closed for malformed or expanded records: %s", (raw) => {
		expect(parseTelemetryPolicyDiskRecord(raw)).toEqual({ ok: false, reason: "invalid_record" });
	});
});

describe("telemetryPolicyRetryable", () => {
	const base: TelemetryPolicyView = {
		eventsEnabled: false,
		consentGeneration: "7f80c8a9-ec67-4a16-a067-a444ffcc5cca",
		updatedAt: "2026-08-28T10:15:30.000Z",
		acknowledged: false,
		state: "cleanup_pending",
		environmentVeto: false,
		durabilitySupported: true,
	};

	it("does not retry a settled policy", () => {
		expect(telemetryPolicyRetryable({ ...base, state: "applied" })).toBe(false);
		expect(telemetryPolicyRetryable({ ...base, state: "applied", reason: "release_blocked" })).toBe(false);
	});

	it("retries transient daemon and cleanup failures", () => {
		expect(telemetryPolicyRetryable({ ...base, state: "cleanup_pending", reason: "daemon_cleanup_pending" })).toBe(true);
		expect(telemetryPolicyRetryable({ ...base, state: "cleanup_failed", reason: "cleanup_failed" })).toBe(true);
	});

	it("never retries a platform without durable policy replacement", () => {
		expect(telemetryPolicyRetryable({ ...base, state: "cleanup_failed", durabilitySupported: false, reason: "durability_unsupported" })).toBe(false);
	});

	it("keys on durabilitySupported rather than the reason label", () => {
		expect(telemetryPolicyRetryable({ ...base, state: "cleanup_failed", durabilitySupported: false, reason: "cleanup_failed" })).toBe(false);
		expect(telemetryPolicyRetryable({ ...base, state: "cleanup_failed", durabilitySupported: false, reason: "invalid_authority" })).toBe(false);
	});
});
