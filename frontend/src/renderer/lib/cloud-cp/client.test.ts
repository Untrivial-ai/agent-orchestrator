import { describe, expect, it, vi } from "vitest";
import { createCloudCpClient } from "./client";

describe("Cloud control-plane interface transitions", () => {
	it("cancels an active interface transition through the Cloud API", async () => {
		const fetchImpl = vi.fn().mockResolvedValue(
			new Response(JSON.stringify({ ok: true }), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			}),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test",
			getToken: async () => "bearer-token",
			fetchImpl,
		});

		await expect(client.cancelInterfaceTransition("org/a", "session b")).resolves.toEqual({ ok: true });
		expect(fetchImpl).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2Fa/sessions/session%20b/interface-transition",
			expect.objectContaining({ method: "DELETE" }),
		);
	});

	it("acknowledges an interface transition notice through the Cloud API", async () => {
		const fetchImpl = vi.fn().mockResolvedValue(
			new Response(JSON.stringify({ ok: true }), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			}),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test",
			getToken: async () => "bearer-token",
			fetchImpl,
		});

		await expect(
			client.acknowledgeInterfaceTransitionNotice("org/a", "session b", "transition/c"),
		).resolves.toEqual({ ok: true });
		expect(fetchImpl).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2Fa/sessions/session%20b/interface-transition/transition%2Fc/notice-acknowledgement",
			expect.objectContaining({ method: "PUT" }),
		);
	});
});
