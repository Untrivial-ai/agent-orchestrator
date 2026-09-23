const INTERNAL_TYPES = new Set(["build", "chore", "ci", "docs", "refactor", "test"]);
const INTERNAL_SCOPES = new Set([
	"build",
	"ci",
	"deps",
	"docs",
	"landing",
	"release",
	"telemetry",
	"test",
	"website",
]);
const INTERNAL_TITLE =
	/\b(bugbot|gitleaks|lint(?:er|ing)?|typecheck|unit tests?|integration tests?|test utilities|test coverage|flaky tests?|release workflow|publish(?:ing)? pipeline|sentry|posthog|telemetry|instrument(?:ation)?)\b/i;
const MAX_HIGHLIGHTS = 4;
const MAX_IMPROVEMENTS = 12;
const MAX_FIXES = 12;
const DATE_FORMATTER = new Intl.DateTimeFormat("en-US", {
	month: "short",
	day: "numeric",
	year: "numeric",
	timeZone: "UTC",
});

function labelNames(pullRequest) {
	return new Set(
		(pullRequest.labels ?? []).map((label) =>
			(typeof label === "string" ? label : label.name).toLowerCase(),
		),
	);
}

function conventionalTitle(title) {
	const match = title.match(/^([a-z]+)(?:\(([^)]+)\))?!?:\s*(.+)$/i);
	return match
		? { type: match[1].toLowerCase(), scope: match[2]?.toLowerCase(), subject: match[3] }
		: { type: undefined, scope: undefined, subject: title };
}

export function classifyPullRequest(pullRequest) {
	const labels = labelNames(pullRequest);
	if (labels.has("changelog:skip")) return "skip";

	const include = labels.has("changelog:include");
	const { type, scope } = conventionalTitle(pullRequest.title);

	if (!include && type && INTERNAL_TYPES.has(type)) return "skip";
	if (!include && scope && INTERNAL_SCOPES.has(scope)) return "skip";
	if (!include && INTERNAL_TITLE.test(pullRequest.title)) return "skip";
	if (type === "fix" || labels.has("bug")) return "fix";
	if (type === "feat" || labels.has("feature")) return "feature";
	if (type === "perf" || labels.has("enhancement") || include) return "improvement";

	return "skip";
}

