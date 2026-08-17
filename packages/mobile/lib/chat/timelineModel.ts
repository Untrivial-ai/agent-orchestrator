import type {
	ConversationActivity,
	ConversationItem,
	ConversationSnapshot,
	ConversationTurn,
} from "./types";

export type ConversationGroup = {
	key: string;
	turnId?: string;
	anchor: number;
	items: ConversationItem[];
	turn?: ConversationTurn;
};

export type ConversationMarker = {
	key: string;
	sequence: number;
	title: string;
	detail?: string;
	state?: ConversationTurn["state"];
};

export type ActivityNode = {
	activity: ConversationActivity;
	children: ActivityNode[];
};

export type ConversationTimelineRenderPlan =
	| { kind: "empty"; inverted: false; groups: [] }
	| { kind: "list"; inverted: true; groups: ConversationGroup[] };

/** Keep conversation signal while removing provider telemetry/noise. */
export function readableConversationItems(snapshot: ConversationSnapshot): ConversationItem[] {
	const plannedTurns = new Set(snapshot.turns.filter((turn) => turn.plan?.steps.length).map((turn) => turn.id));
	return snapshot.items.filter((item) => item.kind === "message" || (
		item.activityKind !== "usage" &&
		item.activityKind !== "reasoning" &&
		!(item.activityKind === "plan" && item.turnId && plannedTurns.has(item.turnId))
	));
}

/**
 * A turn remains one readable exchange even when queued-message sequencing
 * interleaves it with the turn currently running. This is presentation grouping
 * over daemon-owned sequence/turn identities, never inferred lifecycle state.
 */
export function groupConversationByTurn(
	snapshot: ConversationSnapshot,
	items = readableConversationItems(snapshot),
): ConversationGroup[] {
	const turns = new Map(snapshot.turns.map((turn) => [turn.id, turn]));
	const byTurn = new Map<string, ConversationGroup>();
	const groups: ConversationGroup[] = [];
	for (const item of items) {
		if (!item.turnId) {
			const previous = groups.at(-1);
			if (previous && !previous.turnId) previous.items.push(item);
			else groups.push({ key: `loose-${item.sequence}`, anchor: item.sequence, items: [item] });
			continue;
		}
		const existing = byTurn.get(item.turnId);
		if (existing) existing.items.push(item);
		else {
			// The first loaded item can move backward when an older page arrives. The
			// durable turn identity cannot, so use it as the React/FlatList key and
			// preserve expanded rows and scroll measurements across pagination.
			const group = { key: `turn-${item.turnId}`, turnId: item.turnId, anchor: item.sequence, items: [item], turn: turns.get(item.turnId) } satisfies ConversationGroup;
			byTurn.set(item.turnId, group);
			groups.push(group);
		}
	}
	return groups.sort((left, right) => left.anchor - right.anchor);
}

/** Latest-first data lets an inverted virtualized list paint the live edge first. */
export function latestFirstConversationGroups(
	snapshot: ConversationSnapshot,
	items = readableConversationItems(snapshot),
): ConversationGroup[] {
	return groupConversationByTurn(snapshot, items).reverse();
}

export function conversationTimelineRenderPlan(
	snapshot: ConversationSnapshot,
	items = readableConversationItems(snapshot),
): ConversationTimelineRenderPlan {
	const groups = latestFirstConversationGroups(snapshot, items);
	return groups.length === 0
		? { kind: "empty", inverted: false, groups: [] }
		: { kind: "list", inverted: true, groups };
}

export function conversationMarkers(snapshot: ConversationSnapshot): ConversationMarker[] {
	return groupConversationByTurn(snapshot).map((group) => {
		const human = group.items.find((item) => item.kind === "message" && item.role === "user" && item.origin === "human");
		const assistant = [...group.items].reverse().find((item) => item.kind === "message" && item.role === "assistant" && item.text.trim());
		const activity = group.items.find((item): item is ConversationActivity => item.kind === "activity");
		const title = previewText(human?.kind === "message" ? human.text : activity?.summary || "Conversation update", 120);
		const detailSource = assistant?.kind === "message" ? assistant.text : activity?.detail?.text || activity?.summary;
		const detail = detailSource ? previewText(String(detailSource), 240) : undefined;
		return { key: group.key, sequence: group.anchor, title, detail: detail && detail !== title ? detail : undefined, state: group.turn?.state };
	});
}

export function canRollbackTurn(snapshot: ConversationSnapshot, turn: ConversationTurn): boolean {
	return snapshot.capabilities?.includes("rollback") === true &&
		!snapshot.turns.some((candidate) => candidate.state === "running" || candidate.state === "queued") &&
		turn.state !== "running" && turn.state !== "queued" &&
		Boolean(turn.providerTurnId) &&
		!turn.rolledBack;
}

export function activityStartsExpanded(activity: ConversationActivity): boolean {
	const detail = activity.detail;
	const liveBody = activity.status === "running" && Boolean(
		detail?.output || detail?.result || detail?.error || detail?.patchOutput,
	);
	return activity.status === "failed" || liveBody;
}

/**
 * Reconstruct provider-owned nested agent work without inventing new lifecycle
 * state. Unknown parents and malformed cycles remain visible as roots.
 */
export function activityHierarchy(activities: ConversationActivity[]): ActivityNode[] {
	const byProvider = new Map<string, ActivityNode>();
	const nodes = activities.map((activity) => {
		const node = { activity, children: [] } satisfies ActivityNode;
		if (activity.providerItemId) byProvider.set(activity.providerItemId, node);
		return node;
	});
	const roots: ActivityNode[] = [];
	for (const node of nodes) {
		const parentId = node.activity.detail?.parentProviderItemId;
		const parent = parentId ? byProvider.get(parentId) : undefined;
		if (parent && !activityCycle(node, parent, byProvider)) parent.children.push(node);
		else roots.push(node);
	}
	return roots;
}

export function countActivityNodes(nodes: ActivityNode[]): number {
	return nodes.reduce((count, node) => count + 1 + countActivityNodes(node.children), 0);
}

export function activityNodesRunning(nodes: ActivityNode[]): boolean {
	return nodes.some((node) => node.activity.status === "running" || activityNodesRunning(node.children));
}

function activityCycle(node: ActivityNode, parent: ActivityNode, byProvider: Map<string, ActivityNode>): boolean {
	const visited = new Set<ActivityNode>([node]);
	let current: ActivityNode | undefined = parent;
	while (current) {
		if (visited.has(current)) return true;
		visited.add(current);
		const parentId: string | undefined = current.activity.detail?.parentProviderItemId;
		current = parentId ? byProvider.get(parentId) : undefined;
	}
	return false;
}

function previewText(value: string, limit: number): string {
	const plain = value
		.replace(/```[\s\S]*?```/g, " code sample ")
		.replace(/\[([^\]]+)]\([^)]*\)/g, "$1")
		.replace(/[*_`#>~]+/g, " ")
		.replace(/\s+/g, " ")
		.trim();
	return plain.length > limit ? `${plain.slice(0, limit - 1).trimEnd()}…` : plain;
}
