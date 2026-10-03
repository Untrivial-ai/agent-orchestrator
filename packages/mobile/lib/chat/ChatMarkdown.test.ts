import { describe, expect, it } from "vitest";
import { allowsUnderscoreEmphasis } from "./inlineEmphasis";
import { readFileSync } from "node:fs";
import { parseBlocks } from "./markdownBlocks";

describe("mobile Chat markdown blocks", () => {
	it("keeps GFM tables, tasks and remote images structured", () => {
		const blocks = parseBlocks([
			"| File | State |",
			"| --- | --- |",
			"| app.ts | changed |",
			"",
			"- [x] inspect",
			"- [ ] test",
			"",
			"![result](https://example.com/result.png)",
		].join("\n"));
		expect(blocks[0]).toEqual({ kind: "table", headers: ["File", "State"], rows: [["app.ts", "changed"]] });
		expect(blocks[1]).toMatchObject({ kind: "list", items: [{ text: "inspect", checked: true }, { text: "test", checked: false }] });
		expect(blocks[2]).toEqual({ kind: "image", alt: "result", url: "https://example.com/result.png" });
	});
});

it("keeps identifier underscores while allowing standalone emphasis", () => {
	const identifier = "NEW_TASK_HOST_B_OK";
	expect(allowsUnderscoreEmphasis(identifier, 3, 9)).toBe(false);
	expect(allowsUnderscoreEmphasis("_emphasis_", 0, 10)).toBe(true);
	expect(allowsUnderscoreEmphasis("before _emphasis_ after", 7, 17)).toBe(true);
});

describe("mobile Chat markdown layout", () => {
	it("allows list prose to shrink and wrap within the available width", () => {
		const source = readFileSync(new URL("./ChatMarkdown.tsx", import.meta.url), "utf8");
		expect(source).toContain("styles.listBody");
		expect(source).toContain("listBody: { flex: 1, minWidth: 0 }");
	});
});
