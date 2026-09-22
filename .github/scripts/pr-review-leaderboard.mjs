const PAGE_SIZE = 100;

const SEARCH_QUERY = `
  query($searchQuery: String!, $cursor: String) {
    search(query: $searchQuery, type: ISSUE, first: ${PAGE_SIZE}, after: $cursor) {
      issueCount
      pageInfo { hasNextPage endCursor }
      nodes {
        ... on PullRequest {
          id
          number
          reviews(first: ${PAGE_SIZE}) {
            pageInfo { hasNextPage endCursor }
            nodes { author { __typename login } submittedAt }
          }
          comments(first: ${PAGE_SIZE}) {
            pageInfo { hasNextPage endCursor }
            nodes { author { __typename login } createdAt }
          }
        }
      }
    }
  }
`;

const CONNECTION_QUERY = `
  query($id: ID!, $reviewsCursor: String, $commentsCursor: String, $includeReviews: Boolean!, $includeComments: Boolean!) {
    node(id: $id) {
      ... on PullRequest {
        reviews(first: ${PAGE_SIZE}, after: $reviewsCursor) @include(if: $includeReviews) {
          pageInfo { hasNextPage endCursor }
          nodes { author { __typename login } submittedAt }
        }
        comments(first: ${PAGE_SIZE}, after: $commentsCursor) @include(if: $includeComments) {
          pageInfo { hasNextPage endCursor }
          nodes { author { __typename login } createdAt }
        }
      }
    }
  }
`;

const isBot = (author) =>
	author?.__typename === "Bot" || /\[bot\]$/i.test(author?.login ?? "");
const isWithin = (timestamp, start, end) => {
	const time = Date.parse(timestamp);
	return time >= Date.parse(start) && time < Date.parse(end);
};

export function calculateStats(pullRequests, { start, end }) {
	const stats = new Map();
	const entryFor = (login) => {
		if (!stats.has(login)) {
			stats.set(login, { login, comments: 0, reviews: 0, pullRequests: new Set() });
		}
		return stats.get(login);
	};

	for (const pullRequest of pullRequests) {
		for (const review of pullRequest.reviews) {
			const { author } = review;
			const login = author?.login;
			if (
				!login ||
				isBot(author) ||
				!isWithin(review.submittedAt, start, end)
			) {
				continue;
			}
			const entry = entryFor(login);
			entry.reviews += 1;
			entry.pullRequests.add(pullRequest.number);
		}

		for (const comment of pullRequest.comments) {
			const { author } = comment;
			const login = author?.login;
			if (!login || isBot(author) || !isWithin(comment.createdAt, start, end)) continue;
			entryFor(login).comments += 1;
		}
	}

	return [...stats.values()]
		.map(({ pullRequests: reviewed, ...entry }) => ({ ...entry, pullRequests: reviewed.size }))
		.sort((left, right) =>
			right.comments - left.comments ||
			right.reviews - left.reviews ||
			left.login.localeCompare(right.login),
		);
}

export function buildComment(stats, { start, end }) {
	const countLabel = (count, label) => `${count} ${label}${count === 1 ? "" : "s"}`;
	const rows = stats
		.map(
			(entry) =>
				`| ${entry.login} | ${countLabel(entry.reviews, "review")} (${countLabel(entry.pullRequests, "PR")}) | ${entry.comments} |`,
		)
		.join("\n");

	return [
		"## Pull review activity",
		"",
		`Activity from ${start} (inclusive) to ${end} (exclusive).`,
		"",
		"| User | Reviews | PR comments |",
		"| --- | ---: | ---: |",
		rows || "| _No review activity_ | 0 reviews (0 PRs) | 0 |",
		"",
		"Reviews are counted by submission time, including repeated review rounds on the same PR. PR comments are conversation comments created during the same window.",
	].join("\n");
}

async function remainingConnection(github, id, name, initial) {
	const nodes = [...initial.nodes];
	let pageInfo = initial.pageInfo;

	while (pageInfo.hasNextPage) {
		const includeReviews = name === "reviews";
		const response = await github.graphql(CONNECTION_QUERY, {
			id,
			reviewsCursor: includeReviews ? pageInfo.endCursor : null,
			commentsCursor: includeReviews ? null : pageInfo.endCursor,
			includeReviews,
			includeComments: !includeReviews,
		});
		const connection = response.node[name];
		nodes.push(...connection.nodes);
		pageInfo = connection.pageInfo;
	}

	return nodes;
}

export async function fetchPullRequests(github, { owner, repo, start }) {
	const searchQuery = `repo:${owner}/${repo} is:pr updated:>=${start.slice(0, 10)}`;
	const pullRequests = [];
	let cursor = null;
	let hasNextPage = true;

	while (hasNextPage) {
		const response = await github.graphql(SEARCH_QUERY, { searchQuery, cursor });
		if (response.search.issueCount > 1000) {
			throw new Error("The activity window contains more than GitHub Search's 1,000-PR limit");
		}

		for (const pullRequest of response.search.nodes) {
			if (!pullRequest?.id) continue;
			pullRequests.push({
				...pullRequest,
				reviews: await remainingConnection(github, pullRequest.id, "reviews", pullRequest.reviews),
				comments: await remainingConnection(github, pullRequest.id, "comments", pullRequest.comments),
			});
		}

		hasNextPage = response.search.pageInfo.hasNextPage;
		cursor = response.search.pageInfo.endCursor;
	}

	return pullRequests;
}

export async function run({ github, context, periodDays = 7, now = new Date() }) {
	const end = now.toISOString();
	const start = new Date(now.getTime() - periodDays * 24 * 60 * 60 * 1000).toISOString();
	const pullRequests = await fetchPullRequests(github, {
		owner: context.repo.owner,
		repo: context.repo.repo,
		start,
	});
	const stats = calculateStats(pullRequests, { start, end });
	const body = buildComment(stats, { start, end });

	await github.rest.issues.createComment({
		...context.repo,
		issue_number: context.issue.number,
		body,
	});
}
