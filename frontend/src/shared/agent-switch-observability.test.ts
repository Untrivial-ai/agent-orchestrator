import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
	buildVisibilityEvent,
	encodeAgentSwitchEnvelopeV1,
	parseAgentSwitchDSN,
	parseAgentSwitchVisibilitySignal,
} from "./agent-switch-observability";

describe("agent switch visibility IPC", () => {
	it("accepts only the closed signal shape and never accepts sender identity", () => {
		expect(parseAgentSwitchVisibilitySignal({ consentGeneration: "generation-1", signal: { kind: "focus", value: true } })).toEqual({ consentGeneration: "generation-1", signal: { kind: "focus", value: true } });
		expect(parseAgentSwitchVisibilitySignal({ consentGeneration: "generation-1", senderId: 4, signal: { kind: "focus", value: true } })).toBeNull();
		expect(parseAgentSwitchVisibilitySignal({ consentGeneration: "generation-1", signal: { kind: "transport", operation: "active", healthy: false, active: true, route: "secret" } })).toBeNull();
	});
});

describe("agent switch visibility envelope", () => {
	it("uses the frozen byte-length wrapper shared with Go", () => {
		const fixturePath = resolve(process.cwd(), "../test/fixtures/agent-switch-observability/envelope-v1.json");
		const canonical = readFileSync(fixturePath);
		const encoded = encodeAgentSwitchEnvelopeV1("0123456789abcdef0123456789abcdef", canonical);
		const prefix = Buffer.from(encoded).subarray(0, encoded.length - canonical.length).toString("utf8");
		expect(prefix).toBe(`{"event_id":"0123456789abcdef0123456789abcdef"}\n{"type":"event","length":${canonical.length}}\n`);
	});

	it("constructs remote bytes exclusively from allowlisted incident facts", () => {
		const event = buildVisibilityEvent({
			eventId: "fedcba9876543210fedcba9876543210",
			occurredAt: "2026-08-28T10:15:30.000Z",
			failurePoint: "visibility_presentation",
			operation: "recovery_required",
			presentationKind: "recovery_required",
			durableState: "starting_target",
			elapsedTimeBucket: "under_5s",
		}, { release: "1.2.3", environment: "stable", channel: "stable", os: "darwin" });
		const bytes = new TextDecoder().decode(encodeAgentSwitchEnvelopeV1("fedcba9876543210fedcba9876543210", event));
		for (const local of ["local-switch-sentinel", "local-route-sentinel", "local-token-sentinel", "generation-sentinel", "sender-sentinel"]) {
			expect(bytes).not.toContain(local);
		}
		expect(JSON.parse(new TextDecoder().decode(event))).toMatchObject({
			fingerprint: ["agent-switch-visibility", "v1", "visibility_presentation", "recovery_required"],
			tags: { report_kind: "visibility_failure", platform: "renderer", durable_state: "starting_target" },
		});
	});
});

describe("agent switch visibility DSN", () => {
	it("normalizes a standard DSN and rejects unsafe variants", () => {
		expect(parseAgentSwitchDSN("https://public@example.com/base/42", true)).toMatchObject({ endpoint: "https://example.com/base/api/42/envelope/", publicKey: "public" });
		for (const raw of [
			"https://public:secret@example.com/42",
			"https://public@example.com/../42",
			"https://public@example.com/%2e%2e/42",
			"https://public@example.com/42?query=1",
			"http://public@example.com/42",
		]) expect(() => parseAgentSwitchDSN(raw, true)).toThrow();
	});
});
