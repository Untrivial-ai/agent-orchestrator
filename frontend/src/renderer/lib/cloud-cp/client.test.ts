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

		expect(fetchMock).toHaveBeenNthCalledWith(
			1,
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/diff",
			expect.anything(),
		);
		expect(fetchMock).toHaveBeenNthCalledWith(
			2,
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/file/diff?path=notes%2Fone+two.txt",
			expect.anything(),
		);
	});

	it("calls the complete provider-neutral workspace review contract", async () => {
		const fetchMock = vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		await client.getWorkspaceReview("org/1", "session/1");
		await client.getWorkspaceReviewTree("org/1", "session/1", "src/lib");
		await client.searchWorkspaceReview("org/1", "session/1", { query: "app", cursor: "next", limit: 20 });
		await client.getWorkspaceReviewFile("org/1", "session/1", { path: "src/App.tsx", scope: "committed", commitSha: "abc" });
		await client.getWorkspaceReviewDiffs("org/1", "session/1", {
			scope: "staged", paths: ["src/App.tsx"], contextLines: 5, ignoreWhitespace: true, workspaceVersion: "v1",
		});
		await client.getWorkspaceReviewRevision("org/1", "session/1", {
			path: "src/App.tsx", scope: "unstaged", side: "before", workspaceVersion: "v1", expectedRevision: "r1",
		});
		await client.updateWorkspaceReviewFile("org/1", "session/1", {
			path: "src/App.tsx", content: "updated\n", expectedFileFingerprint: "fp1",
		});

		const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
		expect(calls.map(([request]) => request)).toEqual([
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/review",
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/tree?path=src%2Flib",
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/search?query=app&cursor=next&limit=20",
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/review/file?path=src%2FApp.tsx&scope=committed&commitSha=abc",
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/review/diffs",
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/review/revision?path=src%2FApp.tsx&scope=unstaged&side=before&workspaceVersion=v1&expectedRevision=r1",
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/sessions/session%2F1/workspace/review/file",
		]);
		expect(fetchMock).toHaveBeenNthCalledWith(5, expect.any(String), expect.objectContaining({ method: "POST", body: JSON.stringify({
			scope: "staged", paths: ["src/App.tsx"], contextLines: 5, ignoreWhitespace: true, workspaceVersion: "v1",
		}) }));
		expect(fetchMock).toHaveBeenNthCalledWith(7, expect.any(String), expect.objectContaining({ method: "PUT", body: JSON.stringify({
			path: "src/App.tsx", content: "updated\n", expectedFileFingerprint: "fp1",
		}) }));
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
