export type MobileSnapshotEntry = { role: string; name: string; ref: string };
export type MobileActCandidate = MobileSnapshotEntry & { score: number };
export type MobileActMatch =
	| { outcome: "matched"; candidate: MobileActCandidate }
	| { outcome: "ambiguous"; candidates: MobileActCandidate[] }
	| { outcome: "no-match" };

const ROLES = new Set([
	"button", "link", "textbox", "checkbox", "radio", "combobox", "listbox",
	"option", "tab", "menuitem", "heading", "image", "dialog", "switch",
	"slider", "searchbox",
]);
const VERBS = new Set(["click", "tap", "press", "fill", "type", "select", "check", "uncheck", "hover", "focus"]);
const ARTICLES = new Set(["the", "a", "an"]);

function tokens(text: string): string[] {
	return text.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
}

function parsedInstruction(instruction: string, stripVerb = false) {
	let parts = tokens(instruction);
	if (stripVerb && VERBS.has(parts[0])) parts = parts.slice(1);
	parts = parts.filter((part) => !ARTICLES.has(part));
	let role: string | undefined;
	if (ROLES.has(parts.at(-1) ?? "")) role = parts.pop();
	return { role, name: parts.join(" "), names: new Set(parts) };
}

function nameScore(name: string, hint: string, hints: Set<string>): number {
	if (!hint) return 0;
	const normalized = tokens(name).join(" ");
	if (!normalized) return 0;
	if (normalized === hint) return 10;
	if (` ${normalized} `.includes(` ${hint} `) || ` ${hint} `.includes(` ${normalized} `)) return 6;
	const values = new Set(normalized.split(" "));
	const intersection = [...values].filter((value) => hints.has(value)).length;
	const union = new Set([...values, ...hints]).size;
	return union ? Math.round((intersection / union) * 4) : 0;
}

function entries(refs: unknown): MobileSnapshotEntry[] {
	if (!refs || typeof refs !== "object" || Array.isArray(refs)) return [];
	return Object.entries(refs).flatMap(([ref, value]) => {
		if (!/^e\d+$/.test(ref) || !value || typeof value !== "object" || Array.isArray(value)) return [];
		const { role, name } = value as { role?: unknown; name?: unknown };
		return typeof role === "string" && typeof name === "string" ? [{ role, name, ref }] : [];
	}).sort((a, b) => Number(a.ref.slice(1)) - Number(b.ref.slice(1)));
}

/** Keep mobile `act` matching identical to the desktop browser's deterministic policy. */
export function matchMobileBrowserInstruction(instruction: string, refs: unknown, nth?: number): MobileActMatch {
	const source = entries(refs);
	const instructions = [parsedInstruction(instruction)];
	if (VERBS.has(tokens(instruction)[0])) instructions.push(parsedInstruction(instruction, true));
	const roleOnly = instructions.find(({ role, name }) => Boolean(role) && !name);
	const byRef = new Map<string, MobileActCandidate>();
	for (const parsed of roleOnly ? [roleOnly] : instructions) {
		for (const entry of source) {
			const score = (parsed.role === entry.role ? 3 : 0) + nameScore(entry.name, parsed.name, parsed.names);
			if (!score) continue;
			const candidate = { ...entry, score };
			if ((byRef.get(entry.ref)?.score ?? -1) < score) byRef.set(entry.ref, candidate);
		}
	}
	const candidates = [...byRef.values()].sort((a, b) => b.score - a.score);
	if (!candidates.length) return { outcome: "no-match" };
	const [top, second] = candidates;
	const confident =
		(top.score >= 10 && (!second || second.score < 10)) ||
		(top.score >= 4 && (!second || top.score - second.score >= 3));
	if (confident) return { outcome: "matched", candidate: top };
	if (roleOnly && typeof nth === "number" && nth >= 0 && nth < candidates.length) return { outcome: "matched", candidate: candidates[nth] };
	if (candidates.length === 1 && top.score < 4) return { outcome: "no-match" };
	if (typeof nth === "number" && nth >= 0 && nth < candidates.length) return { outcome: "matched", candidate: candidates[nth] };
	return { outcome: "ambiguous", candidates: candidates.slice(0, 5) };
}
