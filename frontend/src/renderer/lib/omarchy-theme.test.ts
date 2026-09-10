import { afterEach, describe, expect, it } from "vitest";
import { contrast } from "../../shared/omarchy-theme";
import { applyDocumentThemeStyle, readStoredThemeStyle, resolveTheme, setOmarchyPalette } from "./theme";
import { buildTerminalThemes } from "./terminal-themes";
const palette = { background: "#1d150f", foreground: "#e3ddd0", accent: "#d4b24a", cursor: "#d4b24a", selection: "#4d3e23", ansi: Array(16).fill("#f25107") };

afterEach(() => { localStorage.clear(); setOmarchyPalette(null); applyDocumentThemeStyle("orchestrate"); });
describe("automatic color style", () => {
	it("defaults to automatic but preserves every saved explicit style", () => {
		expect(readStoredThemeStyle()).toBe("automatic");
		for (const style of ["orchestrate", "nord", "solarized"]) {
			localStorage.setItem("ao.theme-style", style);
			expect(readStoredThemeStyle()).toBe(style);
		}
	});
	it("applies the actual palette and refreshes terminal ANSI colors", () => {
		setOmarchyPalette(palette);
		applyDocumentThemeStyle("automatic");
		expect(resolveTheme("system")).toBe("dark");
		expect(buildTerminalThemes().dark).toMatchObject({ foreground: palette.foreground, black: "#f25107", red: "#f25107", cursor: palette.cursor });
		setOmarchyPalette({ ...palette, background: "#ffffff", ansi: Array(16).fill("#123456") });
		applyDocumentThemeStyle("automatic");
		expect(resolveTheme("system")).toBe("light");
		expect(buildTerminalThemes().light.red).toBe("#123456");
	});
	it("clears inherited tokens for explicit style, explicit appearance, and unavailable data", () => {
		for (const reason of ["style", "appearance", "missing"]) {
			localStorage.clear(); setOmarchyPalette(palette); applyDocumentThemeStyle("automatic");
			if (reason === "appearance") localStorage.setItem("ao.theme", "light");
			if (reason === "missing") setOmarchyPalette(null);
			applyDocumentThemeStyle(reason === "style" ? "nord" : "automatic");
			expect(document.documentElement.style.getPropertyValue("--background")).toBe("");
		}
	});
	it("keeps menu and selected text readable even for poor source contrast", () => {
		setOmarchyPalette({ ...palette, foreground: palette.background, surface: "#ffffff", selection: "#ffffff" });
		applyDocumentThemeStyle("automatic");
		const css = document.documentElement.style;
		for (const [bg, fg] of [["popover", "popover-foreground"], ["accent", "accent-foreground"], ["primary", "primary-foreground"]]) {
			expect(contrast(css.getPropertyValue(`--${bg}`), css.getPropertyValue(`--${fg}`))).toBeGreaterThanOrEqual(4.5);
		}
	});
});
