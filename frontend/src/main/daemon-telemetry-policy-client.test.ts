import { describe, expect, it, vi } from "vitest";
import { DaemonTelemetryPolicyClient } from "./daemon-telemetry-policy-client";

describe("DaemonTelemetryPolicyClient", () => {
	it("accepts only an exact loopback control origin and typed acknowledgement", async () => {
		const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
			status: "applied", consentGeneration: "generation-1", eventsEnabled: false, gateDrained: true, purgeConfirmed: true,
		}), { status: 200, headers: { "content-type": "application/json" } }));
		const client = new DaemonTelemetryPolicyClient(() => "http://127.0.0.1:3001", fetcher);
		await expect(client.applyPolicy("generation-1", false)).resolves.toEqual({
			status: "applied", consentGeneration: "generation-1", eventsEnabled: false, gateDrained: true, purgeConfirmed: true,
		});
		expect(fetcher).toHaveBeenCalledWith("http://127.0.0.1:3001/internal/agent-switch-observability/apply-policy", expect.objectContaining({ method: "POST" }));
	});

	it("rejects non-loopback origins before issuing a request", async () => {
		const fetcher = vi.fn();
		const client = new DaemonTelemetryPolicyClient(() => "https://daemon.example", fetcher);
		await expect(client.prepareDisable()).rejects.toThrow("loopback");
		expect(fetcher).not.toHaveBeenCalled();
	});

	it("rejects stale or malformed acknowledgements", async () => {
		const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: "applied", consentGeneration: "old", eventsEnabled: false, gateDrained: true, purgeConfirmed: true }), { status: 200 }));
		const client = new DaemonTelemetryPolicyClient(() => "http://127.0.0.1:3001", fetcher);
		await expect(client.applyPolicy("new", false)).rejects.toThrow("generation mismatch");
	});

	it("requires prepare-disable drain proof without claiming the later purge", async () => {
		const valid = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: "applied", consentGeneration: "generation-on", eventsEnabled: false, gateDrained: true, purgeConfirmed: false }), { status: 200 }));
		await expect(new DaemonTelemetryPolicyClient(() => "http://127.0.0.1:3001", valid).prepareDisable()).resolves.toMatchObject({ gateDrained: true, purgeConfirmed: false });

		const fabricated = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: "applied", consentGeneration: "generation-on", eventsEnabled: false, gateDrained: false, purgeConfirmed: false }), { status: 200 }));
		await expect(new DaemonTelemetryPolicyClient(() => "http://127.0.0.1:3001", fabricated).prepareDisable()).rejects.toThrow("drain proof");
	});

	it("rejects disable completion without both drain and purge proof", async () => {
		const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: "applied", consentGeneration: "generation-off", eventsEnabled: false, gateDrained: false, purgeConfirmed: true }), { status: 200 }));
		const client = new DaemonTelemetryPolicyClient(() => "http://127.0.0.1:3001", fetcher);
		await expect(client.applyPolicy("generation-off", false)).rejects.toThrow("cleanup proof");
	});
});
