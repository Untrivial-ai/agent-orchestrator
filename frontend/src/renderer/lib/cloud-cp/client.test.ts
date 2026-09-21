import { describe, expect, it, vi } from "vitest";
import { createCloudCpClient } from "./client";

describe("Cloud control-plane interface transitions", () => {
	it("preserves caller message ids for Cloud send retries and steering", async () => {
		const fetchImpl = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ event: {} }), { status: 202, headers: { "Content-Type": "application/json" } }));
		const client = createCloudCpClient({ baseUrl: "https://cloud.example.test", getToken: async () => "token", fetchImpl });
		await client.sendSessionMessage("org", "session", { text: "retry me" }, { idempotencyKey: "stable-message-id" });
		await client.steerTurn("org", "session", "turn", { text: "change course" }, { idempotencyKey: "stable-steer-id" });
		expect((fetchImpl.mock.calls[0]?.[1] as RequestInit).headers).toBeInstanceOf(Headers);
		expect(((fetchImpl.mock.calls[0]?.[1] as RequestInit).headers as Headers).get("Idempotency-Key")).toBe("stable-message-id");
		expect(fetchImpl.mock.calls[1]?.[0]).toContain("/turns/turn/steer");
		expect(((fetchImpl.mock.calls[1]?.[1] as RequestInit).headers as Headers).get("Idempotency-Key")).toBe("stable-steer-id");
	});

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

describe("cloud control-plane session lifecycle", () => {
	it("posts explicit resume intent for one encoded session", async () => {
		const fetchMock = vi.fn(async () =>
			new Response(
				JSON.stringify({
					session: {
						id: "session/1",
						sandboxProvider: "coder",
						desiredState: "running",
						observedState: "stopped",
					},
				}),
				{ status: 202, headers: { "Content-Type": "application/json" } },
			),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test/",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		const response = await client.resumeSession("org/1", "session/1");

		expect(response.session.desiredState).toBe("running");
		expect(fetchMock).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/resume",
			expect.objectContaining({ method: "POST" }),
		);
	});

	it("posts restore intent for one deleted, encoded session", async () => {
		const fetchMock = vi.fn(async () =>
			new Response(
				JSON.stringify({
					session: {
						id: "session/1",
						desiredState: "running",
					},
				}),
				{ status: 202, headers: { "Content-Type": "application/json" } },
			),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test/",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		const response = await client.restoreSession("org/1", "session/1");

		expect(response.session.desiredState).toBe("running");
		expect(fetchMock).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/restore",
			expect.objectContaining({ method: "POST" }),
		);
	});
});
