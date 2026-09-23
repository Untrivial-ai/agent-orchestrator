/**
 * The composer's suggestion logic, kept out of the component.
 *
 * Everything here is a pure function of (text, caret) plus the candidate lists, so
 * the parts that are easy to get wrong — where a trigger starts, what a selection
 * replaces, where the caret lands — are testable without a DOM or a provider.
 *
 * The editor renders accepted suggestions as atomic chips, but trigger matching
 * and ranking stay plain-text operations here. Keeping that policy independent
 * of Lexical makes the menu deterministic and lets the wire representation remain
 * ordinary text for the agent.
 */

import type { ChatSkill } from "../../types/conversation";

/** What kind of thing the caret is currently completing. */
export type TriggerKind = "skill" | "file";

/**
 * An in-progress trigger the caret sits inside.
 *
 * `start` is the index of the trigger character itself, so a replacement can
 * overwrite the sigil along with the query.
 */
export interface ActiveTrigger {
	kind: TriggerKind;
	/** Index of the `/` or `@`. */
	start: number;
	/** Text typed after the sigil, up to the caret. */
	query: string;
}

/**
 * Characters that end a trigger. A query cannot contain whitespace: once the user
 * types a space they have moved on to prose, and holding the menu open would make
 * the next Enter select a skill instead of sending the message.
 */
const TRIGGER_BOUNDARY = /\s/;

/**
 * Find the trigger the caret is inside, if any.
 *
 * A sigil only counts at the start of the text or after whitespace. Without that
 * rule an email address or a path like `src/app` would open a menu mid-word, and
 * `and/or` would hijack Enter. Both commands and file mentions can be added after
 * existing prose.
 */
export function findActiveTrigger(text: string, caret: number): ActiveTrigger | undefined {
	for (let i = caret - 1; i >= 0; i -= 1) {
		const char = text[i]!;
		if (TRIGGER_BOUNDARY.test(char)) return undefined;
		if (char !== "/" && char !== "@") continue;

		const preceding = i > 0 ? text[i - 1]! : undefined;
		const atBoundary = preceding === undefined || TRIGGER_BOUNDARY.test(preceding);
		if (!atBoundary) return undefined;

		return {
			kind: char === "/" ? "skill" : "file",
			start: i,
			query: text.slice(i + 1, caret),
		};
	}
	return undefined;
}

/**
 * Score a candidate against a query. Lower is better; `null` means no match.
 *
 * The bands (prefix, word boundary, substring) are what make the ordering feel
 * intentional rather than alphabetical: typing "rev" should put `review` above
 * `code-review`, and both above something that only mentions review in its
 * description.
 */
function score(value: string, query: string, base: number): number | null {
	const haystack = value.toLowerCase();
	if (query === "") return base;
	if (haystack === query) return base;
	if (haystack.startsWith(query)) return base + 1;

	// A match after a separator reads as the start of a word to a person.
	for (const marker of ["-", "_", "/", ":", "."]) {
		if (haystack.includes(marker + query)) return base + 2;
	}
	const at = haystack.indexOf(query);
	if (at >= 0) return base + 3 + Math.min(at, 20) / 100;
	return null;
}

/** One row in the suggestion menu. */
export interface Suggestion {
	/** Stable key, and the value a selection inserts. */
	value: string;
	/** The label shown. */
	label: string;
	/** Secondary line, when there is something worth saying. */
	detail?: string;
	/** Right-aligned provenance, e.g. a skill's scope. */
	badge?: string;
}

/**
 * How many ranked rows the menu will hold.
 *
 * Set well above what fits so the panel's own scrollbar is what tells the user
 * there is more: a hard cap at the visible row count showed 8 of 99 installed
 * skills with nothing to suggest the rest existed. The cap is still there to stop a
 * five-thousand-file worktree from rendering itself into the menu.
 */
export const MAX_SUGGESTIONS = 50;

/**
 * Rank skills for a `/` query.
 *
 * Matching runs over the invocable name, the label, and the description — a user
 * who remembers what a skill does but not what it is called still finds it — but
 * a name match always outranks a description match.
 */
export function rankSkills(skills: readonly ChatSkill[], query: string): Suggestion[] {
	const needle = query.trim().toLowerCase();
	const scored: { suggestion: Suggestion; score: number; name: string }[] = [];

	for (const skill of skills) {
		if (!skill.name) continue;
		const candidates = [
			score(skill.name, needle, 0),
			score(skill.displayName || skill.name, needle, 10),
			// Only searched when the user typed something; an empty query must not be
			// ranked by description text.
			needle === "" ? null : score(skill.description ?? "", needle, 40),
			needle === "" ? null : score(skill.inputHint ?? "", needle, 50),
		].filter((value): value is number => value !== null);
		if (candidates.length === 0) continue;

		scored.push({
			score: Math.min(...candidates),
			name: skill.name,
			suggestion: {
				value: skill.name,
				label: skill.displayName || skill.name,
				detail: skillDetail(skill),
				badge: skill.source,
			},
		});
	}

	// Name is the tie-break so the order is stable across renders: two skills with
	// the same score must not swap places as the list is refetched.
	scored.sort((a, b) => a.score - b.score || a.name.localeCompare(b.name));
	return scored.slice(0, MAX_SUGGESTIONS).map((entry) => entry.suggestion);
}

function skillDetail(skill: ChatSkill): string | undefined {
	const description = firstLine(skill.description);
	const hint = firstLine(skill.inputHint);
	if (description && hint) return `${description} · ${hint}`;
	return description || hint;
}

/**
 * Rank worktree paths for an `@` query.
 *
 * The basename is scored ahead of the full path: people search for `ChatComposer`,
 * not for the directories above it. The full path is still matched so a directory
 * fragment narrows the list.
 */
export function rankFiles(paths: readonly string[], query: string): Suggestion[] {
	const needle = query.trim().toLowerCase();
	const scored: { suggestion: Suggestion; score: number; path: string }[] = [];

	for (const path of paths) {
		if (!path) continue;
		// Split explicitly rather than with slice(lastIndexOf(...)): a file at the repo
		// root has no slash, and the negative index silently truncates its own name
		// into a parent directory that does not exist.
		const slash = path.lastIndexOf("/");
		const base = slash < 0 ? path : path.slice(slash + 1);
		const parent = slash < 0 ? "" : path.slice(0, slash);

		const candidates = [score(base, needle, 0), score(path, needle, 10)].filter(
			(value): value is number => value !== null,
		);
		if (candidates.length === 0) continue;

		scored.push({
			score: Math.min(...candidates),
			path,
			// The row reads as "name, then where it lives", but the value inserted is
			// always the whole path — the label is never what the agent resolves.
			suggestion: { value: path, label: base, detail: parent || undefined },
		});
	}

	// Shorter paths first among equals: a file at the repo root is more often what
	// was meant than one buried six directories deep.
	scored.sort(
		(a, b) =>
			a.score - b.score || a.path.length - b.path.length || a.path.localeCompare(b.path),
	);
	return scored.slice(0, MAX_SUGGESTIONS).map((entry) => entry.suggestion);
}

function firstLine(text: string | undefined): string | undefined {
	if (!text) return undefined;
	const line = text.split("\n", 1)[0]!.trim();
	return line === "" ? undefined : line;
}

/**
 * Move the highlighted row, wrapping at both ends.
 *
 * Wrapping matters for a short list reached by keyboard: pressing Up on the first
 * row should reach the last one rather than doing nothing.
 */
export function moveHighlight(current: number, delta: number, count: number): number {
	if (count === 0) return 0;
	return (current + delta + count) % count;
}
