// Pure, deterministic matching for the `act` action: given a natural-language
// instruction ("the submit button") and agent-browser's structured refs map,
// resolve which ref it refers to — or say plainly that it can't, so the caller
// can return the real snapshot rather than guessing on a mutating action. No
// LLM call and no Electron/session imports.

export type SnapshotEntry = {
	role: string;
	name: string;
	ref: string;
};

export type ActCandidate = {
	role: string;
	name: string;
	ref: string;
	score: number;
};

export type ActMatchResult =
	| { outcome: "matched"; candidate: ActCandidate }
	| { outcome: "ambiguous"; candidates: ActCandidate[] }
	| { outcome: "no-match" };

export function parseSnapshotEntries(snapshotRefs: unknown): SnapshotEntry[] {
	const entries: SnapshotEntry[] = [];
	if (!snapshotRefs || typeof snapshotRefs !== "object" || Array.isArray(snapshotRefs)) return entries;
	for (const [ref, value] of Object.entries(snapshotRefs)) {
		if (!/^e\d+$/.test(ref) || !value || typeof value !== "object" || Array.isArray(value)) continue;
		const { role, name } = value as { role?: unknown; name?: unknown };
		if (typeof role !== "string" || typeof name !== "string") continue;
		entries.push({ role, name, ref });
	}
	return entries.sort((a, b) => Number(a.ref.slice(1)) - Number(b.ref.slice(1)));
}

const ARIA_ROLES = new Set([
	"button", "link", "textbox", "checkbox", "radio", "combobox", "listbox",
	"option", "tab", "menuitem", "heading", "image", "dialog", "switch",
	"slider", "searchbox",
]);
const LEADING_VERBS = new Set(["click", "tap", "press", "fill", "type", "select", "check", "uncheck", "hover", "focus"]);
const ARTICLES = new Set(["the", "a", "an"]);

function tokenize(text: string): string[] {
	return text
		.toLowerCase()
		.split(/[^a-z0-9]+/)
		.filter(Boolean);
}

function parseInstruction(instruction: string, stripLeadingVerb = false): { roleHint?: string; nameHint: string; nameTokens: Set<string> } {
	let tokens = tokenize(instruction);
	if (stripLeadingVerb && tokens.length > 0 && LEADING_VERBS.has(tokens[0])) tokens = tokens.slice(1);
	tokens = tokens.filter((token) => !ARTICLES.has(token));

	let roleHint: string | undefined;
	const roleIndex = tokens.length - 1;
	if (roleIndex >= 0 && ARIA_ROLES.has(tokens[roleIndex])) {
		roleHint = tokens[roleIndex];
		tokens = tokens.slice(0, roleIndex);
	}

	return { roleHint, nameHint: tokens.join(" "), nameTokens: new Set(tokens) };
}

function nameScore(entryName: string, nameHint: string, nameTokens: Set<string>): number {
	if (!nameHint) return 0;
	const normalizedEntryName = tokenize(entryName).join(" ");
	if (!normalizedEntryName) return 0;
	if (normalizedEntryName === nameHint) return 10;
	const paddedEntryName = ` ${normalizedEntryName} `;
	const paddedNameHint = ` ${nameHint} `;
	if (paddedEntryName.includes(paddedNameHint) || paddedNameHint.includes(paddedEntryName)) return 6;

	const entryTokens = new Set(normalizedEntryName.split(" "));
	if (entryTokens.size === 0 || nameTokens.size === 0) return 0;
	const intersection = [...entryTokens].filter((token) => nameTokens.has(token)).length;
	const union = new Set([...entryTokens, ...nameTokens]).size;
	return union === 0 ? 0 : Math.round((intersection / union) * 4);
}

function scoreCandidates(entries: SnapshotEntry[], roleHint: string | undefined, nameHint: string, nameTokens: Set<string>): ActCandidate[] {
	const candidates: ActCandidate[] = [];
	for (const entry of entries) {
		const roleScore = roleHint && entry.role === roleHint ? 3 : 0;
		const score = roleScore + nameScore(entry.name, nameHint, nameTokens);
		if (score > 0) candidates.push({ ...entry, score });
	}
	return candidates.sort((a, b) => b.score - a.score);
}

const MAX_AMBIGUOUS_CANDIDATES = 5;
const EXACT_NAME_TIER = 10;
const MATCH_SCORE_FLOOR = 4;
const MATCH_SCORE_GAP = 3;

export function matchInstruction(instruction: string, snapshotRefs: unknown, opts: { nth?: number } = {}): ActMatchResult {
	const entries = parseSnapshotEntries(snapshotRefs);
	const instructions = [parseInstruction(instruction)];
	if (LEADING_VERBS.has(tokenize(instruction)[0])) instructions.push(parseInstruction(instruction, true));
	const roleOnlyInstruction = instructions.find(({ roleHint, nameHint }) => Boolean(roleHint) && !nameHint);
	const candidatesByRef = new Map<string, ActCandidate>();
	for (const { roleHint, nameHint, nameTokens } of roleOnlyInstruction ? [roleOnlyInstruction] : instructions) {
		for (const candidate of scoreCandidates(entries, roleHint, nameHint, nameTokens)) {
			const previous = candidatesByRef.get(candidate.ref);
			if (!previous || candidate.score > previous.score) candidatesByRef.set(candidate.ref, candidate);
		}
	}
	const candidates = [...candidatesByRef.values()].sort((a, b) => b.score - a.score);
	if (candidates.length === 0) return { outcome: "no-match" };

	const [top, second] = candidates;
	const isConfidentMatch =
		(top.score >= EXACT_NAME_TIER && (!second || second.score < EXACT_NAME_TIER)) ||
		(top.score >= MATCH_SCORE_FLOOR && (!second || top.score - second.score >= MATCH_SCORE_GAP));

	if (isConfidentMatch) return { outcome: "matched", candidate: top };
	if (roleOnlyInstruction && typeof opts.nth === "number" && opts.nth >= 0 && opts.nth < candidates.length) {
		return { outcome: "matched", candidate: candidates[opts.nth] };
	}
	if (candidates.length === 1 && top.score < MATCH_SCORE_FLOOR) return { outcome: "no-match" };
	if (typeof opts.nth === "number" && opts.nth >= 0 && opts.nth < candidates.length) {
		return { outcome: "matched", candidate: candidates[opts.nth] };
	}
	return { outcome: "ambiguous", candidates: candidates.slice(0, MAX_AMBIGUOUS_CANDIDATES) };
}
