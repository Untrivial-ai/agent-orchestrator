import { describe, expect, it, vi } from "vitest";
import { createCloudCpClient } from "./client";

describe("cloud control-plane session lifecycle", () => {
	it("uses the cloud review endpoints for an encoded session", async () => {
		const fetchMock = vi.fn(
			async () =>
				new Response(JSON.stringify({ sessionId: "session/1", reviews: [], runs: [] }), {
					status: 201,
					headers: { "Content-Type": "application/json" },
				}),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test/",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		await client.triggerSessionReviews("org/1", "session/1");

		expect(fetchMock).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/reviews/trigger",
			expect.objectContaining({ method: "POST" }),
		);
	});

	it("asks the control plane to deliver a stored review to the worker", async () => {
		const fetchMock = vi.fn(
			async (_input: RequestInfo | URL, _init?: RequestInit) =>
				new Response(JSON.stringify({ event: { sequence: 12 } }), {
					status: 202,
					headers: { "Content-Type": "application/json" },
				}),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test/",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		await client.sendSessionReviewToWorker("org/1", "session/1", "run/1", {
			idempotencyKey: "review-run-1",
		});

		expect(fetchMock).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/reviews/run%2F1/send",
			expect.objectContaining({ method: "POST" }),
		);
		const request = fetchMock.mock.calls[0]?.[1] as RequestInit;
		expect(new Headers(request.headers).get("Idempotency-Key")).toBe("review-run-1");
	});

	it("inspects and installs only through typed cloud harness routes", async () => {
		const fetchMock = vi.fn(
			async () =>
				new Response(JSON.stringify({ harness: "cursor", status: "ready" }), {
					status: 200,
					headers: { "Content-Type": "application/json" },
				}),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test/",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		await client.installSessionReviewerHarness("org/1", "session/1", "cursor");

		expect(fetchMock).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/reviewer-harnesses/cursor/install",
			expect.objectContaining({ method: "POST" }),
		);
	});
	it("posts explicit resume intent for one encoded session", async () => {
		const fetchMock = vi.fn(
			async () =>
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
		const fetchMock = vi.fn(
			async () =>
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
