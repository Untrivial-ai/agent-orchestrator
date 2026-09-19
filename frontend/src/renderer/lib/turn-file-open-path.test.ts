import { describe, expect, it } from "vitest";
import { resolveTurnFilePath, turnPathHints } from "./turn-file-open-path";

const cwd = "/Users/me/.ao/dev/data/worktrees/demo/demo-1";

function fileChangeActivity(path: string) {
	return {
		kind: "activity" as const,
		id: `a-${path}`,
		sequence: 1,
		revision: 0,
		activityKind: "file_change" as const,
		status: "completed" as const,
		summary: "Edited 1 file",
		detail: {
			files: [{ path, status: "added" as const, additions: 1, deletions: 0 }],
		},
		createdAt: new Date().toISOString(),
	};
}

// resolveTurnFilePath maps a turn-diff path onto the absolute worktree path the
// row's tooltip shows. The visible label no longer flows through here; it is
// resolved against the real workspace file list in TurnChangedFiles.
describe("resolveTurnFilePath", () => {
	it("passes an absolute path through unchanged", () => {
		expect(resolveTurnFilePath(`${cwd}/src/a.ts`, { byBase: new Map() })).toBe(`${cwd}/src/a.ts`);
	});

	it("resolves a basename against the turn's absolute file_change path", () => {
		const hints = turnPathHints([fileChangeActivity(`${cwd}/docs/notes.txt`)]);
		expect(resolveTurnFilePath("notes.txt", hints)).toBe(`${cwd}/docs/notes.txt`);
	});

	it("matches on the whole relative suffix, not just the basename", () => {
		// `src/a.ts` must not bind to an unrelated `.../other/a.ts`; with no suffix
		// match and a cwd present it joins the cwd instead.
		const hints = { ...turnPathHints([fileChangeActivity(`${cwd}/other/a.ts`)]), cwd };
		expect(resolveTurnFilePath("src/a.ts", hints)).toBe(`${cwd}/src/a.ts`);
	});

	it("joins the worktree cwd when no hint matches", () => {
		const hints = { byBase: new Map<string, string[]>(), cwd };
		expect(resolveTurnFilePath("notes.txt", hints)).toBe(`${cwd}/notes.txt`);
	});

	it("does not guess when candidates are genuinely ambiguous", () => {
		// Two repos change a same-named file and the row is bare, so no unique suffix
		// match exists; fall back to joining the cwd rather than picking a repo.
		const hints = {
			...turnPathHints([
				fileChangeActivity(`${cwd}/alpha/workspace-test.txt`),
				fileChangeActivity(`${cwd}/beta/workspace-test.txt`),
			]),
			cwd,
		};
		expect(resolveTurnFilePath("workspace-test.txt", hints)).toBe(`${cwd}/workspace-test.txt`);
	});

	it("returns the input when there is neither a hint nor a cwd", () => {
		expect(resolveTurnFilePath("notes.txt", { byBase: new Map() })).toBe("notes.txt");
	});
});

describe("turnPathHints", () => {
	it("collects a candidate per basename and the first cwd", () => {
		const hints = turnPathHints([
			{
				kind: "activity",
				id: "cmd",
				sequence: 1,
				revision: 0,
				activityKind: "command",
				status: "completed",
				summary: "ran",
				detail: { cwd, command: "ls" },
				createdAt: new Date().toISOString(),
			},
			fileChangeActivity(`${cwd}/docs/notes.txt`),
		]);
		expect(hints.cwd).toBe(cwd);
		expect(hints.byBase.get("notes.txt")).toEqual([`${cwd}/docs/notes.txt`]);
	});

	it("keeps every distinct candidate when basenames collide", () => {
		const hints = turnPathHints([
			fileChangeActivity(`${cwd}/alpha/workspace-test.txt`),
			fileChangeActivity(`${cwd}/beta/workspace-test.txt`),
		]);
		expect(hints.byBase.get("workspace-test.txt")).toEqual([
			`${cwd}/alpha/workspace-test.txt`,
			`${cwd}/beta/workspace-test.txt`,
		]);
	});
});
