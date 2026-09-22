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
				author: { __typename: "User", login: "author", avatarUrl: "https://avatars.example/author" },
				reviews: [
					{
						author: { __typename: "User", login: "reviewer", avatarUrl: "https://avatars.example/reviewer" },
						submittedAt: "2026-09-16T00:00:00.000Z",
					},
					{
						author: { __typename: "User", login: "reviewer", avatarUrl: "https://avatars.example/reviewer" },
						submittedAt: "2026-09-17T00:00:00.000Z",
					},
				],
				comments: [
					{
						author: { __typename: "User", login: "reviewer", avatarUrl: "https://avatars.example/reviewer" },
						createdAt: "2026-09-18T00:00:00.000Z",
					},
				],
			},
			{
				number: 11,
				author: { __typename: "User", login: "another-author", avatarUrl: "https://avatars.example/another" },
				reviews: [
					{
						author: { __typename: "User", login: "reviewer", avatarUrl: "https://avatars.example/reviewer" },
						submittedAt: "2026-09-19T00:00:00.000Z",
					},
				],
				comments: [],
			},
		],
		window,
	);

	assert.deepEqual(stats, [
		{
			login: "reviewer",
			avatarUrl: "https://avatars.example/reviewer",
			comments: 1,
			reviews: 3,
			pullRequests: 2,
		},
	]);
});

test("excludes self activity and bots while preserving the time boundaries", () => {
	const stats = calculateStats(
		[
			{
				number: 20,
				author: { __typename: "User", login: "author", avatarUrl: "https://avatars.example/author" },
				reviews: [
					{
						author: { __typename: "User", login: "author", avatarUrl: "https://avatars.example/author" },
						submittedAt: "2026-09-16T00:00:00.000Z",
					},
					{
						author: { __typename: "User", login: "i-trytoohard", avatarUrl: "https://avatars.example/automation" },
						submittedAt: "2026-09-16T00:00:00.000Z",
					},
					{
						author: { __typename: "Bot", login: "robot[bot]", avatarUrl: "https://avatars.example/robot" },
						submittedAt: "2026-09-16T00:00:00.000Z",
					},
					{
						author: { __typename: "Bot", login: "github-actions", avatarUrl: "https://avatars.example/actions" },
						submittedAt: "2026-09-16T00:00:00.000Z",
					},
					{
						author: { __typename: "User", login: "early", avatarUrl: "https://avatars.example/early" },
						submittedAt: window.start,
					},
					{
						author: { __typename: "User", login: "late", avatarUrl: "https://avatars.example/late" },
						submittedAt: window.end,
					},
				],
				comments: [
					{
						author: { __typename: "User", login: "author", avatarUrl: "https://avatars.example/author" },
						createdAt: "2026-09-16T00:00:00.000Z",
					},
					{
						author: { __typename: "User", login: "i-trytoohard", avatarUrl: "https://avatars.example/automation" },
						createdAt: "2026-09-16T00:00:00.000Z",
					},
					{
						author: { __typename: "Bot", login: "robot[bot]", avatarUrl: "https://avatars.example/robot" },
						createdAt: "2026-09-16T00:00:00.000Z",
					},
					{
						author: { __typename: "Bot", login: "github-actions", avatarUrl: "https://avatars.example/actions" },
						createdAt: "2026-09-16T00:00:00.000Z",
					},
				],
			},
		],
		window,
	);

	assert.deepEqual(stats, [
		{
			login: "early",
			avatarUrl: "https://avatars.example/early",
			comments: 0,
			reviews: 1,
			pullRequests: 1,
		},
	]);
});

test("removes comment-only contributors and ranks by distinct PRs reviewed", () => {
	const stats = calculateStats(
		[
			{
				number: 30,
				author: { __typename: "User", login: "author", avatarUrl: "https://avatars.example/author" },
				reviews: [
					{ author: { __typename: "User", login: "many-rounds", avatarUrl: "https://avatars.example/many" }, submittedAt: window.start },
					{ author: { __typename: "User", login: "many-rounds", avatarUrl: "https://avatars.example/many" }, submittedAt: window.start },
					{ author: { __typename: "User", login: "wide-reviewer", avatarUrl: "https://avatars.example/wide" }, submittedAt: window.start },
				],
				comments: [
					{ author: { __typename: "User", login: "commenter", avatarUrl: "https://avatars.example/commenter" }, createdAt: window.start },
				],
			},
			{
				number: 31,
				author: { __typename: "User", login: "another-author", avatarUrl: "https://avatars.example/another" },
				reviews: [
					{ author: { __typename: "User", login: "wide-reviewer", avatarUrl: "https://avatars.example/wide" }, submittedAt: window.start },
				],
				comments: [],
			},
		],
		window,
	);

	assert.deepEqual(stats.map(({ login }) => login), ["wide-reviewer", "many-rounds"]);
});

test("renders linked avatars, readable dates, medals, and a collapsed remainder", () => {
	const reviewers = Array.from({ length: 12 }, (_, index) => ({
		login: `reviewer-${index + 1}`,
		avatarUrl: `https://avatars.example/${index + 1}`,
		reviews: 12 - index,
		pullRequests: 12 - index,
		comments: index,
	}));
	const comment = buildComment(
		reviewers,
		window,
	);

	assert.match(comment, /^## 🏆 Review leaderboard/m);
	assert.match(comment, /Sep 15–22, 2026 · UTC/);
	assert.match(comment, /🥇 \| <img src="https:\/\/avatars\.example\/1" width="24" height="24" alt="@reviewer-1"> \[@reviewer-1\]\(https:\/\/github\.com\/reviewer-1\)/);
	assert.match(comment, /\| PRs reviewed \| Review rounds \| PR comments \|/);
	assert.match(comment, /<summary>Show 2 more reviewers<\/summary>/);
	assert.ok(comment.indexOf("@reviewer-10") < comment.indexOf("<details>"));
	assert.ok(comment.indexOf("@reviewer-11") > comment.indexOf("<details>"));
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
			// @octokit/graphql reserves these names and throws when they are
			// passed as variables.
			for (const reserved of ["query", "method", "url"]) {
				assert.ok(!(reserved in variables), `"${reserved}" cannot be used as a GraphQL variable name`);
			}
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
