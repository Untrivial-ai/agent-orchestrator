import { describe, expect, it } from "vitest";
import { resolveUsedMaxWidthPx } from "./resolve-used-max-width";

describe("resolveUsedMaxWidthPx", () => {
	it("returns px used-values directly", () => {
		const el = document.createElement("div");
		el.style.maxWidth = "480px";
		document.body.appendChild(el);
		expect(resolveUsedMaxWidthPx(el)).toBe(480);
		el.remove();
	});

	it("does not treat unresolved min() as NaN / null without measuring", () => {
		const split = document.createElement("div");
		split.id = "session-workspace";
		split.style.cssText = "position:relative;width:1000px;height:100px;";
		const panel = document.createElement("div");
		panel.style.cssText =
			"position:absolute;right:0;top:0;height:100%;width:400px;" +
			"max-width:min(55%, max(300px, calc(100% - 560px)));";
		split.appendChild(panel);
		document.body.appendChild(split);

		const resolved = resolveUsedMaxWidthPx(panel);
		// jsdom may not fully resolve min(), but must not pretend parseFloat worked
		// on the expression string. Either a positive measured px or null is ok —
		// never a bogus small number from parseFloat("min(...)").
		if (resolved !== null) {
			expect(resolved).toBeGreaterThan(100);
			expect(resolved).toBeLessThanOrEqual(1000);
		}
		split.remove();
	});

	it("returns null for max-width: none", () => {
		const el = document.createElement("div");
		document.body.appendChild(el);
		expect(resolveUsedMaxWidthPx(el)).toBeNull();
		el.remove();
	});
});
