import { describe, expect, it } from "vitest";
import {
	findComposerSuggestion,
	composerSuggestionKey,
	rankComposerCatalog,
	rankComposerFiles,
	rankComposerSkills,
	replaceComposerSuggestion,
} from "./composerSuggestions";

describe("mobile Chat composer suggestions", () => {
	it("finds slash skills and @ files at token boundaries", () => {
		expect(findComposerSuggestion("/rev")).toMatchObject({ kind: "skills", query: "rev", start: 0 });
		expect(findComposerSuggestion("inspect @src/app")).toMatchObject({ kind: "files", query: "src/app" });
		expect(findComposerSuggestion("https://ao.dev")).toBeUndefined();
		expect(findComposerSuggestion("please /review")).toBeUndefined();
		expect(findComposerSuggestion("email@example.com")).toBeUndefined();
	});

	it("replaces only the active token", () => {
		const text = "please inspect @src/ap now";
		const trigger = findComposerSuggestion(text, "please inspect @src/ap".length)!;
		expect(replaceComposerSuggestion(text, trigger, "src/app.ts")).toBe("please inspect src/app.ts now");
	});

	it("quotes paths with spaces and keeps the slash for provider skills", () => {
		expect(replaceComposerSuggestion("open @my", findComposerSuggestion("open @my")!, "my notes/todo.md"))
			.toBe('open "my notes/todo.md" ');
		expect(replaceComposerSuggestion("/rev", findComposerSuggestion("/rev")!, "review"))
			.toBe("/review ");
	});

	it("ranks names and basenames ahead of descriptions and deep paths", () => {
		const skills = [
			{ name: "code-review", displayName: "Code review", description: "Review a change" },
			{ name: "review", displayName: "Review", description: "Inspect code" },
		];
		expect(rankComposerSkills(skills, "rev").map((item) => item.value)).toEqual(["review", "code-review"]);
		expect(rankComposerFiles(["deep/src/app.ts", "app.ts", "docs/application.md"], "app").map((item) => item.value))
			.toEqual(["app.ts", "deep/src/app.ts", "docs/application.md"]);
	});

	it("keys a picker request to the exact active trigger", () => {
		expect(composerSuggestionKey({ kind: "files", query: "src", start: 8, end: 12 }))
			.toBe("files:8:12:src");
		expect(composerSuggestionKey({ kind: "files", query: "src/a", start: 8, end: 14 }))
			.not.toBe("files:8:12:src");
	});

	it("searches the full catalog before limiting visible picker results", () => {
		const files = Array.from({ length: 100 }, (_, index) => `src/common-${index}.ts`);
		files.push("deep/only-target.ts");

		expect(rankComposerCatalog({ kind: "files", paths: files }, "only-target")).toEqual([
			{ value: "deep/only-target.ts", label: "only-target.ts", detail: "deep" },
		]);
	});
});
