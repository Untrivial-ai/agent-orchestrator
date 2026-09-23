import { describe, expect, it } from "vitest";
import {
	explicitWorkspaceFilePath,
	findWorkspaceFilePath,
	matchWorkspaceFilePath,
	normalizeWorkspaceFileReference,
} from "./workspace-file-path";

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

	it("strips editor line locations before matching", () => {
		expect(matchWorkspaceFilePath("src/a.ts:42:7", files)).toBe("src/a.ts");
		expect(matchWorkspaceFilePath("docs/report.md#L12-L18", files)).toBe("docs/report.md");
	});

	it("resolves encoded absolute file urls against workspace files", () => {
		expect(
			matchWorkspaceFilePath("file:///Users/me/project/docs/report.md%3A42", files),
		).toBe("docs/report.md");
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

	it("only resolves a basename when it is unambiguous", () => {
		expect(findWorkspaceFilePath("index.ts:10", ["frontend/index.ts", "backend/index.ts"])).toBeUndefined();
	});

	it("recognizes explicit local links without mistaking urls for files", () => {
		expect(explicitWorkspaceFilePath("/repo/src/new.ts:12")).toBe("/repo/src/new.ts");
		expect(explicitWorkspaceFilePath("docs/new.ts#L4")).toBe("docs/new.ts");
		expect(explicitWorkspaceFilePath("https://example.com/docs/new.ts")).toBeUndefined();
		expect(explicitWorkspaceFilePath("//example.com/docs/new.ts")).toBeUndefined();
		expect(explicitWorkspaceFilePath("mailto:dev@example.com")).toBeUndefined();
	});

	it("does not match a web url by its path suffix", () => {
		expect(findWorkspaceFilePath("https://example.com/src/a.ts", ["src/a.ts"])).toBeUndefined();
	});

	it("normalizes Windows file references", () => {
		expect(normalizeWorkspaceFileReference("C:\\repo\\src\\app.ts:9")).toBe("C:/repo/src/app.ts");
	});
});
