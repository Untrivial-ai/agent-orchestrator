import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { getApiBaseUrl, hasTrustedApiBaseUrl, subscribeApiBaseUrl } from "../lib/api-client";
import { computeSseRetryDelayMs } from "../lib/sse-backoff";
import type { ConversationMessage, ConversationSnapshot } from "../types/conversation";

type LiveFrame = components["schemas"]["ConversationLiveResponse"];
const MAX_EVENTS = 4096;
const MAX_TEXT = 1024 * 1024;

// This journal is separate from the durable query cache. Every render replays
// only observations after that snapshot's checkpoint, including on reconnect.
export function mergeConversationLiveFrame(previous: LiveFrame | undefined, frame: LiveFrame): LiveFrame {
	const continuous = previous && previous.generation === frame.generation &&
		previous.branchId === frame.branchId && previous.conversationId === frame.conversationId &&
		frame.afterSequence <= previous.sequence;
	if (continuous && frame.sequence < previous.sequence) return previous;
	let events = continuous
		? [...previous.events, ...frame.events.filter((event) => event.sequence > previous.sequence)]
		: frame.events;
	let afterSequence = continuous ? previous.afterSequence : frame.afterSequence;
	let textSize = events.reduce((size, event) => size + (event.delta?.length ?? 0) + (event.text?.length ?? 0), 0);
	let discard = 0;
	while (events.length - discard > MAX_EVENTS || textSize > MAX_TEXT) {
		const event = events[discard++];
		afterSequence = event.sequence;
		textSize -= (event.delta?.length ?? 0) + (event.text?.length ?? 0);
	}
	if (discard) events = events.slice(discard);
	return { ...frame, afterSequence, events };
}

export function conversationLiveNeedsSnapshot(snapshot: ConversationSnapshot | undefined, live: LiveFrame | undefined): boolean {
	if (!snapshot || !live) return false;
	if (snapshot.liveGeneration !== live.generation || snapshot.conversationId !== live.conversationId ||
		(snapshot.activeBranchId ?? "") !== live.branchId ||
		(snapshot.liveSequence ?? 0) < Math.max(live.afterSequence, live.resetSequence)) return true;
	const affectedItems = new Set(live.events.map((event) => event.providerItemId));
	const affectedTurns = new Set(live.events.filter((event) => event.kind === "turn.completed" && event.providerTurnId).map((event) => event.providerTurnId));
	return snapshot.items.some((item) => item.kind === "message" && item.providerItemId &&
		(affectedItems.has(item.providerItemId) || affectedTurns.has(snapshot.turns.find((turn) => turn.id === item.turnId)?.providerTurnId)) &&
		item.liveGeneration !== undefined &&
		(item.liveGeneration !== live.generation || (item.liveSequence ?? 0) < live.afterSequence));
}

export function applyConversationLive(snapshot: ConversationSnapshot | undefined, live: LiveFrame | undefined): ConversationSnapshot | undefined {
	if (!snapshot || !live || conversationLiveNeedsSnapshot(snapshot, live)) return snapshot;
	const messages = new Map<string, ConversationMessage>();
	for (const item of snapshot.items) {
		if (item.kind === "message" && item.providerItemId) messages.set(item.providerItemId, item);
	}
	const changed = new Map<string, ConversationMessage>();
	const liveTurns = new Map<string, string | undefined>();
	const checkpoint = (message?: ConversationMessage) => message?.liveGeneration === live.generation
		? (message.liveSequence ?? 0) : (snapshot.liveSequence ?? 0);
	for (const event of live.events) {
		if (event.kind === "turn.completed") {
			if (!event.providerTurnId) continue;
			const turn = snapshot.turns.find((turn) => turn.providerTurnId === event.providerTurnId);
			for (const [key, item] of messages) {
				if (event.sequence > checkpoint(item) && item.streaming &&
					((turn && item.turnId === turn.id) || (event.providerTurnId && liveTurns.get(key) === event.providerTurnId))) {
					const settled = { ...item, streaming: false };
					messages.set(key, settled);
					changed.set(key, settled);
				}
			}
			continue;
		}
		if (!event.providerItemId) continue;
		liveTurns.set(event.providerItemId, event.providerTurnId);
		const current = messages.get(event.providerItemId);
		if (event.sequence <= checkpoint(current)) continue;
		const message: ConversationMessage = {
			...(current ?? {
				kind: "message", id: `live/${live.generation}/${event.providerItemId}`,
				providerItemId: event.providerItemId,
				turnId: event.providerTurnId ? snapshot.turns.find((turn) => turn.providerTurnId === event.providerTurnId)?.id : undefined,
				// Provisional rows follow durable rows until SQLite assigns their sequence.
				sequence: snapshot.latestSequence + event.sequence, revision: 0,
				role: "assistant", origin: "provider", createdAt: event.createdAt,
			}),
			text: event.kind === "message.completed" ? (event.text ?? "") : (current?.text ?? "") + (event.delta ?? ""),
			streaming: event.kind !== "message.completed",
		};
		messages.set(event.providerItemId, message);
		changed.set(event.providerItemId, message);
	}
	if (!changed.size) return snapshot;
	const items = snapshot.items.map((item) => {
		if (item.kind !== "message" || !item.providerItemId) return item;
		const replacement = changed.get(item.providerItemId);
		changed.delete(item.providerItemId);
		return replacement ?? item;
	});
	return { ...snapshot, items: [...items, ...changed.values()] };
}

