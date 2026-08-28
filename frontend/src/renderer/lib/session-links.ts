export type SessionLinkTarget = { projectId: string; sessionId: string };
export type SessionLinkWorkspace = { id: string; sessions: Array<{ id: string }> };

const SESSION_LINK_PREFIX = "ao://sessions/";
const RAW_SESSION_LINK = /ao:\/\/sessions\/\S+/g;
const TRAILING_PUNCTUATION = /[.,;:!?]+$/;

/** Strictly parse the only in-app URL route AO supports. */
export function parseSessionLink(value: string): SessionLinkTarget | undefined {
	if (!value.startsWith(SESSION_LINK_PREFIX) || value.includes("?") || value.includes("#")) return undefined;
	const segments = value.slice(SESSION_LINK_PREFIX.length).split("/");
	if (segments.length !== 2 || segments.some((segment) => segment.length === 0)) return undefined;
	try {
		const [projectId, sessionId] = segments.map(decodeURIComponent);
		if (!projectId || !sessionId || projectId.includes("/") || sessionId.includes("/")) return undefined;
		return { projectId, sessionId };
	} catch {
		return undefined;
	}
}

export function isSessionLink(value: string): boolean {
	return parseSessionLink(value) !== undefined;
}

export function resolveSessionLink(value: string, workspaces: SessionLinkWorkspace[]): SessionLinkTarget | undefined {
	const target = parseSessionLink(value);
	if (!target) return undefined;
	const project = workspaces.find((workspace) => workspace.id === target.projectId);
	return project?.sessions.some((session) => session.id === target.sessionId) ? target : undefined;
}

/** Find raw links without swallowing prose punctuation adjacent to them. */
export function findSessionLinks(text: string): Array<{ start: number; end: number; text: string }> {
	const matches: Array<{ start: number; end: number; text: string }> = [];
	for (const match of text.matchAll(RAW_SESSION_LINK)) {
		let candidate = match[0].replace(TRAILING_PUNCTUATION, "");
		while (candidate.endsWith(")") && count(candidate, "(") < count(candidate, ")")) candidate = candidate.slice(0, -1);
		if (!parseSessionLink(candidate)) continue;
		matches.push({ start: match.index, end: match.index + candidate.length, text: candidate });
	}
	return matches;
}

function count(value: string, character: string): number {
	return [...value].filter((entry) => entry === character).length;
}

type MarkdownNode = { type: string; value?: string; url?: string; children?: MarkdownNode[] };

/** remark plugin that linkifies only raw canonical session URLs outside code. */
export function remarkSessionLinks() {
	return (tree: MarkdownNode) => visitMarkdown(tree);
}

function visitMarkdown(node: MarkdownNode): void {
	if (!node.children || node.type === "code" || node.type === "inlineCode" || node.type === "link") return;
	for (let index = 0; index < node.children.length; index += 1) {
		const child = node.children[index]!;
		if (child.type !== "text" || !child.value) {
			visitMarkdown(child);
			continue;
		}
		const links = findSessionLinks(child.value);
		if (links.length === 0) continue;
		const replacement: MarkdownNode[] = [];
		let cursor = 0;
		for (const link of links) {
			if (link.start > cursor) replacement.push({ type: "text", value: child.value.slice(cursor, link.start) });
			replacement.push({ type: "link", url: link.text, children: [{ type: "text", value: link.text }] });
			cursor = link.end;
		}
		if (cursor < child.value.length) replacement.push({ type: "text", value: child.value.slice(cursor) });
		node.children.splice(index, 1, ...replacement);
		index += replacement.length - 1;
	}
}
