import type { ChatSkill } from "./types";

export type ComposerSuggestion = { kind: "skills" | "files"; query: string; start: number; end: number };

export function composerSuggestionKey(suggestion: ComposerSuggestion): string {
	return `${suggestion.kind}:${suggestion.start}:${suggestion.end}:${suggestion.query}`;
}

export type RankedSuggestion = {
	value: string;
	label: string;
	detail?: string;
	badge?: string;
};

const MAX_SUGGESTIONS = 80;

export type ComposerPickerCatalog =
	| { kind: "skills"; skills: readonly ChatSkill[] }
	| { kind: "files"; paths: readonly string[] };

export function rankComposerCatalog(catalog: ComposerPickerCatalog, query: string): RankedSuggestion[] {
	return catalog.kind === "skills"
		? rankComposerSkills(catalog.skills, query)
		: rankComposerFiles(catalog.paths, query);
}

/**
 * Match the desktop composer's text contract exactly.
 *
 * A skill is a slash command and therefore only starts at the beginning of the
 * message (ignoring leading whitespace). A file may be mentioned after any
 * whitespace boundary. Email addresses, URLs and ordinary paths remain text.
 */
export function findComposerSuggestion(text: string, cursor = text.length): ComposerSuggestion | undefined {
	const caret = Math.max(0, Math.min(cursor, text.length));
	for (let index = caret - 1; index >= 0; index -= 1) {
		const char = text[index];
		if (/\s/.test(char)) return undefined;
		if (char !== "/" && char !== "@") continue;
		const preceding = index > 0 ? text[index - 1] : undefined;
		// Slashes inside an @ path and @ signs inside an ordinary token are not
		// triggers. Keep scanning toward the token boundary for the real sigil.
		if (preceding !== undefined && !/\s/.test(preceding)) continue;
		if (char === "/" && text.slice(0, index).trim() !== "") return undefined;
		return {
			kind: char === "/" ? "skills" : "files",
			query: text.slice(index + 1, caret),
			start: index,
			end: caret,
		};
	}
	return undefined;
}

/** Skills keep their slash; file paths are ordinary path text and lose `@`. */
export function replaceComposerSuggestion(text: string, trigger: ComposerSuggestion, value: string): string {
	const inserted = trigger.kind === "skills" ? `/${value}` : quotePath(value);
	const suffix = text.slice(trigger.end);
	const separator = suffix && /^\s/.test(suffix) ? "" : " ";
	return `${text.slice(0, trigger.start)}${inserted}${separator}${suffix}`;
}

export function rankComposerSkills(skills: readonly ChatSkill[], query: string): RankedSuggestion[] {
	const needle = query.trim().toLowerCase();
	return skills
		.flatMap((skill) => {
			if (!skill.name) return [];
			const scores = [
				score(skill.name, needle, 0),
				score(skill.displayName || skill.name, needle, 10),
				needle ? score(skill.description ?? "", needle, 40) : undefined,
				needle ? score(skill.inputHint ?? "", needle, 50) : undefined,
			].filter((value): value is number => value !== undefined);
			if (!scores.length) return [];
			const description = firstLine(skill.description);
			const hint = firstLine(skill.inputHint);
			return [{
				score: Math.min(...scores),
				name: skill.name,
				value: skill.name,
				label: skill.displayName || skill.name,
				detail: description && hint ? `${description} · ${hint}` : description || hint,
				badge: skill.source,
			}];
		})
		.sort((left, right) => left.score - right.score || left.name.localeCompare(right.name))
		.slice(0, MAX_SUGGESTIONS)
		.map(({ score: _score, name: _name, ...suggestion }) => suggestion);
}

export function rankComposerFiles(paths: readonly string[], query: string): RankedSuggestion[] {
	const needle = query.trim().toLowerCase();
	return paths
		.flatMap((path) => {
			if (!path) return [];
			const slash = path.lastIndexOf("/");
			const basename = slash < 0 ? path : path.slice(slash + 1);
			const parent = slash < 0 ? undefined : path.slice(0, slash);
			const scores = [score(basename, needle, 0), score(path, needle, 10)].filter(
				(value): value is number => value !== undefined,
			);
			if (!scores.length) return [];
			return [{ score: Math.min(...scores), path, value: path, label: basename, detail: parent }];
		})
		.sort((left, right) => left.score - right.score || left.path.length - right.path.length || left.path.localeCompare(right.path))
		.slice(0, MAX_SUGGESTIONS)
		.map(({ score: _score, path: _path, ...suggestion }) => suggestion);
}

function score(value: string, query: string, base: number): number | undefined {
	const haystack = value.toLowerCase();
	if (!query || haystack === query) return base;
	if (haystack.startsWith(query)) return base + 1;
	if (["-", "_", "/", ":", "."].some((marker) => haystack.includes(marker + query))) return base + 2;
	const index = haystack.indexOf(query);
	return index < 0 ? undefined : base + 3 + Math.min(index, 20) / 100;
}

function quotePath(path: string): string {
	return /\s/.test(path) ? `"${path}"` : path;
}

function firstLine(value?: string): string | undefined {
	const line = value?.split("\n", 1)[0]?.trim();
	return line || undefined;
}
