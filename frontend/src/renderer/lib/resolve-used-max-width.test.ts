import { describe, expect, it } from "vitest";
import {
	resolveSessionInspectorMaxWidthPx,
	resolveUsedMaxWidthPx,
} from "./resolve-used-max-width";

describe("resolveSessionInspectorMaxWidthPx", () => {
	it("mirrors inspectorMaxWidthPx against the split CSS variable", () => {
		const split = document.createElement("div");
		split.id = "session-workspace";
		Object.defineProperty(split, "clientWidth", { configurable: true, value: 1008 });
		split.style.setProperty(
			"--session-inspector-max-width",
			"min(55%, max(300px, calc(100% - 560px)))",
		);
		document.body.appendChild(split);

		// available = 1008 - 8 = 1000; min(1000, 550, 440) = 440
		expect(resolveSessionInspectorMaxWidthPx(split)).toBe(440);
		split.remove();
	});

	it("uses browser-mode percent and chat floor from the variable", () => {
		const split = document.createElement("div");
		split.id = "session-workspace";
		Object.defineProperty(split, "clientWidth", { configurable: true, value: 1008 });
		split.style.setProperty(
			"--session-inspector-max-width",
			"min(68%, max(300px, calc(100% - 440px)))",
		);
		document.body.appendChild(split);

		// available = 1000; min(1000, 680, 560) = 560
		expect(resolveSessionInspectorMaxWidthPx(split)).toBe(560);
		split.remove();
	});
});

describe("resolveUsedMaxWidthPx", () => {
	it("returns px used-values directly", () => {
		const el = document.createElement("div");
		el.style.maxWidth = "480px";
		document.body.appendChild(el);
		expect(resolveUsedMaxWidthPx(el)).toBe(480);
		el.remove();
	});

	it("falls back to the session-split CSS variable for unresolved min()", () => {
		const split = document.createElement("div");
		split.id = "session-workspace";
		Object.defineProperty(split, "clientWidth", { configurable: true, value: 1008 });
		split.style.setProperty(
			"--session-inspector-max-width",
			"min(55%, max(300px, calc(100% - 560px)))",
		);
		const panel = document.createElement("div");
		panel.style.maxWidth = "min(55%, max(300px, calc(100% - 560px)))";
		split.appendChild(panel);
		document.body.appendChild(split);

		expect(resolveUsedMaxWidthPx(panel)).toBe(440);
		split.remove();
	});
});