export function useConversationLive(sessionId: string | undefined, snapshot: ConversationSnapshot | undefined) {
	const queryClient = useQueryClient();
	const resync = useRef<{ sessionId: string; request: Promise<void> } | undefined>(undefined);
	const [received, setReceived] = useState<{ sessionId: string; frame: LiveFrame }>();
	const live = received && received.sessionId === sessionId ? received.frame : undefined;
	useEffect(() => {
		if (!sessionId || typeof EventSource === "undefined") return;
		let source: EventSource | undefined;
		let timer: ReturnType<typeof setTimeout> | undefined;
		let disposed = false;
		let attempts = 0;
		const refresh = () => void queryClient.invalidateQueries({ queryKey: ["conversation", sessionId] });
		const connect = () => {
			if (disposed || !hasTrustedApiBaseUrl()) return;
			try {
				const current = new EventSource(`${getApiBaseUrl()}/api/v1/sessions/${encodeURIComponent(sessionId)}/conversation/events`);
				source = current;
				current.onopen = () => {
					if (disposed || source !== current) return;
					attempts = 0;
					refresh();
				};
				current.addEventListener("conversation_text", (event) => {
					if (disposed || source !== current) return;
					try {
						const frame = JSON.parse((event as MessageEvent).data) as LiveFrame;
						if (!Array.isArray(frame.events) || typeof frame.generation !== "string" ||
							!Number.isSafeInteger(frame.sequence) || !Number.isSafeInteger(frame.afterSequence)) return;
						setReceived((previous) => ({ sessionId, frame: mergeConversationLiveFrame(
							previous?.sessionId === sessionId ? previous.frame : undefined, frame,
						) }));
					} catch { /* An invalid transient frame cannot change durable history. */ }
				});
				current.onerror = () => {
					if (disposed || source !== current) return;
					current.close();
					source = undefined;
					refresh();
					timer = setTimeout(connect, computeSseRetryDelayMs(++attempts));
				};
			} catch {
				timer = setTimeout(connect, computeSseRetryDelayMs(++attempts));
			}
		};
		const unsubscribe = subscribeApiBaseUrl(() => {
			source?.close();
			source = undefined;
			clearTimeout(timer);
			setReceived(undefined);
			void queryClient.cancelQueries({ queryKey: ["conversation", sessionId] }).then(refresh);
			connect();
		});
		connect();
		return () => {
			disposed = true;
			unsubscribe();
			clearTimeout(timer);
			source?.close();
		};
	}, [sessionId, queryClient]);

	const needsSnapshot = conversationLiveNeedsSnapshot(snapshot, live);
	useEffect(() => {
		if (!needsSnapshot || !sessionId) return;
		let disposed = false;
		const refresh = async () => {
			const previous = resync.current?.sessionId === sessionId ? resync.current.request : undefined;
			if (previous) await previous;
			if (disposed) return;
			// Finish an active read instead of restarting it on every replay or
			// reset. Only the latest target/snapshot change follows that read.
			const request = (previous ? Promise.resolve() : queryClient.cancelQueries({ queryKey: ["conversation", sessionId] }))
				.then(() => disposed ? undefined : queryClient.invalidateQueries({ queryKey: ["conversation", sessionId] }, { cancelRefetch: false }));
			resync.current = { sessionId, request };
			try { await request; } finally { if (resync.current?.request === request) resync.current = undefined; }
		};
		// Query state surfaces request failures; retries require new progress.
		void refresh().catch(() => {});
		return () => { disposed = true; };
	}, [needsSnapshot, sessionId, live?.generation, live?.branchId, live?.conversationId,
		live?.afterSequence, live?.resetSequence, snapshot, queryClient]);
	return applyConversationLive(snapshot, live);
}
