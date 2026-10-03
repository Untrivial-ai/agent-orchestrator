import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn() }));
vi.mock("expo/fetch", () => ({ fetch: vi.fn() }));

import {
	cancelSessionReview,
	getSessionReviews,
	requestSessionRereview,
	resolveSessionReviewComment,
	restoreSessionReviewer,
	switchSessionReviewer,
	triggerSessionReview,
	type PRReviewState,
	type ReviewRun,
	type SessionReviews,
} from "./api";
import type { ServerConfig } from "./config";
import { conversationErrorIsPermanent } from "./chat/conversationErrors";
import { reviewBatchAction, reviewForPullRequest, reviewPrimaryAction, reviewStatusLabel, reviewVerdictLabel } from "./reviewView";

const cfg: ServerConfig = { host: "ao.test", httpPort: "3011", muxPort: "3011", secure: false, password: "secret12" };
const sessionId = "worker/1";
const pr1 = "https://github.com/ao/repo/pull/41";
const pr2 = "https://github.com/ao/repo/pull/42";

function run(over: Partial<ReviewRun> = {}): ReviewRun {
	return {
		id: "run-1", reviewId: "review-1", sessionId, batchId: "batch-1", harness: "codex",
		triggerSource: "manual", prUrl: pr1, targetSha: "sha-1", status: "complete",
		verdict: "approved", body: "Looks good", githubReviewId: "101",
		createdAt: "2026-09-23T00:00:00Z", autoInjectReview: true, ...over,
	};
}

function review(prUrl: string, prNumber: number, over: Partial<PRReviewState> = {}): PRReviewState {
	return { prUrl, prNumber, title: `PR ${prNumber}`, targetSha: `sha-${prNumber}`, status: "needs_review", ...over };
}

class ReviewDaemon {
	state: SessionReviews = { reviewerHandleId: "reviewer-1", reviewerHarness: "codex", reviews: [], runs: [] };
	requests: Array<{ path: string; method: string; body?: unknown }> = [];

	fetch = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
		const url = new URL(String(input));
		const path = url.pathname;
		const method = init?.method ?? "GET";
		const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
		this.requests.push({ path, method, body });

		if (method === "GET" && path.endsWith("/reviews")) return json(this.state);
		if (path.endsWith("/reviews/trigger")) {
			const batchId = "batch-2";
			this.state.reviews = this.state.reviews.map((item, index) => ({
				...item,
				status: "running",
				latestRun: run({ id: `running-${index + 1}`, reviewId: `review-${index + 1}`, batchId, prUrl: item.prUrl, targetSha: item.targetSha, status: "running", verdict: "", body: "", githubReviewId: "" }),
			}));
			return json({ ok: true }, 201);
		}
		if (path.endsWith("/reviews/cancel")) {
			this.state.reviews = this.state.reviews.map((item) => item.status !== "running" ? item : ({
				...item,
				status: "needs_review",
				latestRun: item.latestRun ? { ...item.latestRun, status: "cancelled", body: "cancelled by user" } : undefined,
			}));
			return json({ ok: true });
		}
		if (path.endsWith("/reviews/switch")) {
			this.state = { ...this.state, reviewerHandleId: "reviewer-2", reviewerHarness: body?.harness, reviewerActivityState: "active" };
			return json(this.state);
		}
		if (path.endsWith("/reviews/restore")) {
			this.state = { ...this.state, reviewerActivityState: "active" };
			return json({ reviewerHandleId: this.state.reviewerHandleId });
		}
		if (path.endsWith("/reviews/rerequest") || path.endsWith("/reviews/comments/resolve")) return json({ ok: true });
		return json({ code: "NOT_FOUND", message: "not found", requestId: "req-404" }, 404);
	});
}

