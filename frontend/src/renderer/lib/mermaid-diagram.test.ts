import { describe, expect, it } from "vitest";
import { isRenderableDiagram, MAX_DIAGRAM_CHARS, renderMermaidDiagram } from "./mermaid-diagram";

describe("mermaid-diagram validation", () => {
	it("accepts ordinary diagram source", () => {
		expect(isRenderableDiagram("flowchart TD\n    A --> B")).toBe(true);
	});

	it("rejects empty and whitespace-only blocks without touching the engine", async () => {
		expect(isRenderableDiagram("")).toBe(false);
		expect(isRenderableDiagram("   \n  ")).toBe(false);
		await expect(renderMermaidDiagram("", "dark")).rejects.toThrow(/empty/);
	});

	it("rejects oversized blocks that would hang layout on the render thread", async () => {
		const oversized = `flowchart TD\n${"    A --> B\n".repeat(2_000)}`;
		expect(oversized.length).toBeGreaterThan(MAX_DIAGRAM_CHARS);
		expect(isRenderableDiagram(oversized)).toBe(false);
		await expect(renderMermaidDiagram(oversized, "dark")).rejects.toThrow(/too large/);
	});

	it("accepts a block exactly at the limit", () => {
		expect(isRenderableDiagram(`x${"y".repeat(MAX_DIAGRAM_CHARS - 1)}`)).toBe(true);
	});
});
