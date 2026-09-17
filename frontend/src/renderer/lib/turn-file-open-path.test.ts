import { describe, expect, it } from "vitest";
import { turnFileOpenPath, turnPathHints, workspaceRelativeOpenPath } from "./turn-file-open-path";

const cwd = "/Users/me/.ao/dev/data/worktrees/demo/demo-1";

describe("workspaceRelativeOpenPath", () => {
	it("strips the worktree cwd and keeps nested path segments", () => {
		expect(workspaceRelativeOpenPath(`${cwd}/frontend/index.ts`, cwd)).toBe("frontend/index.ts");
		expect(workspaceRelativeOpenPath(`${cwd}/backend/index.ts`, cwd)).toBe("backend/index.ts");
	});

	it("keeps parent segments when cwd is missing", () => {
		expect(
			workspaceRelativeOpenPath("/Users/me/.ao/dev/data/worktrees/demo/demo-1/frontend/index.ts"),
		).toBe("frontend/index.ts");
	});
});

describe("turnFileOpenPath", () => {
	it("passes through an already workspace-relative path", () => {
		expect(turnFileOpenPath("src/a.ts", { byBase: new Map() })).toBe("src/a.ts");
	});

	it("preserves duplicate-disambiguating suffixes for relative paths", () => {
		const hints = { byBase: new Map(), cwd };
		expect(turnFileOpenPath("frontend/index.ts", hints)).toBe("frontend/index.ts");
		expect(turnFileOpenPath("backend/index.ts", hints)).toBe("backend/index.ts");
	});

	it("converts an absolute turn diff path using the worktree cwd", () => {
		const hints = { byBase: new Map(), cwd };
		expect(turnFileOpenPath(`${cwd}/frontend/index.ts`, hints)).toBe("frontend/index.ts");
		expect(turnFileOpenPath(`${cwd}/backend/index.ts`, hints)).toBe("backend/index.ts");
	});

	it("resolves a basename hint from the turn's file_change activity", () => {
		const hints = turnPathHints([
			{
				kind: "activity",
				id: "a-1",
				sequence: 1,
				revision: 0,
				activityKind: "file_change",
				status: "completed",
				summary: "Edited 1 file",
				detail: {
					files: [
						{
							path: `${cwd}/docs/notes.txt`,
							status: "added",
							additions: 1,
							deletions: 0,
						},
					],
				},
				createdAt: new Date().toISOString(),
			},
		]);
		expect(turnFileOpenPath("notes.txt", hints)).toBe("docs/notes.txt");
	});
});