export function cleanPullRequestTitle(title) {
	const { subject } = conventionalTitle(title);
	const cleaned = subject.replace(/\s*\(#\d+\)\s*$/, "").trim();
	return cleaned ? cleaned[0].toUpperCase() + cleaned.slice(1) : title;
}

export function escapeMdxText(value) {
	return value
		.replaceAll("&", "&amp;")
		.replaceAll("<", "&lt;")
		.replaceAll(">", "&gt;")
		.replaceAll("{", "&#123;")
		.replaceAll("}", "&#125;")
		.replaceAll("[", "\\[")
		.replaceAll("]", "\\]");
}

export function extractPullRequestNumbers(content) {
	const numbers = new Set();
	for (const match of content.matchAll(
		/github\.com\/[^/]+\/[^/]+\/pull\/(\d+)|(?:^|[\s(])#(\d+)/gm,
	)) {
		numbers.add(Number(match[1] ?? match[2]));
	}
	return numbers;
}

function formatDate(date) {
	return DATE_FORMATTER.format(new Date(`${date}T12:00:00Z`));
}

function badge(pullRequest) {
	if (pullRequest.number) return `<PRBadge url="${pullRequest.url}" />`;
	return "";
}

function contributorCredit(pullRequest) {
	const labels = labelNames(pullRequest);
	if (!labels.has("changelog:credit") || !pullRequest.author) return "";
	const author = escapeMdxText(pullRequest.author);
	return ` — contributed by [@${author}](https://github.com/${author})`;
}

export function productArea(pullRequest) {
	const { scope } = conventionalTitle(pullRequest.title);
	const value = `${scope ?? ""} ${pullRequest.title}`.toLowerCase();
	if (/mobile|ios|android/.test(value)) return "Mobile";
	if (/browser|preview/.test(value)) return "Browser";
	if (/terminal|pty|tui/.test(value)) return "Terminal";
	if (/chat|composer/.test(value)) return "Chat";
	if (/agent|harness|session/.test(value)) return "Agents";
	if (/\b(?:github|git|scm|pull|review|pr)\b/.test(value)) return "Pull requests";
	if (/release|update|updater/.test(value)) return "Updates";
	if (/cloud|account|auth/.test(value)) return "Cloud";
	if (/cli/.test(value)) return "CLI";
	if (/landing|site|docs/.test(value)) return "Website and docs";
	if (/desktop|renderer|frontend|ui|sidebar|settings/.test(value)) return "Desktop";
	return "Product";
}

function plainText(value) {
	return value
		.replace(/<!--.*?-->/gs, " ")
		.replace(/!\[[^\]]*\]\([^)]*\)/g, " ")
		.replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
		.replace(/[`*_~>|]/g, "")
		.replace(/^[-+]\s+/gm, "")
		.replace(/^\d+\.\s+/gm, "")
		.replace(/\s+/g, " ")
		.trim();
}

export function pullRequestSummary(pullRequest) {
	const body = pullRequest.body ?? "";
	const section = body.match(
		/(?:^|\n)#{1,3}\s+(?:summary|what(?: changed)?|feature|overview)\s*\n+([\s\S]*?)(?=\n#{1,3}\s|$)/i,
	)?.[1];
	const candidates = [section, body]
		.filter(Boolean)
		.map((value) =>
			value
				.split(/\r?\n/)
				.map((line) => line.trim())
				.filter(
					(line) =>
						line &&
						!/^#{1,6}\s/.test(line) &&
						!/^```/.test(line) &&
						!/^\|/.test(line) &&
						!/^[-*]\s*\[[ x]\]/i.test(line),
				)
				.slice(0, 2)
				.map(plainText)
				.join(" "),
		)
		.filter(
			(value) =>
				value.length >= 24 &&
				!/^brief description of the change/i.test(value) &&
				!/^problem this solves/i.test(value),
		);
	const fallback = cleanPullRequestTitle(pullRequest.title);
	const summary = candidates[0] ?? fallback;
	const sentenceEnd = summary.match(/^.{40,320}?[.!?](?:\s|$)/)?.[0]?.trim();
	const shortened = summary.length > 280 ? summary.slice(0, 280).replace(/\s+\S*$/, "") : summary;
	return sentenceEnd ?? `${shortened.replace(/[,:;\s]+$/, "")}.`;
}

export function pullRequestMedia(pullRequest) {
	const body = pullRequest.body ?? "";
	const markdown = body.match(
		/!\[([^\]]*)\]\((https:\/\/github\.com\/user-attachments\/assets\/[^)\s]+)\)/i,
	);
	if (markdown) {
		return {
			alt: plainText(markdown[1]) || cleanPullRequestTitle(pullRequest.title),
			url: markdown[2],
		};
	}
	const html = body.match(
		/<img\b[^>]*\balt=["']([^"']*)["'][^>]*\bsrc=["'](https:\/\/github\.com\/user-attachments\/assets\/[^"']+)["'][^>]*>/i,
	);
	if (html) {
		return {
			alt: plainText(html[1]) || cleanPullRequestTitle(pullRequest.title),
			url: html[2],
		};
	}
	return undefined;
}

function impactScore(pullRequest) {
	const labels = labelNames(pullRequest);
	if (labels.has("changelog:highlight")) return Number.MAX_SAFE_INTEGER;
	const title = pullRequest.title.toLowerCase();
	const size =
		Math.log10(1 + (pullRequest.additions ?? 0) + (pullRequest.deletions ?? 0)) * 12;
	const breadth = Math.min(pullRequest.changedFiles ?? 0, 80) / 4;
	const productBoost =
		/(mobile|chat|browser|terminal|dashboard|onboarding|session|agent|review|windows|gitlab|github|settings|workspace)/.test(
			title,
		)
			? 18
			: 0;
	const internalPenalty =
		/(telemetry|instrument|sentry|test|ci\b|refactor|migration|pipeline|release|docs|landing)/.test(
			title,
		)
			? 22
			: 0;
	return size + breadth + productBoost - internalPenalty;
}

