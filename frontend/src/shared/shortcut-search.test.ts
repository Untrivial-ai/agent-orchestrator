import { describe, expect, it } from "vitest";
import {
	APP_SHORTCUTS,
	type AppShortcutId,
	type KeybindingOverrides,
	type ShortcutBinding,
	type ShortcutDefinition,
} from "./shortcuts";
import { shortcutMatchesSearch } from "./shortcut-search";

function shortcut(id: AppShortcutId): ShortcutDefinition {
	const found = APP_SHORTCUTS.find((candidate) => candidate.id === id);
	if (!found) throw new Error(`unknown shortcut: ${id}`);
	return found;
}

function binding(key: string, modifiers: Partial<Pick<ShortcutBinding, "ctrl" | "meta" | "shift" | "alt">>): ShortcutBinding {
	return { key, ctrl: false, meta: false, shift: false, alt: false, ...modifiers };
}

function matches(
	id: AppShortcutId,
	query: string,
	options: { isMac?: boolean; overrides?: KeybindingOverrides } = {},
): boolean {
	const definition = shortcut(id);
	return shortcutMatchesSearch(definition, query, {
		label: definition.label,
		category: definition.category,
		isMac: options.isMac ?? false,
		overrides: options.overrides ?? {},
	});
}

describe("shortcutMatchesSearch", () => {
	it("returns every shortcut for an empty or whitespace query", () => {
		for (const definition of APP_SHORTCUTS) {
			expect(
				shortcutMatchesSearch(definition, "", { label: definition.label, category: definition.category, isMac: false }),
			).toBe(true);
			expect(
				shortcutMatchesSearch(definition, "   ", { label: definition.label, category: definition.category, isMac: false }),
			).toBe(true);
		}
	});

	it("matches the locale-independent shortcut id", () => {
		expect(matches("toggle-sidebar", "toggle-sidebar")).toBe(true);
		expect(matches("focus-terminal", "focus-terminal")).toBe(true);
		expect(matches("toggle-sidebar", "focus-terminal")).toBe(false);
	});

	it("matches the localized label and category (case-insensitive)", () => {
		expect(matches("toggle-sidebar", "SIDEBAR")).toBe(true);
		expect(matches("next-tab", "navigation")).toBe(true);
		expect(matches("next-tab", "session")).toBe(false);
	});

	it("still matches the default binding after the shortcut is remapped", () => {
		const overrides: KeybindingOverrides = { "toggle-sidebar": [binding("p", { ctrl: true, shift: true })] };
		expect(matches("toggle-sidebar", "ctrl b", { overrides })).toBe(true);
		expect(matches("toggle-sidebar", "ctrl shift p", { overrides })).toBe(true);
	});

	it("matches modifier aliases on macOS", () => {
		for (const query of ["cmd b", "command b", "cmd+b", "meta b", "meta-key b", "super b", "win b", "⌘"]) {
			expect(matches("toggle-sidebar", query, { isMac: true })).toBe(true);
		}
	});

	it("matches ctrl and control aliases on Windows/Linux", () => {
		expect(matches("toggle-sidebar", "ctrl b")).toBe(true);
		expect(matches("toggle-sidebar", "control b")).toBe(true);
		expect(matches("toggle-sidebar", "ctrl+b")).toBe(true);
	});

	it("matches option and alt aliases for the alt modifier", () => {
		// toggle-browser-devtools is ⌘⌥I on macOS and Ctrl+Shift+I elsewhere.
		expect(matches("toggle-browser-devtools", "option i", { isMac: true })).toBe(true);
		expect(matches("toggle-browser-devtools", "alt i", { isMac: true })).toBe(true);
		expect(matches("toggle-browser-devtools", "⌥ i", { isMac: true })).toBe(true);
	});

	it("accepts spaced key queries in place of the + separator", () => {
		expect(matches("new-session", "ctrl shift n")).toBe(true);
		expect(matches("new-session", "ctrl+shift+n")).toBe(true);
		expect(matches("new-session", "control shift n")).toBe(true);
	});

	it("requires every whitespace-separated token to match (AND)", () => {
		expect(matches("next-tab", "tab next")).toBe(true);
		expect(matches("next-tab", "next tab")).toBe(true);
		expect(matches("previous-tab", "tab previous")).toBe(true);
		expect(matches("next-tab", "tab previous")).toBe(false);
		expect(matches("next-tab", "next sidebar")).toBe(false);
	});

	it("matches English keywords not present in the localized label", () => {
		expect(matches("command-palette", "command bar")).toBe(true);
		expect(matches("command-palette", "palette")).toBe(true);
		expect(matches("toggle-sidebar", "side panel")).toBe(true);
		expect(matches("toggle-inspector", "details")).toBe(true);
		expect(matches("toggle-browser-devtools", "developer tools")).toBe(true);
	});

	it("matches the modified status token against keybinding overrides", () => {
		const overrides: KeybindingOverrides = { "toggle-sidebar": [binding("p", { ctrl: true })] };
		expect(matches("toggle-sidebar", "modified", { overrides })).toBe(true);
		expect(matches("next-tab", "modified", { overrides })).toBe(false);
	});

	it("matches the unassigned status token for an empty effective binding set", () => {
		const overrides: KeybindingOverrides = { "toggle-sidebar": [] };
		expect(matches("toggle-sidebar", "unassigned", { overrides })).toBe(true);
		expect(matches("next-tab", "unassigned")).toBe(false);
	});

	it("matches the fixed status token for non-customizable shortcuts", () => {
		expect(matches("open-project", "fixed")).toBe(true);
		expect(matches("toggle-sidebar", "fixed")).toBe(false);
	});

	it("combines status tokens with other tokens using AND", () => {
		const overrides: KeybindingOverrides = { "toggle-sidebar": [binding("p", { ctrl: true })] };
		expect(matches("toggle-sidebar", "modified sidebar", { overrides })).toBe(true);
		expect(matches("toggle-sidebar", "modified terminal", { overrides })).toBe(false);
	});
});
