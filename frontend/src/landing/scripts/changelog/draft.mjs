import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import matter from "gray-matter";
import {
	extractPullRequestNumbers,
	renderPullRequestBody,
	renderWeeklyDraft,
} from "./changelog-core.mjs";

const changelogDirectory = path.join(process.cwd(), "content/changelog");
const repository = process.env.GITHUB_REPOSITORY ?? "Untrivial-ai/agent-orchestrator";

function requestedDate() {
	const dateArgument = process.argv.find((argument) => argument.startsWith("--date="));
	return dateArgument?.slice("--date=".length) ?? new Date().toISOString().slice(0, 10);
}

function getExistingEntries() {
	if (!fs.existsSync(changelogDirectory)) return [];
	return fs
		.readdirSync(changelogDirectory)
		.filter((file) => file.endsWith(".mdx"))
		.map((file) => {
			const filePath = path.join(changelogDirectory, file);
			const raw = fs.readFileSync(filePath, "utf8");
			const parsed = matter(raw);
			return { file, raw, data: parsed.data };
		});
}

function previousBoundary(entries, endDate) {
	const dateOnly = (value) => {
		if (value instanceof Date) return value.toISOString().slice(0, 10);
		return String(value ?? "").slice(0, 10);
	};
	const candidates = entries
		.map((entry) => dateOnly(entry.data.rangeEnd ?? entry.data.date))
		.filter((date) => /^\d{4}-\d{2}-\d{2}$/.test(date) && date < endDate)
		.sort();
	if (candidates.length > 0) {
		const nextDate = new Date(`${candidates.at(-1)}T00:00:00Z`);
		nextDate.setUTCDate(nextDate.getUTCDate() + 1);
		return nextDate.toISOString().slice(0, 10);
	}

	const fallback = new Date(`${endDate}T00:00:00Z`);
	fallback.setUTCDate(fallback.getUTCDate() - 7);
	return fallback.toISOString().slice(0, 10);
}

async function githubGraphql(query, variables) {
	const token = process.env.GITHUB_TOKEN;
	if (!token) throw new Error("GITHUB_TOKEN is required to collect merged pull requests");

	const response = await fetch("https://api.github.com/graphql", {
		method: "POST",
		headers: {
			Accept: "application/vnd.github+json",
			Authorization: `Bearer ${token}`,
			"Content-Type": "application/json",
			"User-Agent": "agent-orchestrator-changelog",
		},
		body: JSON.stringify({ query, variables }),
	});
	if (!response.ok) {
		throw new Error(`GitHub GraphQL request failed with ${response.status}`);
	}
	const payload = await response.json();
	if (payload.errors?.length) {
		throw new Error(payload.errors.map((error) => error.message).join("; "));
	}
	return payload.data;
}

async function collectMergedPullRequests(startDate, endDate) {
	const query = `
		query WeeklyChangelog($query: String!, $cursor: String) {
			search(type: ISSUE, query: $query, first: 100, after: $cursor) {
				pageInfo { hasNextPage endCursor }
				nodes {
					... on PullRequest {
						number
						title
						url
						mergedAt
						body
						additions
						deletions
						changedFiles
						author { login }
						labels(first: 30) { nodes { name } }
					}
				}
			}
		}
	`;
	const searchQuery = `repo:${repository} is:pr is:merged merged:${startDate}..${endDate}`;
	const pullRequests = [];
	let cursor = null;

	do {
		const data = await githubGraphql(query, { query: searchQuery, cursor });
		for (const node of data.search.nodes) {
			if (!node?.mergedAt) continue;
			pullRequests.push({
				number: node.number,
				title: node.title,
				url: node.url,
				mergedAt: node.mergedAt,
				body: node.body,
				additions: node.additions,
				deletions: node.deletions,
				changedFiles: node.changedFiles,
				author: node.author?.login,
				labels: node.labels.nodes.map((label) => label.name),
			});
		}
		cursor = data.search.pageInfo.hasNextPage
			? data.search.pageInfo.endCursor
			: null;
	} while (cursor);

	return pullRequests.sort((a, b) => a.mergedAt.localeCompare(b.mergedAt));
}

function setOutput(name, value) {
	if (process.env.GITHUB_OUTPUT) {
		fs.appendFileSync(process.env.GITHUB_OUTPUT, `${name}=${value}\n`);
	}
}

export function selectDraftInputs({ entries, entryFile, endDate, collected }) {
	const previousEntries = entries.filter((entry) => entry.file !== entryFile);
	const startDate = previousBoundary(previousEntries, endDate);
	const previouslyReferenced = new Set(
		previousEntries.flatMap((entry) => [...extractPullRequestNumbers(entry.raw)]),
	);
	return {
		startDate,
		pullRequests: collected.filter(
			(pullRequest) => !previouslyReferenced.has(pullRequest.number),
		),
	};
}

async function main() {
	const endDate = requestedDate();
	if (!/^\d{4}-\d{2}-\d{2}$/.test(endDate)) {
		throw new Error(`Invalid --date value: ${endDate}`);
	}

	const entryFile = `${endDate}-weekly-update.mdx`;
	const entryPath = path.join(changelogDirectory, entryFile);
	const entries = getExistingEntries();
	const entriesBeforeDraft = entries.filter((entry) => entry.file !== entryFile);
	const startDate = previousBoundary(entriesBeforeDraft, endDate);
	const collected = await collectMergedPullRequests(startDate, endDate);
	const { pullRequests: newPullRequests } = selectDraftInputs({
		entries,
		entryFile,
		endDate,
		collected,
	});
	const draft = renderWeeklyDraft({
		pullRequests: newPullRequests,
		startDate,
		endDate,
	});

	if (!draft) {
		console.log("No user-facing merged pull requests were found for this window.");
		setOutput("has_changes", "false");
		return;
	}
	const existingContent = fs.existsSync(entryPath)
		? fs.readFileSync(entryPath, "utf8")
		: undefined;
	if (existingContent === draft.content) {
		console.log(`Weekly changelog at ${entryPath} is already up to date.`);
		setOutput("has_changes", "false");
		return;
	}

	fs.mkdirSync(changelogDirectory, { recursive: true });
	fs.writeFileSync(entryPath, draft.content);
	const relativeEntryPath = path.relative(process.cwd(), entryPath).replaceAll("\\", "/");
	const pullRequestBody = renderPullRequestBody({
		counts: draft.counts,
		startDate,
		endDate,
		entryPath: relativeEntryPath,
	});
	const bodyPath = path.join(process.env.RUNNER_TEMP ?? os.tmpdir(), "changelog-pr-body.md");
	fs.writeFileSync(bodyPath, pullRequestBody);

	setOutput("has_changes", "true");
	setOutput("date", endDate);
	setOutput("entry_path", relativeEntryPath);
	setOutput("body_path", bodyPath);
	console.log(
		`${existingContent === undefined ? "Created" : "Updated"} ${relativeEntryPath} from ${newPullRequests.length} merged pull requests.`,
	);
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
	await main();
}
