import { describe, expect, it } from "vitest";
import { telemetryPolicyRetryable, type TelemetryPolicyView } from "./telemetry-policy";

const base: TelemetryPolicyView = {
	eventsEnabled: false,
	consentGeneration: "7f80c8a9-ec67-4a16-a067-a444ffcc5cca",
	updatedAt: "2026-08-28T10:15:30.000Z",
	acknowledged: false,
	state: "cleanup_pending",
	environmentVeto: false,
	durabilitySupported: true,
};

describe("telemetryPolicyRetryable", () => {
	it("does not retry a settled policy", () => {
		expect(telemetryPolicyRetryable({ ...base, state: "applied" })).toBe(false);
		expect(telemetryPolicyRetryable({ ...base, state: "applied", reason: "release_blocked" })).toBe(false);
	});

	it("retries transient daemon and cleanup failures", () => {
		expect(telemetryPolicyRetryable({ ...base, state: "cleanup_pending", reason: "daemon_cleanup_pending" })).toBe(true);
		expect(telemetryPolicyRetryable({ ...base, state: "cleanup_failed", reason: "cleanup_failed" })).toBe(true);
		expect(telemetryPolicyRetryable({ ...base, state: "cleanup_failed", reason: "invalid_authority" })).toBe(true);
	});

	it("never retries a platform without durable policy replacement", () => {
		// Windows: retryPendingReplacement always throws, so every retry is a
		// guaranteed failure that the 1s timer would repeat forever (#5196).
		expect(telemetryPolicyRetryable({ ...base, state: "cleanup_failed", durabilitySupported: false, reason: "durability_unsupported" })).toBe(false);
	});
});
