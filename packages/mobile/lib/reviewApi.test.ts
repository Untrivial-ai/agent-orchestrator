import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn() }));
vi.mock("expo/fetch", () => ({ fetch: vi.fn() }));

import { ApiError, getSessionPR, killSessionReviewer, requestSessionRereview, resolveSessionReviewComment, switchSessionReviewer, triggerSessionReview } from "./api";
import type { ServerConfig } from "./config";

const cfg: ServerConfig = { host: "ao.test", httpPort: "3011", muxPort: "3011", secure: false, password: "secret12" };

describe("mobile review action API", () => {
	beforeEach(() => vi.stubGlobal("fetch", vi.fn()));
	afterEach(() => vi.unstubAllGlobals());

	it("switches the session reviewer and returns the updated review state", async () => {
		vi.mocked(fetch).mockResolvedValue(response({
			reviewerHandleId: "review-2",
			reviewerHarness: "codex",
			reviews: [{ prNumber: 42, status: "needs_review" }],
			runs: [],
		}));

		const result = await switchSessionReviewer(cfg, "worker/1", "codex", { model: "gpt-5", effort: "high" });

		expect(result).toMatchObject({ reviewerHandleId: "review-2", reviewerHarness: "codex" });
		expect(requests()).toEqual([[
			"http://ao.test:3011/api/v1/sessions/worker%2F1/reviews/switch",
			"POST",
			JSON.stringify({ harness: "codex", agentConfig: { model: "gpt-5", effort: "high" } }),
		]]);
	});

	it("can clear the reviewer override without sending an empty harness", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ reviewerHandleId: "", reviews: [], runs: [] }));

		await switchSessionReviewer(cfg, "worker-1");

		expect(requests()[0]?.[2]).toBe("{}");
	});

	it("reports when the daemon reused an already-reviewed commit", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ created: false, reviewerHandleId: "review-2", reviews: [], runs: [] }));

		const result = await triggerSessionReview(cfg, "worker/1");

		expect(result).toMatchObject({ created: false, reviewerHandleId: "review-2" });
	});

	it("kills the persistent reviewer and returns the authoritative state", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ reviewerHandleId: "", reviewerHarness: "codex", reviews: [], runs: [] }));

		const result = await killSessionReviewer(cfg, "worker/1");

		expect(result).toMatchObject({ reviewerHandleId: "", reviewerHarness: "codex" });
		expect(requests()).toEqual([[
			"http://ao.test:3011/api/v1/sessions/worker%2F1/reviews/kill",
			"POST",
			undefined,
		]]);
	});

	it("requests another external review for the selected pull request", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ ok: true }));

		await requestSessionRereview(cfg, "worker/1", "https://github.com/ao/repo/pull/42", "octocat");

		expect(requests()).toEqual([[
			"http://ao.test:3011/api/v1/sessions/worker%2F1/reviews/rerequest",
			"POST",
			JSON.stringify({ pullRequestUrl: "https://github.com/ao/repo/pull/42", reviewerId: "octocat" }),
		]]);
	});

	it("resolves one review comment by its provider URL", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ ok: true }));

		await resolveSessionReviewComment(cfg, "worker/1", "https://github.com/ao/repo/pull/42", "https://github.com/ao/repo/pull/42#discussion_r1");

		expect(requests()).toEqual([[
			"http://ao.test:3011/api/v1/sessions/worker%2F1/reviews/comments/resolve",
			"POST",
			JSON.stringify({ pullRequestUrl: "https://github.com/ao/repo/pull/42", commentUrl: "https://github.com/ao/repo/pull/42#discussion_r1" }),
		]]);
	});

	it("preserves daemon error details for review actions", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ code: "REVIEWER_NOT_FOUND", message: "Reviewer is no longer eligible", requestId: "req-7" }, 422));

		const error = await requestSessionRereview(cfg, "worker-1", "https://github.com/ao/repo/pull/42", "octocat").catch((value: unknown) => value);

		expect(error).toBeInstanceOf(ApiError);
		expect(error).toMatchObject({
			status: 422,
			code: "REVIEWER_NOT_FOUND",
			requestId: "req-7",
		});
	});

	it("preserves external review entries and comment links from session PR data", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ prs: [{
			number: 42,
			review: {
				unresolvedBy: [{ reviewerId: "octocat", count: 1, links: [{ url: "https://github.com/ao/repo/pull/42#discussion_r1", file: "app.ts", line: 12, body: "Please cover this.", autoInjectReview: false }] }],
				resolvedBy: [{ reviewerId: "hubot", count: 1, links: [] }],
				reviews: [{ reviewerId: "octocat", verdict: "changes_requested", submittedAt: "2026-09-23T00:00:00Z", autoInjectReview: false }],
			},
		}] }));

		const [pr] = await getSessionPR(cfg, "worker-1");

		expect(pr.review.unresolvedBy[0]?.links[0]).toMatchObject({ file: "app.ts", line: 12 });
		expect(pr.review.resolvedBy?.[0]?.reviewerId).toBe("hubot");
		expect(pr.review.reviews?.[0]?.verdict).toBe("changes_requested");
	});
});

function requests(): [unknown, string | undefined, unknown][] {
	return vi.mocked(fetch).mock.calls.map(([url, init]) => [url, init?.method, init?.body]);
}

function response(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}
