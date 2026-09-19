import { describe, expect, it } from "vitest";
import { matchWorkspaceFilePath, qualifyTurnDiffPath } from "./workspace-file-path";

describe("matchWorkspaceFilePath", () => {
	const files = [
		{ path: "src/a.ts", status: "modified" as const, additions: 1, deletions: 0, binary: false, size: 12 },
		{ path: "docs/report.md", status: "added" as const, additions: 10, deletions: 0, binary: false, size: 40 },
	];

	it("matches an exact workspace path", () => {
		expect(matchWorkspaceFilePath("src/a.ts", files)).toBe("src/a.ts");
	});

	it("matches a basename from a turn diff", () => {
		expect(matchWorkspaceFilePath("report.md", files)).toBe("docs/report.md");
	});

	it("matches a suffix path", () => {
		expect(matchWorkspaceFilePath("a.ts", files)).toBe("src/a.ts");
	});

	it("normalizes leading ./", () => {
		expect(matchWorkspaceFilePath("./src/a.ts", files)).toBe("src/a.ts");
	});

	it("falls back to the normalized request when nothing matches", () => {
		expect(matchWorkspaceFilePath("missing.txt", files)).toBe("missing.txt");
	});

	it("disambiguates duplicate basenames with a path suffix", () => {
		const duplicateFiles = [
			...files,
			{ path: "frontend/index.ts", status: "modified" as const, additions: 1, deletions: 0, binary: false, size: 12 },
			{ path: "backend/index.ts", status: "modified" as const, additions: 1, deletions: 0, binary: false, size: 12 },
		];
		expect(matchWorkspaceFilePath("frontend/index.ts", duplicateFiles)).toBe("frontend/index.ts");
		expect(matchWorkspaceFilePath("backend/index.ts", duplicateFiles)).toBe("backend/index.ts");
	});

});

describe("qualifyTurnDiffPath", () => {
	const file = (path: string) => ({
		path,
		status: "added" as const,
		additions: 1,
		deletions: 0,
		binary: false,
		size: 12,
	});

	it("qualifies a bare row when exactly one changed file matches", () => {
		expect(qualifyTurnDiffPath("workspace-test.txt", [file("alpha/workspace-test.txt")])).toBe(
			"alpha/workspace-test.txt",
		);
	});

	it("keeps an already-qualified row unchanged", () => {
		expect(
			qualifyTurnDiffPath("alpha/workspace-test.txt", [file("alpha/workspace-test.txt")]),
		).toBe("alpha/workspace-test.txt");
	});

	// The core #5366 case the frontend cannot fix: two repos change a same-named file,
	// the daemon emits the bare basename for both, so the honest answer is the bare
	// path rather than guessing a repo.
	it("keeps the bare path when two repos share the basename", () => {
		expect(
			qualifyTurnDiffPath("workspace-test.txt", [
				file("alpha/workspace-test.txt"),
				file("beta/workspace-test.txt"),
			]),
		).toBe("workspace-test.txt");
	});

	it("keeps the bare path when nothing in the change set matches", () => {
		expect(qualifyTurnDiffPath("workspace-test.txt", [file("alpha/other.txt")])).toBe(
			"workspace-test.txt",
		);
	});

	it("returns the raw path when it normalizes to empty", () => {
		expect(qualifyTurnDiffPath("./", [file("alpha/workspace-test.txt")])).toBe("./");
	});
});
