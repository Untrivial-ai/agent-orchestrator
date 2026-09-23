import type { ConversationItem, ConversationSnapshot, ConversationTurn } from "./types";

export type ConversationPage = ConversationSnapshot;

export type ConversationPageCache = {
	get(key: string): ConversationPage[] | undefined;
	set(key: string, pages: ConversationPage[]): void;
};

/**
 * Small memory-only cache for instant repeat opens. It deliberately retains
 * only the authoritative live page: user-requested historical pages can be
 * large and are cheap to request again when the user asks for them.
 */
export function createConversationPageCache(
	maxEntries: number,
	maxWeight = Number.POSITIVE_INFINITY,
	weigh: (page: ConversationPage) => number = estimateRetainedBytes,
): ConversationPageCache {
	const entries = new Map<string, { pages: ConversationPage[]; weight: number }>();
	const limit = Math.max(1, maxEntries);
	const weightLimit = Math.max(0, maxWeight);
	let retainedWeight = 0;
	return {
		get(key) {
			const entry = entries.get(key);
			if (!entry) return undefined;
			entries.delete(key);
			entries.set(key, entry);
			return [...entry.pages];
		},
		set(key, pages) {
			const live = pages[0];
			const previous = entries.get(key);
			if (previous) retainedWeight -= previous.weight;
			entries.delete(key);
			if (!live) {
				return;
			}
			const weight = Math.max(0, weigh(live));
			if (weight > weightLimit) return;
			entries.set(key, { pages: [live], weight });
			retainedWeight += weight;
			while (entries.size > limit || retainedWeight > weightLimit) {
				const oldest = entries.keys().next().value as string | undefined;
				if (oldest === undefined) break;
				retainedWeight -= entries.get(oldest)?.weight ?? 0;
				entries.delete(oldest);
			}
		},
	};
}

export function createMobileConversationPageCache(
	weigh?: (page: ConversationPage) => number,
): ConversationPageCache {
	return createConversationPageCache(16, 2 * 1024 * 1024, weigh);
}

function estimateRetainedBytes(value: unknown): number {
	const pending: unknown[] = [value];
	const seen = new Set<object>();
	let bytes = 0;
	while (pending.length) {
		const current = pending.pop();
		if (typeof current === "string") {
			bytes += current.length * 2;
		} else if (typeof current === "number" || typeof current === "boolean") {
			bytes += 8;
		} else if (current && typeof current === "object" && !seen.has(current)) {
			seen.add(current);
			for (const [key, child] of Object.entries(current)) {
				bytes += key.length * 2 + 16;
				pending.push(child);
			}
		}
	}
	return bytes;
}

export function cachedConversationState(cache: ConversationPageCache, key: string): {
	pages: ConversationPage[];
	loading: boolean;
} {
	const pages = cache.get(key) ?? [];
	return { pages, loading: pages.length === 0 };
}

/**
 * A rollback rewrites the projection by removing rows. A live first page cannot
 * carry tombstones for rows cached in older pages, so those pages must be
 * discarded before the authoritative refresh and may be paged in again later.
 */
export function discardHistoricalPages(pages: ConversationPage[]): ConversationPage[] {
	return pages.length ? pages.slice(0, 1) : pages;
}

/** Merge historical pages behind the current live page without duplicating revisions. */
export function mergeConversationPages(pages: ConversationPage[]): ConversationSnapshot | undefined {
	const live = pages[0];
	if (!live) return undefined;
	const items = new Map<string, ConversationItem>();
	const turns = new Map<string, ConversationTurn>();
	// Oldest first, so newer pages replace the same item/turn with its latest
	// revision when a page boundary overlaps a streaming update.
	for (const page of [...pages].reverse()) {
		for (const item of page.items) items.set(`${item.kind}:${item.id}`, item);
		for (const turn of page.turns) turns.set(turn.id, turn);
	}
	const oldest = pages[pages.length - 1] ?? live;
	return {
		...live,
		oldestSequence: oldest.oldestSequence,
		hasMoreBefore: oldest.hasMoreBefore,
		items: [...items.values()].sort((a, b) => a.sequence - b.sequence),
		turns: [...turns.values()].sort((a, b) => a.requestedAt.localeCompare(b.requestedAt)),
	};
}