describe("mobile review lifecycle", () => {
	let daemon: ReviewDaemon;

	beforeEach(() => {
		daemon = new ReviewDaemon();
		vi.stubGlobal("fetch", daemon.fetch);
	});
	afterEach(() => vi.unstubAllGlobals());

	it("moves a multi-PR batch from no reviews through running and cancellation", async () => {
		expect((await getSessionReviews(cfg, sessionId)).reviews).toEqual([]);

		daemon.state.reviews = [review(pr1, 41), review(pr2, 42)];
		const pending = await getSessionReviews(cfg, sessionId);
		expect(reviewBatchAction(pending.reviews[0]!, pending.reviews)).toBe("start");

		await triggerSessionReview(cfg, sessionId);
		const running = await getSessionReviews(cfg, sessionId);
		expect(running.reviews.map((item) => item.status)).toEqual(["running", "running"]);
		expect(new Set(running.reviews.map((item) => item.latestRun?.batchId))).toEqual(new Set(["batch-2"]));
		expect(reviewBatchAction(running.reviews[0]!, running.reviews)).toBe("cancel");

		await cancelSessionReview(cfg, sessionId);
		const cancelled = await getSessionReviews(cfg, sessionId);
		expect(cancelled.reviews.map((item) => item.latestRun?.status)).toEqual(["cancelled", "cancelled"]);
		expect(cancelled.reviews.map((item) => reviewVerdictLabel(item.latestRun!))).toEqual(["Review cancelled", "Review cancelled"]);
	});

	it("distinguishes approval, requested changes, and a new commit needing a fresh pass", async () => {
		const approved = review(pr1, 41, { status: "up_to_date", latestRun: run() });
		const requested = review(pr2, 42, { status: "changes_requested", latestRun: run({ prUrl: pr2, targetSha: "sha-42", verdict: "changes_requested", body: "Fix the race" }) });
		daemon.state.reviews = [approved, requested];

		const completed = await getSessionReviews(cfg, sessionId);
		expect(reviewStatusLabel(completed.reviews[0]!.status)).toBe("Reviewed");
		expect(reviewVerdictLabel(completed.reviews[0]!.latestRun!)).toBe("Approved");
		expect(reviewPrimaryAction(completed.reviews[1]!)).toBe("review_again");

		const previousRun = requested.latestRun;
		daemon.state.reviews[1] = review(pr2, 42, { targetSha: "sha-42-next", status: "needs_review", previousRun });
		const afterPush = await getSessionReviews(cfg, sessionId);
		const current = reviewForPullRequest(afterPush.reviews, pr2, 42)!;
		expect(current).toMatchObject({ targetSha: "sha-42-next", status: "needs_review" });
		expect(current.previousRun).toMatchObject({ targetSha: "sha-42", verdict: "changes_requested" });
		expect(current.latestRun).toBeUndefined();
	});

	it("switches and restores a reviewer without losing the authoritative PR state", async () => {
		daemon.state.reviews = [review(pr1, 41, { status: "running", latestRun: run({ status: "running", verdict: "" }) })];
		daemon.state.reviewerActivityState = "exited";

		const switched = await switchSessionReviewer(cfg, sessionId, "claude-code", { model: "sonnet", effort: "high" });
		expect(switched).toMatchObject({ reviewerHandleId: "reviewer-2", reviewerHarness: "claude-code", reviewerActivityState: "active" });
		expect(switched.reviews[0]).toMatchObject({ prUrl: pr1, status: "running" });

		daemon.state.reviewerActivityState = "exited";
		await restoreSessionReviewer(cfg, sessionId);
		expect((await getSessionReviews(cfg, sessionId)).reviewerActivityState).toBe("active");
		expect(conversationErrorIsPermanent("CHAT_CONTROLLER_NOT_READY", true)).toBe(false);
		expect(conversationErrorIsPermanent("CHAT_DRIVER_UNAVAILABLE", true)).toBe(true);
	});

	it("recovers an in-flight review from daemon state after the mobile client restarts", async () => {
		const activeRun = run({ status: "running", verdict: "", body: "", githubReviewId: "" });
		daemon.state = {
			reviewerHandleId: "reviewer-1",
			reviewerHarness: "codex",
			reviewerActivityState: "active",
			reviewerSurface: { mode: "chat", reviewId: "review-1", harness: "codex" },
			reviews: [review(pr1, 41, { status: "running", latestRun: activeRun })],
			runs: [activeRun],
		};

		// The mobile process owns no review lifecycle state. A fresh client reads the
		// durable daemon response and can resume polling/chat without retriggering.
		vi.unstubAllGlobals();
		vi.stubGlobal("fetch", daemon.fetch);
		const recovered = await getSessionReviews(cfg, sessionId);

		expect(recovered).toMatchObject({
			reviewerHandleId: "reviewer-1",
			reviewerActivityState: "active",
			reviewerSurface: { mode: "chat", reviewId: "review-1" },
		});
		expect(recovered.reviews[0]).toMatchObject({ status: "running", latestRun: { id: "run-1", status: "running" } });
		expect(reviewBatchAction(recovered.reviews[0]!, recovered.reviews)).toBe("cancel");
		expect(daemon.requests.filter((request) => request.path.endsWith("/reviews/trigger"))).toHaveLength(0);
	});

	it("presents daemon-owned automatic retries without spending the manual action path", async () => {
		const firstFailure = run({ id: "auto-1", triggerSource: "auto", status: "failed", verdict: "", body: "controller exited", githubReviewId: "" });
		const secondFailure = run({ id: "auto-2", triggerSource: "auto", status: "failed", verdict: "", body: "controller exited", githubReviewId: "" });
		const retry = run({ id: "auto-3", triggerSource: "auto", status: "running", verdict: "", body: "", githubReviewId: "" });
		daemon.state = {
			reviewerHandleId: "reviewer-1",
			reviewerHarness: "codex",
			reviewerActivityState: "active",
			reviews: [review(pr1, 41, { status: "running", latestRun: retry, previousRun: secondFailure })],
			runs: [firstFailure, secondFailure, retry],
		};

		const observed = await getSessionReviews(cfg, sessionId);
		expect(observed.runs.map((item) => [item.id, item.triggerSource, item.status])).toEqual([
			["auto-1", "auto", "failed"],
			["auto-2", "auto", "failed"],
			["auto-3", "auto", "running"],
		]);
		expect(reviewVerdictLabel(observed.reviews[0]!.previousRun!)).toBe("Review failed");
		expect(reviewStatusLabel(observed.reviews[0]!.status)).toBe("Review in progress");
		expect(daemon.requests.filter((request) => request.method === "POST")).toHaveLength(0);
	});

	it("sends exact external-review and comment-resolution payloads", async () => {
		await requestSessionRereview(cfg, sessionId, pr2, "octocat");
		await resolveSessionReviewComment(cfg, sessionId, pr2, `${pr2}#discussion_r77`);

		expect(daemon.requests.slice(-2)).toEqual([
			{ path: "/api/v1/sessions/worker%2F1/reviews/rerequest", method: "POST", body: { pullRequestUrl: pr2, reviewerId: "octocat" } },
			{ path: "/api/v1/sessions/worker%2F1/reviews/comments/resolve", method: "POST", body: { pullRequestUrl: pr2, commentUrl: `${pr2}#discussion_r77` } },
		]);
	});
});

function json(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}
