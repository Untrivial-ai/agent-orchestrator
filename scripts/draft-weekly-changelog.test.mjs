import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
	categorizePrTitle,
	defaultSinceDate,
	findLastWeeklyEntryDate,
	parseArgs,
	parseChangelogFrontmatter,
	renderWeeklyDraft,
	stripConventionalPrefix,
} from "./draft-weekly-changelog.mjs";

describe("categorizePrTitle", () => {
	it("maps conventional prefixes", () => {
		assert.equal(categorizePrTitle("feat(chat): add search"), "features");
		assert.equal(categorizePrTitle("fix(board): wrap rows"), "fixes");
		assert.equal(categorizePrTitle("perf(landing): cut weight"), "improvements");
		assert.equal(categorizePrTitle("docs: add map"), "skip");
		assert.equal(categorizePrTitle("chore(release): bump"), "skip");
	});

	it("maps imperative product verbs to features", () => {
		assert.equal(categorizePrTitle("Restore the all sessions board"), "features");
		assert.equal(categorizePrTitle("Add Safari profile import support on macOS"), "features");
	});
});

describe("parseChangelogFrontmatter", () => {
	it("detects weekly entries by kind and by dated slug", () => {
		const weekly = parseChangelogFrontmatter(
			"/x/2026-09-18-mobile.mdx",
			`---\ntitle: "Hello"\ndate: "2026-09-18"\nkind: weekly\n---\n\nbody\n`,
		);
		assert.equal(weekly?.weekly, true);
		assert.equal(weekly?.date, "2026-09-18");

		const bySlug = parseChangelogFrontmatter(
			"/x/2026-09-11-board.mdx",
			`---\ntitle: "Board"\ndate: "2026-09-11T12:00:00Z"\n---\n`,
		);
		assert.equal(bySlug?.weekly, true);

		const release = parseChangelogFrontmatter(
			"/x/agent-orchestrator-0-13-0.mdx",
			`---\ntitle: "0.13.0"\ndate: "2026-09-12T11:55:00+05:30"\n---\n`,
		);
		assert.equal(release?.weekly, false);
	});
});

describe("findLastWeeklyEntryDate", () => {
	it("returns the newest weekly day", () => {
		const files = {
			"agent-orchestrator-0-13-0.mdx": `---\ntitle: "0.13.0"\ndate: "2026-09-12"\n---\n`,
			"2026-09-11-board.mdx": `---\ntitle: "Board"\ndate: "2026-09-11"\nkind: weekly\n---\n`,
			"2026-09-18-mobile.mdx": `---\ntitle: "Mobile"\ndate: "2026-09-18"\nkind: weekly\n---\n`,
		};
		const last = findLastWeeklyEntryDate("/c", {
			list: () => Object.keys(files),
			read: (p) => files[p.replace(/^\/c\//, "")],
		});
		assert.equal(last?.day, "2026-09-18");
		assert.equal(last?.slug, "2026-09-18-mobile");
		assert.equal(findLastWeeklyEntryDate("/missing", { list: () => [] }), null);
	});
});

describe("defaultSinceDate", () => {
	it("is seven UTC days before now", () => {
		assert.equal(defaultSinceDate(new Date("2026-09-18T15:00:00Z")), "2026-09-11");
	});
});

describe("renderWeeklyDraft", () => {
	it("groups PRs and emits MediaPlaceholder slots for features", () => {
		const { markdown, counts } = renderWeeklyDraft({
			since: "2026-09-11",
			untilDay: "2026-09-18",
			slug: "2026-09-18-weekly",
			pulls: [
				{
					number: 1,
					title: "feat(mobile): revamp flows",
					url: "https://example.com/1",
					mergedAt: "2026-09-18T00:00:00Z",
					author: "alice",
				},
				{
					number: 2,
					title: "fix(board): wrap rows",
					url: "https://example.com/2",
					mergedAt: "2026-09-17T00:00:00Z",
					author: "bob",
				},
				{
					number: 3,
					title: "perf(landing): cut weight",
					url: "https://example.com/3",
					mergedAt: "2026-09-16T00:00:00Z",
					author: "carol",
				},
				{
					number: 4,
					title: "docs: ignore me",
					url: "https://example.com/4",
					mergedAt: "2026-09-15T00:00:00Z",
					author: "dave",
				},
			],
		});
		assert.equal(counts.features, 1);
		assert.equal(counts.fixes, 1);
		assert.equal(counts.improvements, 1);
		assert.equal(counts.skip, 1);
		assert.match(markdown, /kind: weekly/);
		assert.match(markdown, /draft: true/);
		assert.match(markdown, /<MediaPlaceholder/);
		assert.match(markdown, /RECORD:/);
		assert.match(markdown, /## Features/);
		assert.match(markdown, /## Improvements/);
		assert.match(markdown, /## Fixes/);
		assert.match(markdown, /@alice/);
		assert.equal(stripConventionalPrefix("feat(mobile): revamp flows"), "revamp flows");
	});
});

describe("parseArgs", () => {
	it("parses flags", () => {
		assert.deepEqual(parseArgs(["--since", "2026-09-11", "--out", "x.mdx"]), {
			since: "2026-09-11",
			out: "x.mdx",
		});
	});
});
