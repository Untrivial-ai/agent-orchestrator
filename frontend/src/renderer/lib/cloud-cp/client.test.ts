import { describe, expect, it, vi } from "vitest";
import { createCloudCpClient } from "./client";

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

	it("gets Docker workspace summary and encodes selected diff paths", async () => {
		const fetchMock = vi.fn(async () =>
			new Response(
				JSON.stringify({
					files: [],
					diffBaseRef: "HEAD",
					truncated: { combined: false, stats: false },
				}),
				{ status: 200, headers: { "Content-Type": "application/json" } },
			),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		await client.getWorkspaceDiff("org/1", "session/1");
		await client.readWorkspaceDiffFile("org/1", "session/1", "notes/one two.txt");

		expect(fetchMock.mock.calls[0]?.[0]).toBe(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/diff",
		);
		expect(fetchMock.mock.calls[1]?.[0]).toBe(
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/file/diff?path=notes%2Fone+two.txt",
		);
	});
});
