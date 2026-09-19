#!/usr/bin/env node
/**
 * Draft a weekly changelog MDX entry from PRs merged to main since the last
 * weekly entry (or the last seven days if none exist yet).
 *
 *   node scripts/draft-weekly-changelog.mjs
 *   node scripts/draft-weekly-changelog.mjs --since 2026-09-11
 *   node scripts/draft-weekly-changelog.mjs --out frontend/src/landing/content/changelog/draft.mdx
 *   node scripts/draft-weekly-changelog.mjs --repo Untrivial-ai/agent-orchestrator
 *
 * Requires `gh` authenticated against the repo. The draft is a starting point:
 * curate Features into narrative sections, drop noise, and replace each
 * <MediaPlaceholder /> with a recorded GIF/screenshot before publishing.
 */

import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, "..");
const DEFAULT_CHANGELOG_DIR = join(
	REPO_ROOT,
	"frontend/src/landing/content/changelog",
);
const DEFAULT_REPO = "Untrivial-ai/agent-orchestrator";
const WEEKLY_SLUG = /^(\d{4}-\d{2}-\d{2})(?:-|$)/;

export function parseArgs(argv) {
	const args = {};
	for (let i = 0; i < argv.length; i++) {
		const token = argv[i];
		if (!token.startsWith("--")) continue;
		const key = token.slice(2);
		const next = argv[i + 1];
		if (next !== undefined && !next.startsWith("--")) {
			args[key] = next;
			i++;
		} else {
			args[key] = true;
		}
	}
	return args;
}

