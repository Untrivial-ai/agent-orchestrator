import assert from "node:assert/strict";
import test from "node:test";

import {
	buildComment,
	calculateStats,
	fetchPullRequests,
} from "./pr-review-leaderboard.mjs";

const window = {
	start: "2026-09-15T00:00:00.000Z",
	end: "2026-09-22T00:00:00.000Z",
};

test("counts review submissions, distinct PRs, and comments in the activity window", () => {
	const stats = calculateStats(
		[
			{
				number: 10,
				author: { login: "author" },
				reviews: [
					{ author: { login: "reviewer" }, submittedAt: "2026-09-16T00:00:00.000Z" },
					{ author: { login: "reviewer" }, submittedAt: "2026-09-17T00:00:00.000Z" },
				],
				comments: [
					{ author: { login: "reviewer" }, createdAt: "2026-09-18T00:00:00.000Z" },
				],
			},
			{
				number: 11,
				author: { login: "another-author" },
				reviews: [
					{ author: { login: "reviewer" }, submittedAt: "2026-09-19T00:00:00.000Z" },
				],
				comments: [],
			},
		],
		window,
	);

	assert.deepEqual(stats, [
		{ login: "reviewer", comments: 1, reviews: 3, pullRequests: 2 },
	]);
});

test("uses activity timestamps, includes self-reviews, and excludes bots", () => {
	const stats = calculateStats(
		[
			{
				number: 20,
				author: { login: "author" },
				reviews: [
					{ author: { login: "author" }, submittedAt: "2026-09-16T00:00:00.000Z" },
					{ author: { login: "robot[bot]" }, submittedAt: "2026-09-16T00:00:00.000Z" },
					{
						author: { __typename: "Bot", login: "github-actions" },
						submittedAt: "2026-09-16T00:00:00.000Z",
					},
					{ author: { login: "early" }, submittedAt: window.start },
					{ author: { login: "late" }, submittedAt: window.end },
				],
				comments: [
					{ author: { login: "author" }, createdAt: "2026-09-16T00:00:00.000Z" },
					{ author: { login: "robot[bot]" }, createdAt: "2026-09-16T00:00:00.000Z" },
					{
						author: { __typename: "Bot", login: "github-actions" },
						createdAt: "2026-09-16T00:00:00.000Z",
					},
				],
			},
		],
		window,
	);

	assert.deepEqual(stats, [
		{ login: "author", comments: 1, reviews: 1, pullRequests: 1 },
		{ login: "early", comments: 0, reviews: 1, pullRequests: 1 },
	]);
});

test("combines review submissions and distinct PRs in one column", () => {
	const comment = buildComment(
		[
			{ login: "reviewer", reviews: 31, pullRequests: 18, comments: 37 },
			{ login: "another-reviewer", reviews: 1, pullRequests: 1, comments: 0 },
		],
		window,
	);

	assert.match(comment, /User \| Reviews \| PR comments/);
	assert.match(comment, /reviewer \| 31 reviews \(18 PRs\) \| 37/);
	assert.match(comment, /another-reviewer \| 1 review \(1 PR\) \| 0/);
	assert.match(comment, /2026-09-15T00:00:00\.000Z/);
});

test("paginates search results and oversized PR connections", async () => {
	const responses = [
		{
			search: {
				issueCount: 2,
				pageInfo: { hasNextPage: true, endCursor: "search-2" },
				nodes: [
					{
						id: "pr-1",
						number: 1,
						author: { login: "author" },
						reviews: {
							pageInfo: { hasNextPage: true, endCursor: "reviews-2" },
							nodes: [{ author: { login: "one" }, submittedAt: window.start }],
						},
						comments: {
							pageInfo: { hasNextPage: false, endCursor: null },
							nodes: [],
						},
					},
				],
			},
		},
		{
			node: {
				reviews: {
					pageInfo: { hasNextPage: false, endCursor: null },
					nodes: [{ author: { login: "two" }, submittedAt: window.start }],
				},
			},
		},
		{
			search: {
				issueCount: 2,
				pageInfo: { hasNextPage: false, endCursor: null },
				nodes: [],
			},
		},
	];
	const calls = [];
	const github = {
		graphql: async (_query, variables) => {
			calls.push(variables);
			return responses.shift();
		},
	};

	const pullRequests = await fetchPullRequests(github, {
		owner: "owner",
		repo: "repo",
		start: window.start,
	});

	assert.equal(pullRequests[0].reviews.length, 2);
	assert.equal(calls[1].includeReviews, true);
	assert.equal(calls[2].cursor, "search-2");
});
