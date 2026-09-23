import assert from "node:assert/strict";
import test from "node:test";
import {
	classifyPullRequest,
	extractPullRequestNumbers,
	productArea,
	pullRequestSummary,
	renderHistoricalWeek,
	renderWeeklyDraft,
} from "./changelog-core.mjs";
import { selectDraftInputs } from "./draft.mjs";
import { resolveWeeklyDate } from "./resolve-date.mjs";

const pullRequest = (overrides = {}) => ({
	number: 42,
	title: "feat(chat): keep queued messages after restart",
	url: "https://github.com/Untrivial-ai/agent-orchestrator/pull/42",
	labels: [],
	mergedAt: "2026-09-20T12:00:00Z",
	...overrides,
});

test("classifies conventional user-facing changes", () => {
	assert.equal(classifyPullRequest(pullRequest()), "feature");
	assert.equal(
		classifyPullRequest(pullRequest({ title: "fix: restore terminal focus" })),
		"fix",
	);
	assert.equal(
		classifyPullRequest(pullRequest({ title: "chore: update dependencies" })),
		"skip",
	);
	assert.equal(
		classifyPullRequest(pullRequest({ title: "fix(ci): repair release workflow" })),
		"skip",
	);
	assert.equal(
		classifyPullRequest(pullRequest({ title: "fix: address bugbot follow-up" })),
		"skip",
	);
});

test("explicit labels override the conventional type", () => {
	assert.equal(
		classifyPullRequest(
			pullRequest({ title: "chore: ship a visible migration", labels: ["changelog:include"] }),
		),
		"improvement",
	);
	assert.equal(
		classifyPullRequest(pullRequest({ labels: ["changelog:skip"] })),
		"skip",
	);
});

test("extracts unique pull request references", () => {
	const content = [
		"https://github.com/Untrivial-ai/agent-orchestrator/pull/42",
		"https://github.com/Untrivial-ai/agent-orchestrator/pull/42",
		"Shipped in (#99)",
	].join("\n");
	assert.deepEqual([...extractPullRequestNumbers(content)], [42, 99]);
});

test("matches pull request product-area terms as complete words", () => {
	assert.equal(productArea(pullRequest({ title: "feat(pr): improve review flow" })), "Pull requests");
	assert.equal(
		productArea(pullRequest({ title: "perf: improve sidebar performance" })),
		"Desktop",
	);
});

test("renders a reviewable weekly MDX draft", () => {
	const result = renderWeeklyDraft({
		pullRequests: [
			pullRequest(),
			pullRequest({ number: 43, title: "fix(files): render large diffs", url: "https://github.com/Untrivial-ai/agent-orchestrator/pull/43" }),
		],
		startDate: "2026-09-14",
		endDate: "2026-09-20",
	});
	assert.ok(result);
	assert.match(result.content, /rangeStart: "2026-09-14"/);
	assert.match(result.content, /<PRBadge url=".*\/pull\/42" \/>/);
	assert.match(result.content, /## Bug fixes/);
	assert.match(result.content, /### Product/);
	assert.match(result.content, /View detailed releases on GitHub/);
	assert.equal(result.content.match(/^---$/gm)?.length, 2);
	assert.equal(result.counts.included, 2);
});

test("never publishes raw commit references", () => {
	const result = renderHistoricalWeek({
		changes: [
			pullRequest({
				number: undefined,
				sha: "1234567890abcdef",
				url: "https://github.com/Untrivial-ai/agent-orchestrator/commit/1234567890abcdef",
			}),
		],
		startDate: "2026-09-14",
		endDate: "2026-09-20",
		totalCommits: 1,
	});
	assert.doesNotMatch(result.content, /1234567|\/commit\//);
	assert.match(result.content, /no separately announced user-facing updates/i);
});

test("credits contributors only when explicitly marked", () => {
	const regular = renderWeeklyDraft({
		pullRequests: [pullRequest({ author: "great-contributor" })],
		startDate: "2026-09-14",
		endDate: "2026-09-20",
	});
	assert.doesNotMatch(regular.content, /great-contributor/);

	const exceptional = renderWeeklyDraft({
		pullRequests: [
			pullRequest({ author: "great-contributor", labels: ["changelog:credit"] }),
		],
		startDate: "2026-09-14",
		endDate: "2026-09-20",
	});
	assert.match(exceptional.content, /contributed by \[@great-contributor\]/);
});

test("feature summaries use factual PR context and reject an empty template", () => {
	assert.equal(
		pullRequestSummary(
			pullRequest({
				body: "## Summary\n\nUsers can now open links in the in-app browser without leaving their session.\n\n## Testing\n\nCovered.",
			}),
		),
		"Users can now open links in the in-app browser without leaving their session.",
	);
	assert.equal(
		pullRequestSummary(
			pullRequest({ body: "## What\n\nBrief description of the change." }),
		),
		"Keep queued messages after restart.",
	);
});

test("renders historical weeks without an editorial checklist", () => {
	const result = renderHistoricalWeek({
		changes: [pullRequest()],
		startDate: "2026-09-14",
		endDate: "2026-09-20",
		totalCommits: 3,
	});
	assert.match(result.content, /historical: true/);
	assert.match(result.content, /## Features/);
	assert.match(result.content, /## Learn more/);
	assert.match(result.content, /Read the documentation/);
	assert.doesNotMatch(result.content, /Review before merge/);
});

test("recollects the complete week when an existing draft is regenerated", () => {
	const entries = [
		{
			file: "2026-09-13-weekly-update.mdx",
			raw: 'rangeEnd: "2026-09-13"\n<PRBadge url="https://github.com/Untrivial-ai/agent-orchestrator/pull/41" />',
			data: { rangeEnd: "2026-09-13" },
		},
		{
			file: "2026-09-20-weekly-update.mdx",
			raw: 'rangeEnd: "2026-09-20"\n<PRBadge url="https://github.com/Untrivial-ai/agent-orchestrator/pull/42" />',
			data: { rangeEnd: "2026-09-20" },
		},
	];
	const selected = selectDraftInputs({
		entries,
		entryFile: "2026-09-20-weekly-update.mdx",
		endDate: "2026-09-20",
		collected: [pullRequest(), pullRequest({ number: 43 })],
	});

	assert.equal(selected.startDate, "2026-09-14");
	assert.deepEqual(
		selected.pullRequests.map((item) => item.number),
		[42, 43],
	);
});

test("scheduled Monday runs publish the completed Sunday window", () => {
	assert.equal(
		resolveWeeklyDate({
			eventName: "schedule",
			now: new Date("2026-09-28T00:15:00Z"),
		}),
		"2026-09-27",
	);
});

test("manual weekly runs preserve the requested date", () => {
	assert.equal(
		resolveWeeklyDate({
			eventName: "workflow_dispatch",
			requestedDate: "2026-09-20",
			now: new Date("2026-09-28T00:15:00Z"),
		}),
		"2026-09-20",
	);
});

test("manual weekly runs without a date use the current UTC date", () => {
	assert.equal(
		resolveWeeklyDate({
			eventName: "workflow_dispatch",
			now: new Date("2026-09-28T23:45:00Z"),
		}),
		"2026-09-28",
	);
});
