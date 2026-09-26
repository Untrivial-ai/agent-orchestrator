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
