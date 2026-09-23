const PAGE_SIZE = 100;
const BOT_LOGINS = new Set(["i-trytoohard"]);

const SEARCH_QUERY = `
  query($searchQuery: String!, $cursor: String) {
    search(query: $searchQuery, type: ISSUE, first: ${PAGE_SIZE}, after: $cursor) {
      issueCount
      pageInfo { hasNextPage endCursor }
      nodes {
        ... on PullRequest {
          id
          number
          author { login }
          reviews(first: ${PAGE_SIZE}) {
            pageInfo { hasNextPage endCursor }
            nodes { author { __typename login avatarUrl } submittedAt }
          }
          comments(first: ${PAGE_SIZE}) {
            pageInfo { hasNextPage endCursor }
            nodes { author { __typename login avatarUrl } createdAt }
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
          nodes { author { __typename login avatarUrl } submittedAt }
        }
        comments(first: ${PAGE_SIZE}, after: $commentsCursor) @include(if: $includeComments) {
          pageInfo { hasNextPage endCursor }
          nodes { author { __typename login avatarUrl } createdAt }
        }
      }
    }
  }
`;

const isBot = (author) =>
	author?.__typename === "Bot" ||
	/\[bot\]$/i.test(author?.login ?? "") ||
	BOT_LOGINS.has(author?.login?.toLowerCase());
const isWithin = (timestamp, start, end) => {
	const time = Date.parse(timestamp);
	return time >= Date.parse(start) && time < Date.parse(end);
};

export function calculateStats(pullRequests, { start, end }) {
	const stats = new Map();
	const entryFor = (author) => {
		const { login, avatarUrl } = author;
		if (!stats.has(login)) {
			stats.set(login, {
				login,
				avatarUrl,
				comments: 0,
				reviews: 0,
				pullRequests: new Set(),
			});
		} else if (!stats.get(login).avatarUrl && avatarUrl) {
			stats.get(login).avatarUrl = avatarUrl;
		}
		return stats.get(login);
	};

	for (const pullRequest of pullRequests) {
		const authorLogin = pullRequest.author?.login?.toLowerCase();
		for (const review of pullRequest.reviews) {
			const { author } = review;
			const login = author?.login;
			if (
				!login ||
				isBot(author) ||
				login.toLowerCase() === authorLogin ||
				!isWithin(review.submittedAt, start, end)
			) {
				continue;
			}
			const entry = entryFor(author);
			entry.reviews += 1;
			entry.pullRequests.add(pullRequest.number);
		}

		for (const comment of pullRequest.comments) {
			const { author } = comment;
			const login = author?.login;
			if (
				!login ||
				isBot(author) ||
				login.toLowerCase() === authorLogin ||
				!isWithin(comment.createdAt, start, end)
			) {
				continue;
			}
			entryFor(author).comments += 1;
		}
	}

	return [...stats.values()]
		.map(({ pullRequests: reviewed, ...entry }) => ({ ...entry, pullRequests: reviewed.size }))
		.filter((entry) => entry.reviews > 0)
		.sort((left, right) =>
			right.pullRequests - left.pullRequests ||
			right.reviews - left.reviews ||
			right.comments - left.comments ||
			left.login.localeCompare(right.login),
		);
}

export function buildComment(stats, { start, end }) {
	const dateRange = formatDateRange(start, end);
	const visible = stats.slice(0, 10);
	const remaining = stats.slice(10);
	const table = (entries, rankOffset = 0) => [
		"| Rank | Reviewer | PRs reviewed | Review rounds | PR comments |",
		"| :---: | --- | ---: | ---: | ---: |",
		...(entries.length > 0
			? entries.map((entry, index) => formatRow(entry, rankOffset + index + 1))
			: ["| — | _No external review activity_ | 0 | 0 | 0 |"]),
	].join("\n");

	const sections = [
		"## 🏆 Review leaderboard",
		"",
		dateRange,
		"",
		table(visible),
		"",
		"Ranked by distinct external PRs reviewed, then review rounds, then PR comments. Self-activity and bot activity are excluded.",
	];

	if (remaining.length > 0) {
		sections.push(
			"",
			"<details>",
			`<summary>Show ${remaining.length} more reviewers</summary>`,
			"",
			table(remaining, visible.length),
			"",
			"</details>",
		);
	}

	return sections.join("\n");
}

function formatDateRange(start, end) {
	const startDate = new Date(start);
	const endDate = new Date(end);
	const month = new Intl.DateTimeFormat("en-US", { month: "short", timeZone: "UTC" });
	const day = new Intl.DateTimeFormat("en-US", { day: "numeric", timeZone: "UTC" });
	const year = new Intl.DateTimeFormat("en-US", { year: "numeric", timeZone: "UTC" });
	const sameMonth =
		startDate.getUTCFullYear() === endDate.getUTCFullYear() &&
		startDate.getUTCMonth() === endDate.getUTCMonth();
	const range = sameMonth
		? `${month.format(startDate)} ${day.format(startDate)}–${day.format(endDate)}, ${year.format(endDate)}`
		: `${month.format(startDate)} ${day.format(startDate)}, ${year.format(startDate)}–${month.format(endDate)} ${day.format(endDate)}, ${year.format(endDate)}`;
	return `${range} · UTC`;
}

function formatRow(entry, rank) {
	const medal = ["🥇", "🥈", "🥉"][rank - 1] ?? rank;
	const profileUrl = `https://github.com/${entry.login}`;
	const avatar = entry.avatarUrl
		? `<img src="${entry.avatarUrl}" width="24" height="24" alt="@${entry.login}"> `
		: "";
	return `| ${medal} | ${avatar}[@${entry.login}](${profileUrl}) | ${entry.pullRequests} | ${entry.reviews} | ${entry.comments} |`;
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