/** @param {string} title */
export function categorizePrTitle(title) {
	const t = title.trim();
	if (/^(feat)([\(:]|\b)/i.test(t)) return "features";
	if (/^(fix)([\(:]|\b)/i.test(t)) return "fixes";
	if (/^(perf|improv)([\(:]|\b)/i.test(t)) return "improvements";
	if (/^(docs|test|chore|ci|style|build)([\(:]|\b)/i.test(t)) return "skip";
	// Imperative titles without a conventional prefix: treat product verbs as
	// features, everything else as improvements (polish / behavior tweaks).
	if (
		/^(Add|Restore|Open|Reveal|Warn|Preserve|Introduce|Support|Enable|Launch)\b/.test(
			t,
		)
	) {
		return "features";
	}
	if (/^refactor([\(:]|\b)/i.test(t)) return "improvements";
	return "improvements";
}

/**
 * Parse weekly-entry metadata from an MDX file's frontmatter.
 * @param {string} filePath
 * @param {string} raw
 */
export function parseChangelogFrontmatter(filePath, raw) {
	const match = raw.match(/^---\r?\n([\s\S]*?)\r?\n---/);
	if (!match) return null;
	const block = match[1];
	const get = (key) => {
		const line = block.match(new RegExp(`^${key}:\\s*(.+)$`, "m"));
		if (!line) return undefined;
		return line[1].trim().replace(/^["']|["']$/g, "");
	};
	const slug = filePath.replace(/\\/g, "/").split("/").pop()?.replace(/\.mdx$/, "") ?? "";
	const kind = get("kind");
	const date = get("date");
	const title = get("title");
	const weekly =
		kind === "weekly" || WEEKLY_SLUG.test(slug);
	return { slug, kind, date, title, weekly };
}

/**
 * Find the most recent weekly changelog entry's date (YYYY-MM-DD).
 * @param {string} changelogDir
 * @param {object} [io]
 * @param {(dir: string) => string[]} [io.list]
 * @param {(path: string) => string} [io.read]
 */
export function findLastWeeklyEntryDate(
	changelogDir,
	io = {},
) {
	const list = io.list ?? ((dir) => (existsSync(dir) ? readdirSync(dir) : []));
	const read = io.read ?? ((p) => readFileSync(p, "utf8"));
	const names = list(changelogDir);
	const entries = [];
	for (const name of names) {
		if (!name.endsWith(".mdx")) continue;
		const full = join(changelogDir, name);
		const meta = parseChangelogFrontmatter(full, read(full));
		if (!meta?.weekly || !meta.date) continue;
		const day = meta.date.slice(0, 10);
		if (!/^\d{4}-\d{2}-\d{2}$/.test(day)) continue;
		entries.push({ day, slug: meta.slug, title: meta.title });
	}
	entries.sort((a, b) => b.day.localeCompare(a.day));
	return entries[0] ?? null;
}

/** @param {Date} now */
export function defaultSinceDate(now = new Date()) {
	const d = new Date(now);
	d.setUTCDate(d.getUTCDate() - 7);
	return d.toISOString().slice(0, 10);
}

/**
 * @param {object} opts
 * @param {string} opts.repo
 * @param {string} opts.since YYYY-MM-DD (inclusive lower bound on merged_at)
 * @param {(args: string[]) => string} [opts.runGh]
 */
export function fetchMergedPulls({ repo, since, runGh = runGhCli }) {
	const query = `repo:${repo} is:pr is:merged base:main merged:>=${since}`;
	const pulls = [];
	let cursor = null;
	for (;;) {
		const afterArg = cursor ? `, after: "${cursor}"` : "";
		const gql = `query {
  search(query: ${JSON.stringify(query)}, type: ISSUE, first: 100${afterArg}) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number
        title
        url
        mergedAt
        author { login }
      }
    }
  }
}`;
		const raw = runGh(["api", "graphql", "-f", `query=${gql}`]);
		const json = JSON.parse(raw);
		const search = json?.data?.search;
		if (!search) {
			throw new Error(`Unexpected GraphQL response: ${raw.slice(0, 400)}`);
		}
		for (const node of search.nodes ?? []) {
			if (!node?.number) continue;
			pulls.push({
				number: node.number,
				title: node.title,
				url: node.url,
				mergedAt: node.mergedAt,
				author: node.author?.login ?? "unknown",
			});
		}
		if (!search.pageInfo?.hasNextPage) break;
		cursor = search.pageInfo.endCursor;
	}
	const byNumber = new Map(pulls.map((p) => [p.number, p]));
	return [...byNumber.values()].sort(
		(a, b) => new Date(b.mergedAt) - new Date(a.mergedAt),
	);
}

function runGhCli(args) {
	return execFileSync("gh", args, {
		encoding: "utf8",
		maxBuffer: 16 * 1024 * 1024,
	});
}

/** @param {string} title */
export function stripConventionalPrefix(title) {
	return title
		.replace(
			/^(feat|fix|perf|improv|docs|test|chore|ci|refactor|style|build)(\([^)]*\))?:\s*/i,
			"",
		)
		.trim();
}

function mediaIdFromTitle(title) {
	return stripConventionalPrefix(title)
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-|-$/g, "")
		.slice(0, 48);
}

/**
 * @param {object} opts
 * @param {Array<{number:number,title:string,url:string,mergedAt:string,author:string}>} opts.pulls
 * @param {string} opts.since
 * @param {string} opts.untilDay YYYY-MM-DD for the entry date
 * @param {string} [opts.slug]
 */
export function renderWeeklyDraft({ pulls, since, untilDay, slug }) {
	const groups = {
		features: [],
		improvements: [],
		fixes: [],
		skip: [],
	};
	for (const pr of pulls) {
		groups[categorizePrTitle(pr.title)].push(pr);
	}

	const entrySlug = slug ?? `${untilDay}-weekly`;
	const title =
		groups.features[0] != null
			? sentenceCase(stripConventionalPrefix(groups.features[0].title))
			: `Week of ${untilDay}`;

	const authors = [...new Set(pulls.map((p) => p.author))].sort((a, b) =>
		a.localeCompare(b),
	);

	const lines = [];
	lines.push("---");
	lines.push(`title: ${JSON.stringify(title)}`);
	lines.push(
		`description: ${JSON.stringify(
			`Weekly changelog covering ${pulls.length} merged PRs on main since ${since}.`,
		)}`,
	);
	lines.push(`date: ${JSON.stringify(untilDay)}`);
	lines.push(`kind: weekly`);
	lines.push(`draft: true`);
	lines.push("---");
	lines.push("");
	lines.push(
		`> Draft generated by \`scripts/draft-weekly-changelog.mjs\` for merges since **${since}**. Curate Features into narrative sections, trim noise from Improvements/Fixes, replace every \`<MediaPlaceholder />\` with a recorded GIF or screenshot, set \`draft: false\`, and open the publish PR.`,
	);
	lines.push("");
	lines.push(
		`${pulls.length} pull requests merged to \`main\` · ${authors.length} contributors`,
	);
	lines.push("");
	lines.push("---");
	lines.push("");
	lines.push("## Features");
	lines.push("");

	if (groups.features.length === 0) {
		lines.push("_No feature-tagged merges in this window._");
		lines.push("");
	} else {
		for (const pr of groups.features) {
			const heading = sentenceCase(stripConventionalPrefix(pr.title));
			const id = mediaIdFromTitle(pr.title) || `pr-${pr.number}`;
			lines.push(`### ${heading} [#${pr.number}](${pr.url})`);
			lines.push("");
			lines.push(
				`${sentenceCase(stripConventionalPrefix(pr.title))}. Contributed by @${pr.author}.`,
			);
			lines.push("");
			lines.push(`<MediaPlaceholder`);
			lines.push(`  id="${id}"`);
			lines.push(`  kind="gif"`);
			lines.push(`  caption=${JSON.stringify(heading)}`);
			lines.push(
				`  note=${JSON.stringify(
					`RECORD: demo the user-visible change from PR #${pr.number}, then replace this placeholder with ![${heading}](/changelog/${entrySlug}/${id}.gif) or <Video src="/changelog/${entrySlug}/${id}.mp4" title="${heading}" />`,
				)}`,
			);
			lines.push(`/>`);
			lines.push("");
		}
	}

	lines.push("## Improvements");
	lines.push("");
	appendBulletGroup(lines, groups.improvements);
	lines.push("## Fixes");
	lines.push("");
	appendBulletGroup(lines, groups.fixes);

	if (groups.skip.length > 0) {
		lines.push("## Skipped in draft (docs/test/chore/ci)");
		lines.push("");
		appendBulletGroup(lines, groups.skip);
	}

	lines.push("---");
	lines.push("");
	lines.push("## Contributors");
	lines.push("");
	lines.push(
		authors.map((a) => `@${a}`).join(", ") + ".",
	);
	lines.push("");

	return {
		markdown: lines.join("\n"),
		slug: entrySlug,
		counts: {
			features: groups.features.length,
			improvements: groups.improvements.length,
			fixes: groups.fixes.length,
			skip: groups.skip.length,
			total: pulls.length,
		},
		authors,
	};
}

function appendBulletGroup(lines, pulls) {
	if (pulls.length === 0) {
		lines.push("_None in this window._");
		lines.push("");
		return;
	}
	for (const pr of pulls) {
		const summary = sentenceCase(stripConventionalPrefix(pr.title));
		lines.push(
			`- ${summary} [#${pr.number}](${pr.url}) (@${pr.author})`,
		);
	}
	lines.push("");
}

function sentenceCase(text) {
	if (!text) return text;
	return text.charAt(0).toUpperCase() + text.slice(1);
}

function main(argv = process.argv.slice(2)) {
	const args = parseArgs(argv);
	if (args.help || args.h) {
		console.log(`Usage: node scripts/draft-weekly-changelog.mjs [options]

Options:
  --since YYYY-MM-DD   Inclusive lower bound on merged_at (default: last weekly entry, else 7 days ago)
  --until YYYY-MM-DD   Entry date / upper label (default: today UTC)
  --out PATH           Write MDX to PATH instead of stdout
  --repo owner/name    GitHub repo (default: ${DEFAULT_REPO})
  --changelog-dir PATH Directory of existing changelog MDX entries
  --slug SLUG          Override output slug (default: <until>-weekly)
`);
		return 0;
	}

	const changelogDir = resolve(
		String(args["changelog-dir"] ?? DEFAULT_CHANGELOG_DIR),
	);
	const repo = String(args.repo ?? DEFAULT_REPO);
	const untilDay =
		typeof args.until === "string"
			? args.until
			: new Date().toISOString().slice(0, 10);

	let since = typeof args.since === "string" ? args.since : null;
	if (!since) {
		const last = findLastWeeklyEntryDate(changelogDir);
		if (last) {
			// Exclusive of the previous entry day: start the day after.
			const next = new Date(`${last.day}T00:00:00Z`);
			next.setUTCDate(next.getUTCDate() + 1);
			since = next.toISOString().slice(0, 10);
			console.error(
				`Using since=${since} (day after last weekly entry ${last.slug} / ${last.day})`,
			);
		} else {
			since = defaultSinceDate();
			console.error(`No weekly entry found; using since=${since} (7 days ago)`);
		}
	}

	const pulls = fetchMergedPulls({ repo, since });
	const draft = renderWeeklyDraft({
		pulls,
		since,
		untilDay,
		slug: typeof args.slug === "string" ? args.slug : undefined,
	});

	console.error(
		`Drafted ${draft.counts.total} PRs → features=${draft.counts.features} improvements=${draft.counts.improvements} fixes=${draft.counts.fixes} skip=${draft.counts.skip}`,
	);

	if (typeof args.out === "string") {
		const outPath = resolve(args.out);
		mkdirSync(dirname(outPath), { recursive: true });
		writeFileSync(outPath, draft.markdown);
		console.error(`Wrote ${outPath}`);
	} else {
		process.stdout.write(draft.markdown);
	}
	return 0;
}

const isMain =
	process.argv[1] != null &&
	fileURLToPath(import.meta.url) === resolve(process.argv[1]);

if (isMain) {
	try {
		process.exitCode = main();
	} catch (err) {
		console.error(err instanceof Error ? err.message : err);
		process.exitCode = 1;
	}
}
