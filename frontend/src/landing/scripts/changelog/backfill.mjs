import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import matter from "gray-matter";
import { renderHistoricalWeek } from "./changelog-core.mjs";

const landingDirectory = process.cwd();
const repositoryRoot = path.resolve(landingDirectory, "../../..");
const changelogDirectory = path.join(landingDirectory, "content/changelog");
const repository = process.env.GITHUB_REPOSITORY ?? "Untrivial-ai/agent-orchestrator";

function dateOnly(value) {
	if (value instanceof Date) return value.toISOString().slice(0, 10);
	return String(value ?? "").slice(0, 10);
}

function parseArgument(name) {
	const prefix = `--${name}=`;
	return process.argv.find((argument) => argument.startsWith(prefix))?.slice(prefix.length);
}

function assertDate(value, name) {
	if (!/^\d{4}-\d{2}-\d{2}$/.test(value ?? "")) {
		throw new Error(`Invalid --${name} value: ${value}`);
	}
}

function addDays(value, days) {
	const date = new Date(`${value}T00:00:00Z`);
	date.setUTCDate(date.getUTCDate() + days);
	return date.toISOString().slice(0, 10);
}

function sundayOnOrAfter(value) {
	const date = new Date(`${value}T00:00:00Z`);
	date.setUTCDate(date.getUTCDate() + ((7 - date.getUTCDay()) % 7));
	return date.toISOString().slice(0, 10);
}

function latestSunday() {
	const date = new Date();
	date.setUTCHours(0, 0, 0, 0);
	date.setUTCDate(date.getUTCDate() - date.getUTCDay());
	return date.toISOString().slice(0, 10);
}

function git(...arguments_) {
	return execFileSync("git", arguments_, {
		cwd: repositoryRoot,
		encoding: "utf8",
		maxBuffer: 64 * 1024 * 1024,
	});
}

function earliestCommitDate() {
	const dates = git("log", "HEAD", "--format=%aI")
		.split(/\r?\n/)
		.filter(Boolean)
		.map((value) => new Date(value))
		.filter((value) => !Number.isNaN(value.getTime()))
		.sort((a, b) => a - b);
	if (dates.length === 0) throw new Error("The repository has no commits to backfill");
	return dates[0].toISOString().slice(0, 10);
}

function existingCoverage() {
	if (!fs.existsSync(changelogDirectory)) return new Set();
	return new Set(
		fs
			.readdirSync(changelogDirectory)
			.filter((file) => file.endsWith(".mdx"))
			.map((file) => matter(fs.readFileSync(path.join(changelogDirectory, file), "utf8")).data)
			.map((data) => dateOnly(data.rangeEnd || sundayOnOrAfter(dateOnly(data.date))))
			.filter((date) => /^\d{4}-\d{2}-\d{2}$/.test(date)),
	);
}

function weeklyWindows(from, to) {
	const windows = [];
	let start = from;
	let end = sundayOnOrAfter(start);
	while (end <= to) {
		windows.push({ start, end });
		start = addDays(end, 1);
		end = addDays(start, 6);
	}
	return windows;
}

function pullRequestNumber(title, body) {
	const squash = title.match(/\(#(\d+)\)\s*$/);
	const merge = `${title}\n${body}`.match(/Merge pull request #(\d+)/i);
	return Number(squash?.[1] ?? merge?.[1]) || undefined;
}

function changesForWindow(startDate, endDate) {
	const output = git(
		"log",
		"HEAD",
		`--since=${startDate}T00:00:00Z`,
		`--until=${endDate}T23:59:59Z`,
		"--format=%H%x00%aI%x00%an%x00%s%x00%b%x1e",
	);
	const seen = new Set();
	const changes = [];

	for (const record of output.split("\x1e")) {
		const [sha, committedAt, author, title, body = ""] = record
			.replace(/^\r?\n/, "")
			.split("\x00");
		if (!sha || !title) continue;
		const number = pullRequestNumber(title, body);
		const key = number ? `pull-${number}` : `commit-${sha}`;
		if (seen.has(key)) continue;
		seen.add(key);
		changes.push({
			sha,
			number,
			title,
			author,
			mergedAt: committedAt,
			labels: [],
			url: number
				? `https://github.com/${repository}/pull/${number}`
				: `https://github.com/${repository}/commit/${sha}`,
		});
	}

	return changes.sort((a, b) => a.mergedAt.localeCompare(b.mergedAt));
}

function main() {
	const from = parseArgument("from") ?? earliestCommitDate();
	const to = parseArgument("to") ?? latestSunday();
	const force = process.argv.includes("--force");
	assertDate(from, "from");
	assertDate(to, "to");
	if (from > to) throw new Error(`Backfill start ${from} is after end ${to}`);

	const covered = existingCoverage();
	let created = 0;
	let preserved = 0;
	for (const window of weeklyWindows(from, to)) {
		if (covered.has(window.end)) {
			preserved += 1;
			continue;
		}

		const file = `${window.end}-weekly-update.mdx`;
		const filePath = path.join(changelogDirectory, file);
		if (fs.existsSync(filePath) && !force) {
			throw new Error(`${file} already exists; use --force to regenerate it`);
		}
		const changes = changesForWindow(window.start, window.end);
		const rendered = renderHistoricalWeek({
			changes,
			startDate: window.start,
			endDate: window.end,
			totalCommits: changes.length,
		});
		fs.writeFileSync(filePath, rendered.content);
		created += 1;
		console.log(
			`${file}: ${rendered.counts.included} user-facing changes from ${changes.length} commits`,
		);
	}

	console.log(`Backfill complete: ${created} weekly entries created, ${preserved} existing entries preserved.`);
}

main();
