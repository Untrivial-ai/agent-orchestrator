import { afterEach, describe, expect, it } from "vitest";
import { useUiStore } from "../stores/ui-store";
const palette = { background: "#1d150f", foreground: "#e3ddd0", accent: "#d4b24a", cursor: "#d4b24a", selection: "#4d3e23", ansi: Array(16).fill("#f25107") };
afterEach(() => {
	localStorage.clear();
	useUiStore.getState().updateOmarchy(null);
	useUiStore.setState({ themeStyle: "automatic", themePreference: "system" });
});
describe("live Omarchy store integration", () => {
	it("updates scheme and revision without changing stored preferences", () => {
		const state = useUiStore.getState();
		state.updateOmarchy(palette);
		expect(useUiStore.getState().resolvedTheme).toBe("dark");
		state.updateOmarchy({ ...palette, background: "#ffffff" });
		expect(useUiStore.getState().resolvedTheme).toBe("light");
		expect(useUiStore.getState().omarchyRevision).toBe(state.omarchyRevision + 2);
		expect(localStorage.getItem("ao.theme")).toBeNull();
		expect(localStorage.getItem("ao.theme-style")).toBeNull();
	});
	it("preserves explicit appearance and clears inherited tokens immediately", () => {
		const state = useUiStore.getState();
		state.updateOmarchy(palette);
		state.setThemePreference("light");
		expect(useUiStore.getState().resolvedTheme).toBe("light");
		expect(document.documentElement.style.getPropertyValue("--background")).toBe("");
		state.updateOmarchy(palette);
		expect(useUiStore.getState().resolvedTheme).toBe("light");
	});
	it("restores inheritance after switching back from a named style", () => {
		const state = useUiStore.getState();
		state.updateOmarchy(palette);
		state.setThemeStyle("nord");
		expect(document.documentElement.style.getPropertyValue("--background")).toBe("");
		state.setThemeStyle("automatic");
		expect(document.documentElement.style.getPropertyValue("--background")).toBe(palette.background);
		state.updateOmarchy(null);
		expect(document.documentElement.dataset.styleTheme).toBeUndefined();
	});
});
