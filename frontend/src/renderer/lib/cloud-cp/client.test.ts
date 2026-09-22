import { describe, expect, it, vi } from "vitest";
import { createCloudCpClient } from "./client";

describe("cloud control-plane session lifecycle", () => {
	it("uses the organization GitHub App installation and repository routes", async () => {
		const responses = [
			{ installationUrl: "https://github.com/apps/ao/installations/new", expiresAt: "2026-09-22T12:00:00Z" },
			{ installations: [] },
			{ items: [], page: { hasMore: false } },
			{ project: { id: "project-1" } },
		];
		const fetchMock = vi.fn(async () =>
			new Response(JSON.stringify(responses.shift()), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			}),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.test",
			getToken: async () => "token",
			fetchImpl: fetchMock as typeof fetch,
		});

		await client.startGitHubInstallation("org/1");
		await client.listGitHubInstallations("org/1");
		await client.listGitHubRepositories("org/1");
		await client.createProjectFromGitHub("org/1", {
			githubRepositoryId: "42",
			displayName: "widgets",
			config: { worker: { agent: "codex" } },
		});

		const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
		expect(calls.map(([url]) => url)).toEqual([
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/github/installations/start",
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/github/installations",
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/github/repositories",
			"https://cloud.example.test/api/cloud/v1/orgs/org%2F1/github/projects",
		]);
		expect(calls[0]?.[1]).toEqual(expect.objectContaining({ method: "POST" }));
		expect(calls[3]?.[1]).toEqual(expect.objectContaining({
			method: "POST",
			body: JSON.stringify({
				githubRepositoryId: "42",
				displayName: "widgets",
				config: { worker: { agent: "codex" } },
			}),
		}));
	});

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