function normalizedTitle(pullRequest) {
	return cleanPullRequestTitle(pullRequest.title)
		.toLowerCase()
		.replace(/#\d+/g, "")
		.replace(/[^a-z0-9]+/g, " ")
		.trim();
}

function deduplicatePullRequests(pullRequests) {
	const byTitle = new Map();
	for (const pullRequest of pullRequests) {
		const key = normalizedTitle(pullRequest);
		const previous = byTitle.get(key);
		if (!previous || (pullRequest.number ?? 0) > (previous.number ?? 0)) {
			byTitle.set(key, pullRequest);
		}
	}
	return [...byTitle.values()];
}

function featureSection(pullRequest) {
	const title = escapeMdxText(cleanPullRequestTitle(pullRequest.title));
	const summary = escapeMdxText(pullRequestSummary(pullRequest));
	const media = pullRequestMedia(pullRequest);
	return [
		`## ${title} ${badge(pullRequest)}${contributorCredit(pullRequest)}`,
		"",
		`${summary}`,
		...(media ? ["", `![${escapeMdxText(media.alt)}](${media.url})`] : []),
	].join("\n");
}

function bullet(pullRequest) {
	const title = escapeMdxText(cleanPullRequestTitle(pullRequest.title));
	return `- **${title}** ${badge(pullRequest)}${contributorCredit(pullRequest)}`.trimEnd();
}

function groupedBullets(pullRequests) {
	const groups = new Map();
	for (const pullRequest of pullRequests) {
		const area = productArea(pullRequest);
		const group = groups.get(area) ?? [];
		group.push(pullRequest);
		groups.set(area, group);
	}
	return [...groups].flatMap(([area, items]) => [
		`### ${area}`,
		"",
		...items.map(bullet),
		"",
	]);
}

function categorizedPullRequests(pullRequests) {
	return deduplicatePullRequests(pullRequests)
		.map((pullRequest) => ({
			...pullRequest,
			category: classifyPullRequest(pullRequest),
		}))
		.filter(
			(pullRequest) =>
				pullRequest.category !== "skip" && pullRequest.number && pullRequest.url,
		);
}

export function renderWeeklyDraft({ pullRequests, startDate, endDate }) {
	const categorized = categorizedPullRequests(pullRequests);

	if (categorized.length === 0) return null;

	const allFeatures = categorized
		.filter((pullRequest) => pullRequest.category === "feature")
		.sort((a, b) => impactScore(b) - impactScore(a));
	const highlights = allFeatures.slice(0, MAX_HIGHLIGHTS);
	const improvements = [
		...allFeatures.slice(MAX_HIGHLIGHTS),
		...categorized.filter((pullRequest) => pullRequest.category === "improvement"),
	].slice(0, MAX_IMPROVEMENTS);
	const fixes = categorized
		.filter((pullRequest) => pullRequest.category === "fix")
		.slice(0, MAX_FIXES);
	const title = `Weekly update — ${formatDate(startDate)} to ${formatDate(endDate)}`;
	const description = [
		highlights.length ? `${highlights.length} highlights` : null,
		improvements.length ? `${improvements.length} improvements` : null,
		fixes.length ? `${fixes.length} fixes` : null,
	]
		.filter(Boolean)
		.join(", ");

	const sections = [
		"---",
		`title: ${JSON.stringify(title)}`,
		`description: ${JSON.stringify(`What changed in Agent Orchestrator this week: ${description}.`)}`,
		`date: ${JSON.stringify(endDate)}`,
		`rangeStart: ${JSON.stringify(startDate)}`,
		`rangeEnd: ${JSON.stringify(endDate)}`,
		"---",
		"",
		"{/*",
		"Review before merge:",
		"- Replace the working title and generated highlight copy.",
		"- Confirm every item is available to users and not behind an internal flag.",
		"- Add a real product image for the strongest visual change when possible.",
		"- Keep contributor credit rare; use it only for an exceptional contribution explicitly labeled changelog:credit.",
		"- Add links to relevant documentation where they help users act on the update.",
		"- Remove this checklist after the editorial pass.",
		"*/}",
	];

	if (highlights.length > 0) {
		sections.push("", ...highlights.flatMap((pullRequest) => ["", featureSection(pullRequest)]));
	}

	if (improvements.length > 0) {
		sections.push("", "## Improvements", "", ...groupedBullets(improvements));
	}

	if (fixes.length > 0) {
		sections.push("", "## Bug fixes", "", ...groupedBullets(fixes));
	}

	sections.push(
		"",
		"## Learn more",
		"",
		"- [Read the documentation](/docs)",
		"- [View detailed releases on GitHub](https://github.com/Untrivial-ai/agent-orchestrator/releases)",
	);

	return {
		content: `${sections.join("\n").trim()}\n`,
		counts: {
			included: categorized.length,
			highlights: highlights.length,
			improvements: improvements.length,
			fixes: fixes.length,
			skipped: pullRequests.length - categorized.length,
		},
	};
}

export function renderHistoricalWeek({ changes, startDate, endDate, totalCommits }) {
	const categorized = categorizedPullRequests(changes);
	const features = categorized
		.filter((change) => change.category === "feature")
		.slice(0, MAX_HIGHLIGHTS);
	const improvements = categorized
		.filter((change) => change.category === "improvement")
		.slice(0, MAX_IMPROVEMENTS);
	const fixes = categorized
		.filter((change) => change.category === "fix")
		.slice(0, MAX_FIXES);
	const title = `Weekly update — ${formatDate(startDate)} to ${formatDate(endDate)}`;
	const summary = [
		features.length ? `${features.length} features` : null,
		improvements.length ? `${improvements.length} improvements` : null,
		fixes.length ? `${fixes.length} fixes` : null,
	]
		.filter(Boolean)
		.join(", ");
	const description = summary
		? `What changed in Agent Orchestrator this week: ${summary}.`
		: `A maintenance week with ${totalCommits} integrated ${totalCommits === 1 ? "change" : "changes"} and no separately announced user-facing updates.`;
	const sections = [
		"---",
		`title: ${JSON.stringify(title)}`,
		`description: ${JSON.stringify(description)}`,
		`date: ${JSON.stringify(endDate)}`,
		`rangeStart: ${JSON.stringify(startDate)}`,
		`rangeEnd: ${JSON.stringify(endDate)}`,
		"historical: true",
		"---",
	];

	if (features.length > 0) {
		sections.push("", "## Features", "", ...features.map(bullet));
	}
	if (improvements.length > 0) {
		sections.push("", "## Improvements", "", ...groupedBullets(improvements));
	}
	if (fixes.length > 0) {
		sections.push("", "## Bug fixes", "", ...groupedBullets(fixes));
	}
	if (categorized.length === 0) {
		sections.push(
			"",
			"## Maintenance",
			"",
			`This week contained ${totalCommits} integrated ${totalCommits === 1 ? "change" : "changes"}. There were no separately announced user-facing updates.`,
		);
	}
	sections.push(
		"",
		"## Learn more",
		"",
		"- [Read the documentation](/docs)",
		"- [View detailed releases on GitHub](https://github.com/Untrivial-ai/agent-orchestrator/releases)",
	);

	return {
		content: `${sections.join("\n").trim()}\n`,
		counts: {
			included: categorized.length,
			features: features.length,
			improvements: improvements.length,
			fixes: fixes.length,
			skipped: changes.length - categorized.length,
		},
	};
}

export function renderPullRequestBody({ counts, startDate, endDate, entryPath }) {
	return `## What and why

Prepare the weekly product update for ${startDate} through ${endDate}.

- ${counts.included} merged pull requests included
- ${counts.highlights} suggested highlights
- ${counts.improvements} additional improvements
- ${counts.fixes} fixes
- ${counts.skipped} internal or excluded changes skipped

Draft: \`${entryPath}\`

## Human review

- [ ] Confirm the included changes are available to users
- [ ] Remove internal, duplicated, reverted, or feature-flagged work
- [ ] Rewrite the title, summary, and highlight copy in plain language
- [ ] Add real screenshots or a short recording where they improve understanding
- [ ] Check every pull request reference
- [ ] Keep improvements and fixes grouped by product area
- [ ] Add relevant documentation and GitHub Releases links
- [ ] Credit a contributor only for an exceptional contribution explicitly labeled \`changelog:credit\`
- [ ] Review the desktop and mobile changelog preview
- [ ] Remove the editorial checklist from the MDX file

## Publishing

Merging this pull request publishes the update through the existing landing-site deployment. This workflow never merges or publishes directly.
`;
}
