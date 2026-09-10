import { describe, expect, it } from "vitest";
import { parseTelemetryPolicyDiskRecord } from "./telemetry-policy";

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
