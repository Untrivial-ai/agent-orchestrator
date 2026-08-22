// Pure, deterministic matching for the `act` action: given a natural-language
// instruction ("the submit button") and a compact accessibility snapshot's text,
// resolve which `[ref=eN]` it refers to — or say plainly that it can't, so the
// caller can fall back to a real snapshot rather than guessing on a mutating
// action. No LLM call, no Electron/session imports, fully testable with plain
// fixture strings.

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

// Real fixtures observed in this codebase's own tests are inconsistent about
// quoting (`- button "Open" [ref=e1]` vs `button Save [ref=e1]`), so both a
// quoted and a bare name are accepted. Lines with no `[ref=eN]` (headings,
// static text, containers) are not candidates at all.
const SNAPSHOT_LINE = /-?\s*(?<role>[a-zA-Z][\w-]*)\s+(?:"(?<quoted>[^"]*)"|(?<bare>[^[]*?))\s*\[ref=(?<ref>e\d+)\]/;

export function parseSnapshotEntries(snapshotText: string): SnapshotEntry[] {
	const entries: SnapshotEntry[] = [];
	for (const line of snapshotText.split("\n")) {
		const match = SNAPSHOT_LINE.exec(line);
		if (!match?.groups) continue;
		const name = (match.groups.quoted ?? match.groups.bare ?? "").trim();
		entries.push({ role: match.groups.role, name, ref: match.groups.ref });
	}
	return entries;
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

function parseInstruction(instruction: string): { roleHint?: string; nameHint: string; nameTokens: Set<string> } {
	let tokens = tokenize(instruction);
	if (tokens.length > 0 && LEADING_VERBS.has(tokens[0])) tokens = tokens.slice(1);
	tokens = tokens.filter((token) => !ARTICLES.has(token));

	let roleHint: string | undefined;
	const roleIndex = tokens.findIndex((token) => ARIA_ROLES.has(token));
	if (roleIndex !== -1) {
		roleHint = tokens[roleIndex];
		tokens = [...tokens.slice(0, roleIndex), ...tokens.slice(roleIndex + 1)];
	}

	return { roleHint, nameHint: tokens.join(" "), nameTokens: new Set(tokens) };
}

function nameScore(entryName: string, nameHint: string, nameTokens: Set<string>): number {
	if (!nameHint) return 0;
	const lowerEntryName = entryName.toLowerCase();
	if (lowerEntryName === nameHint) return 10;
	if (lowerEntryName.includes(nameHint) || nameHint.includes(lowerEntryName)) return 6;

	const entryTokens = new Set(tokenize(entryName));
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

export function matchInstruction(instruction: string, snapshotText: string, opts: { nth?: number } = {}): ActMatchResult {
	const entries = parseSnapshotEntries(snapshotText);
	const { roleHint, nameHint, nameTokens } = parseInstruction(instruction);
	const candidates = scoreCandidates(entries, roleHint, nameHint, nameTokens);
	if (candidates.length === 0) return { outcome: "no-match" };

	const [top, second] = candidates;
	const isConfidentMatch =
		candidates.length === 1 ||
		(top.score >= EXACT_NAME_TIER && (!second || second.score < EXACT_NAME_TIER)) ||
		(top.score >= MATCH_SCORE_FLOOR && (!second || top.score - second.score >= MATCH_SCORE_GAP));

	if (isConfidentMatch) return { outcome: "matched", candidate: top };
	if (typeof opts.nth === "number" && opts.nth >= 0 && opts.nth < candidates.length) {
		return { outcome: "matched", candidate: candidates[opts.nth] };
	}
	return { outcome: "ambiguous", candidates: candidates.slice(0, MAX_AMBIGUOUS_CANDIDATES) };
}
