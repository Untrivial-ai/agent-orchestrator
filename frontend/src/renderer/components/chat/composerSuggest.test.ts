import { describe, expect, it } from "vitest";
import {
	findActiveTrigger,
	moveHighlight,
	rankFiles,
	rankSkills,
} from "./composerSuggest";
import type { ChatSkill } from "../../types/conversation";

const skill = (name: string, description = "", source = "user"): ChatSkill => ({
	name,
	displayName: name,
	description,
	source,
});

describe("findActiveTrigger", () => {
	it("opens a skill trigger for a slash at the start of the message", () => {
		expect(findActiveTrigger("/rev", 4)).toEqual({ kind: "skill", start: 0, query: "rev" });
	});

	it("opens a skill trigger for a bare slash, so the whole catalog is browsable", () => {
		expect(findActiveTrigger("/", 1)).toEqual({ kind: "skill", start: 0, query: "" });
	});

	it("opens a slash command after existing prose", () => {
		expect(findActiveTrigger("read src/app", 12)).toBeUndefined();
		expect(findActiveTrigger("either and/or", 13)).toBeUndefined();
		expect(findActiveTrigger("look at /rev", 12)).toEqual({
			kind: "skill",
			start: 8,
			query: "rev",
		});
	});

	it("opens a file trigger for an at-sign after whitespace", () => {
		expect(findActiveTrigger("look at @Chat", 13)).toEqual({
			kind: "file",
			start: 8,
			query: "Chat",
		});
	});

	// Otherwise an email address opens a file menu mid-word.
	it("ignores an at-sign inside a word", () => {
		expect(findActiveTrigger("mail dhruv@corethink.ai", 23)).toBeUndefined();
	});

	// Once there is a space the user has moved on to prose; holding the menu open
	// would keep Enter hijacked.
	it("closes the trigger at whitespace", () => {
		expect(findActiveTrigger("@src/app more words", 19)).toBeUndefined();
		expect(findActiveTrigger("/review now", 11)).toBeUndefined();
	});

	it("reads the query up to the caret, not to the end of the text", () => {
		expect(findActiveTrigger("@ChatComposer.tsx", 5)).toEqual({
			kind: "file",
			start: 0,
			query: "Chat",
		});
	});

	it("finds nothing in plain text", () => {
		expect(findActiveTrigger("just a message", 14)).toBeUndefined();
		expect(findActiveTrigger("", 0)).toBeUndefined();
	});
});

describe("rankSkills", () => {
	const skills = [
		skill("code-review", "Review a pull request"),
		skill("review", "Look over the diff"),
		skill("ship", "Open a PR"),
		skill("investigate", "Root cause analysis for a bug"),
	];

	it("returns everything for an empty query so the catalog is browsable", () => {
		expect(rankSkills(skills, "").map((item) => item.value)).toHaveLength(4);
	});

	// An exact/prefix match on the name has to beat a match buried mid-name,
	// otherwise the ordering reads as arbitrary.
	it("ranks a name prefix above a mid-name match", () => {
		const ranked = rankSkills(skills, "rev").map((item) => item.value);
		expect(ranked[0]).toBe("review");
		expect(ranked).toContain("code-review");
		expect(ranked.indexOf("review")).toBeLessThan(ranked.indexOf("code-review"));
	});

	it("matches on the description so a half-remembered skill is still findable", () => {
		expect(rankSkills(skills, "root cause").map((item) => item.value)).toEqual(["investigate"]);
	});

	it("drops non-matches entirely", () => {
		expect(rankSkills(skills, "zzzz")).toEqual([]);
	});

	it("is empty when the provider reported no skills", () => {
		expect(rankSkills([], "rev")).toEqual([]);
		expect(rankSkills([], "")).toEqual([]);
	});

	it("carries the label, first description line, and scope onto the row", () => {
		const [row] = rankSkills([skill("ship", "Open a PR\nsecond line", "repo")], "ship");
		expect(row).toMatchObject({
			value: "ship",
			label: "ship",
			detail: "Open a PR",
			badge: "repo",
		});
	});

	it("shows and searches the provider's command argument hint", () => {
		const command = { ...skill("review", "Review a change"), inputHint: "<pull-request>" };
		const [row] = rankSkills([command], "pull-request");
		expect(row).toMatchObject({
			value: "review",
			detail: "Review a change · <pull-request>",
		});
	});

	it("orders equal scores by name so the list does not reshuffle on refetch", () => {
		const a = rankSkills([skill("beta"), skill("alpha")], "");
		expect(a.map((item) => item.value)).toEqual(["alpha", "beta"]);
	});
});

describe("rankFiles", () => {
	const paths = [
		"AGENTS.md",
		"backend/internal/ports/chat.go",
		"frontend/src/renderer/components/chat/ChatComposer.tsx",
		"frontend/src/renderer/components/chat/ChatWorkspace.tsx",
	];

	// People search for the file name, not the directories above it.
	it("scores the basename ahead of the full path", () => {
		const ranked = rankFiles(paths, "chatcomposer");
		expect(ranked[0]?.value).toBe(
			"frontend/src/renderer/components/chat/ChatComposer.tsx",
		);
	});

	it("narrows on a directory fragment", () => {
		expect(rankFiles(paths, "ports/").map((item) => item.value)).toEqual([
			"backend/internal/ports/chat.go",
		]);
	});

	it("shows the basename as the label and the parent as the detail", () => {
		const [row] = rankFiles(["backend/internal/ports/chat.go"], "chat.go");
		expect(row).toMatchObject({
			value: "backend/internal/ports/chat.go",
			label: "chat.go",
			detail: "backend/internal/ports",
		});
	});

	// A file at the repo root has no parent directory. Deriving one from a negative
	// index truncated its own name and showed "AGENTS.m" under "AGENTS.md".
	it("gives a root-level file no parent detail", () => {
		const [row] = rankFiles(["AGENTS.md"], "agents");
		expect(row).toMatchObject({ value: "AGENTS.md", label: "AGENTS.md" });
		expect(row?.detail).toBeUndefined();
	});

	it("prefers a shallower path among equal matches", () => {
		const ranked = rankFiles(["a/b/c/notes.md", "notes.md"], "notes");
		expect(ranked[0]?.value).toBe("notes.md");
	});
});

describe("moveHighlight", () => {
	it("wraps at both ends so a short list is fully reachable by keyboard", () => {
		expect(moveHighlight(0, -1, 3)).toBe(2);
		expect(moveHighlight(2, 1, 3)).toBe(0);
		expect(moveHighlight(0, 1, 3)).toBe(1);
	});

	it("stays at zero with nothing to highlight", () => {
		expect(moveHighlight(0, 1, 0)).toBe(0);
	});
});
