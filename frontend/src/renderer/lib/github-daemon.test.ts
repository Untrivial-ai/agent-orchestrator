import { beforeEach, describe, expect, it, vi } from "vitest";
import { GitHubDaemonError, isGitHubAuthInvalidError, listGitHubRepos } from "./github-daemon";

vi.mock("./api-client", () => ({
	getApiBaseUrl: () => "http://127.0.0.1:3002",
}));

describe("GitHub daemon client", () => {
	beforeEach(() => {
		vi.restoreAllMocks();
	});

	it("preserves the status and stable code for a revoked credential", async () => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({
			code: "GITHUB_AUTH_INVALID",
			message: "GitHub authorization expired. Reconnect GitHub to continue.",
		}), {
			status: 401,
			headers: { "Content-Type": "application/json" },
		}));

		const error = await listGitHubRepos().catch((caught: unknown) => caught);

		expect(error).toBeInstanceOf(GitHubDaemonError);
		expect(error).toMatchObject({ status: 401, code: "GITHUB_AUTH_INVALID" });
		expect(isGitHubAuthInvalidError(error)).toBe(true);
	});
});
