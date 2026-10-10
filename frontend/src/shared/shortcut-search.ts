// Shared search matcher for the keyboard-shortcut settings lists. Kept free of
// React/i18n so both settings surfaces (inline content and dialog) filter
// through one implementation and cannot drift. Localized strings are passed in
// by the caller; keywords and ids stay locale-independent.

import {
	defaultShortcutBindings,
	effectiveShortcutBindings,
	shortcutBindingLabel,
	type KeybindingOverrides,
	type ShortcutBinding,
	type ShortcutDefinition,
} from "./shortcuts";

export type ShortcutSearchContext = {
	/** Localized shortcut label, e.g. t("shortcut.toggle-sidebar"). */
	label: string;
	/** Localized category label, e.g. t("shortcut.category.general"). */
	category: string;
	isMac: boolean;
	/** User keybinding overrides; drives the "modified"/"unassigned" tokens. */
	overrides?: KeybindingOverrides;
};

// Modifier spellings users actually type. Symbols (⌘/⌥) cover macOS-rendered
// chords; the word forms cover users typing "cmd", "command", "option", etc.
const MODIFIER_ALIASES: Record<"ctrl" | "meta" | "alt" | "shift", readonly string[]> = {
	ctrl: ["ctrl", "control"],
	meta: ["cmd", "command", "⌘", "meta", "meta-key", "super", "win"],
	alt: ["alt", "option", "⌥"],
	shift: ["shift"],
};

function modifierAliasSets(binding: ShortcutBinding): readonly (readonly string[])[] {
	const sets: (readonly string[])[] = [];
	if (binding.ctrl) sets.push(MODIFIER_ALIASES.ctrl);
	if (binding.meta) sets.push(MODIFIER_ALIASES.meta);
	if (binding.alt) sets.push(MODIFIER_ALIASES.alt);
	if (binding.shift) sets.push(MODIFIER_ALIASES.shift);
	return sets;
}

/** Cartesian product of modifier aliases joined with "+", e.g. "cmd+shift+b". */
function aliasChordCombinations(sets: readonly (readonly string[])[]): string[] {
	let combos = [""];
	for (const aliases of sets) {
		const next: string[] = [];
		for (const prefix of combos) {
			for (const alias of aliases) {
				next.push(prefix ? `${prefix}+${alias}` : alias);
			}
		}
		combos = next;
	}
	return combos;
}

/**
 * Searchable representations of one binding: the rendered label (with its
 * platform separator), the bare key, and every modifier-alias chord joined with
 * "+". This lets "cmd b", "cmd+b", "⌘", and "ctrl+shift+n" all resolve.
 */
function bindingSearchStrings(binding: ShortcutBinding, isMac: boolean): string[] {
	const strings = new Set<string>();
	const key = binding.key.toLowerCase();
	const rendered = shortcutBindingLabel(binding, isMac).toLowerCase();
	if (rendered) strings.add(rendered);
	strings.add(key);
	for (const combo of aliasChordCombinations(modifierAliasSets(binding))) {
		strings.add(`${combo}+${key}`);
	}
	return [...strings];
}

/**
 * True when every whitespace-separated token in `query` matches the shortcut
 * row. An empty query matches everything. Each token may match the localized
 * label/category, the shortcut id, an English keyword, an effective or default
 * binding chord (including modifier aliases), or a status token.
 */
export function shortcutMatchesSearch(
	shortcut: ShortcutDefinition,
	query: string,
	context: ShortcutSearchContext,
): boolean {
	const tokens = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
	if (tokens.length === 0) return true;

	const { label, category, isMac, overrides = {} } = context;
	const effective = effectiveShortcutBindings(shortcut.id, isMac, overrides);
	const bindings = [...effective, ...defaultShortcutBindings(shortcut.id, isMac)];

	const haystack = new Set<string>([
		label.toLowerCase(),
		category.toLowerCase(),
		shortcut.id.toLowerCase(),
		...(shortcut.keywords ?? []).map((keyword) => keyword.toLowerCase()),
		...bindings.flatMap((binding) => bindingSearchStrings(binding, isMac)),
	]);

	const modified = Object.hasOwn(overrides, shortcut.id);
	const unassigned = effective.length === 0;
	const fixed = shortcut.customizable === false;

	return tokens.every((token) => {
		if (token === "modified") return modified;
		if (token === "unassigned") return unassigned;
		if (token === "fixed") return fixed;
		for (const value of haystack) {
			if (value.includes(token)) return true;
		}
		return false;
	});
}
