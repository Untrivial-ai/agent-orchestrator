import { describe, expect, it, vi } from "vitest";
import { createCloudCpClient } from "./client";

describe("cloud control-plane session lifecycle", () => {
	it("loads detailed pull requests for a cloud session", async () => {
		const fetchMock = vi.fn(async () =>
			new Response(JSON.stringify({ sessionId: "session/1", pullRequests: [] }), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			}),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test/",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		const response = await client.listSessionPullRequests("org/1", "session/1");

		expect(response).toEqual({ sessionId: "session/1", pullRequests: [] });
		expect(fetchMock).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/pull-requests",
			expect.objectContaining({ method: "GET" }),
		);
	});

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

	it("patches automatic CI feedback for one cloud session", async () => {
		const fetchMock = vi.fn(async () => new Response(JSON.stringify({ session: { id: "session/1", autoInjectCI: false } }), { status: 200, headers: { "Content-Type": "application/json" } }));
		const client = createCloudCpClient({ baseUrl: "https://cloud.example.test/", getToken: async () => "token", fetchImpl: fetchMock as typeof fetch });

		await client.setSessionAutoInjectCI("org/1", "session/1", false);

		expect(fetchMock).toHaveBeenCalledWith(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/auto-inject-ci",
			expect.objectContaining({ method: "PATCH", body: JSON.stringify({ autoInjectCI: false }) }),
		);
	});

	it.each([
		["review feedback", "setSessionAutoInjectReview", "auto-inject-review", "autoInjectReview"],
		["terminate-on-merge", "setSessionMergePolicy", "merge-policy", "terminateOnPrMerge"],
	] as const)("patches %s for one cloud session", async (_label, method, path, field) => {
		const fetchMock = vi.fn(async () => new Response(JSON.stringify({ session: { id: "session/1", [field]: false } }), { status: 200, headers: { "Content-Type": "application/json" } }));
		const client = createCloudCpClient({ baseUrl: "https://cloud.example.test/", getToken: async () => "token", fetchImpl: fetchMock as typeof fetch });

		await client[method]("org/1", "session/1", false);

		expect(fetchMock).toHaveBeenCalledWith(
			`https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/${path}`,
			expect.objectContaining({ method: "PATCH", body: JSON.stringify({ [field]: false }) }),
		);
	});
});
