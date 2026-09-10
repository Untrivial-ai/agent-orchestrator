import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import type { ConversationSnapshot } from "../types/conversation";
import { applyConversationLive, conversationLiveNeedsSnapshot, mergeConversationLiveFrame, useConversationLive } from "./useConversationLive";

vi.mock("../lib/api-client", () => ({
	getApiBaseUrl: () => "http://127.0.0.1:3001",
	hasTrustedApiBaseUrl: () => true,
	subscribeApiBaseUrl: () => () => {},
}));

type Frame = components["schemas"]["ConversationLiveResponse"];
const snapshot: ConversationSnapshot = {
	conversationId: "conversation", sessionId: "session", harness: "codex", mode: "chat",
	controller: { state: "busy" }, activeBranchId: "branch", liveGeneration: "generation", liveSequence: 0,
	latestSequence: 1, oldestSequence: 1, hasMoreBefore: false, settings: {},
	turns: [{ id: "turn", providerTurnId: "provider-turn", state: "running", requestedAt: "2026-09-10T00:00:00Z" }],
	items: [{ kind: "message", id: "saved", providerItemId: "reply", turnId: "turn", sequence: 1,
		revision: 1, role: "assistant", origin: "provider", text: "Existing ", streaming: true, createdAt: "2026-09-10T00:00:00Z" }],
};
function frame(deltas = ["live ", "text"]): Frame {
	return { generation: "generation", conversationId: "conversation", branchId: "branch", afterSequence: 0, resetSequence: 0,
		sequence: deltas.length, events: deltas.map((delta, index) => ({ sequence: index + 1, kind: "message.delta",
			providerItemId: "reply", providerTurnId: "provider-turn", delta, createdAt: "2026-09-10T00:00:01Z" })) };
}

describe("live text reconciliation", () => {
	it("shows all incoming text over a resumed prefix without changing the saved cache", () => {
		const result = applyConversationLive(snapshot, frame());
		expect(result?.items[0]).toMatchObject({ id: "saved", text: "Existing live text", streaming: true });
		expect(snapshot.items[0]).toMatchObject({ text: "Existing " });
	});
	it("does not duplicate text as a partial database save catches up", () => {
		const partial = { ...snapshot, liveSequence: 1, items: [{ ...snapshot.items[0], text: "Existing live " }] } as ConversationSnapshot;
		expect(applyConversationLive(partial, frame())?.items[0]).toMatchObject({ text: "Existing live text" });
		const saved = { ...snapshot, liveSequence: 2, items: [{ ...snapshot.items[0], text: "Existing live text" }] } as ConversationSnapshot;
		expect(applyConversationLive(saved, frame())).toBe(saved);
	});
	it("uses each history page's checkpoint when pages were fetched at different times", () => {
		const older = { ...snapshot, liveSequence: 2, items: [{ ...snapshot.items[0], liveGeneration: "generation", liveSequence: 1, text: "Existing live " }] } as ConversationSnapshot;
		expect(applyConversationLive(older, frame())?.items[0]).toMatchObject({ text: "Existing live text" });
		const newer = { ...snapshot, items: [{ ...snapshot.items[0], liveGeneration: "generation", liveSequence: 2, text: "Existing live text" }] } as ConversationSnapshot;
		expect(applyConversationLive(newer, frame())).toBe(newer);
	});
	it("refetches an older page when its prefix has left the live journal", () => {
		const older = { ...snapshot, liveSequence: 10, items: [{ ...snapshot.items[0], liveGeneration: "generation", liveSequence: 1, text: "a" }] } as ConversationSnapshot;
		const live = { ...frame(["f"]), afterSequence: 5, sequence: 6, events: [{ ...frame(["f"]).events[0], sequence: 6 }] };
		expect(conversationLiveNeedsSnapshot(older, live)).toBe(true);
		expect(applyConversationLive(older, live)).toBe(older);
	});
	it("replaces provisional IDs with the durable row and ignores replayed frames", () => {
		const live = frame(["hello"]);
		const provisional = applyConversationLive({ ...snapshot, items: [] }, live);
		expect(provisional?.items).toHaveLength(1);
		expect(provisional?.items[0]).toMatchObject({ text: "hello", turnId: "turn", providerItemId: "reply" });
		const repeated = mergeConversationLiveFrame(live, live);
		expect(repeated.events).toHaveLength(1);
		const saved = { ...snapshot, liveSequence: 1, items: [{ ...snapshot.items[0], text: "hello" }] } as ConversationSnapshot;
		expect(applyConversationLive(saved, repeated)?.items).toEqual(saved.items);
	});
	it("does not assign an unidentified provider turn to a queued local turn", () => {
		const live = frame(["hello"]);
		live.events[0].providerTurnId = undefined;
		const queued = { ...snapshot, items: [], turns: [{ ...snapshot.turns[0], providerTurnId: undefined }] };
		expect(applyConversationLive(queued, live)?.items[0]).toMatchObject({ text: "hello", turnId: undefined });
	});
	it("honors final text corrections and completion without a final message snapshot", () => {
		const complete = frame();
		complete.events.push({ sequence: 3, kind: "message.completed", providerItemId: "reply", text: "Corrected answer", createdAt: "now" });
		expect(applyConversationLive(snapshot, complete)?.items[0]).toMatchObject({ text: "Corrected answer", streaming: false });
		complete.events.pop();
		complete.events.push({ sequence: 3, kind: "turn.completed", providerTurnId: "provider-turn", createdAt: "now" });
		expect(applyConversationLive(snapshot, complete)?.items[0]).toMatchObject({ text: "Existing live text", streaming: false });
	});
	it("refuses unknown prefixes, old controller generations, and other branches", () => {
		for (const live of [{ ...frame(), afterSequence: 1 }, { ...frame(), generation: "retired" }, { ...frame(), branchId: "other" }]) {
			expect(applyConversationLive(snapshot, live)).toBe(snapshot);
		}
	});
	it("keeps continuous incremental frames and detects an evicted reconnect prefix", () => {
		const first = frame(["one"]);
		const next = { ...frame(["two"]), afterSequence: 1, sequence: 2, events: [{ ...first.events[0], sequence: 2, delta: "two" }] };
		expect(mergeConversationLiveFrame(first, next).events.map((event) => event.delta)).toEqual(["one", "two"]);
		const gap = mergeConversationLiveFrame(first, { ...next, afterSequence: 5, sequence: 6, events: [] });
		expect(gap.afterSequence).toBe(5);
		expect(gap.events).toEqual([]);
	});
	it("bounds retained text and discards failed previews after their processed checkpoint", () => {
		const retained = mergeConversationLiveFrame(undefined, frame(["x".repeat(1024 * 1024 + 1)]));
		expect(retained.events).toEqual([]);
		expect(retained.afterSequence).toBe(1);
		const failed = { ...snapshot, liveSequence: 2 };
		expect(applyConversationLive(failed, frame())).toBe(failed);
	});
});

class FakeEventSource {
	static instances: FakeEventSource[] = [];
	onopen?: () => void;
	onerror?: () => void;
	listener?: (event: MessageEvent) => void;
	close = vi.fn();
	constructor(public url: string) { FakeEventSource.instances.push(this); }
	addEventListener(_type: string, listener: (event: MessageEvent) => void) { this.listener = listener; }
	emit(value: Frame) { this.listener?.({ data: JSON.stringify(value) } as MessageEvent); }
}

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); FakeEventSource.instances = []; });

it("renders multiple chunks while a durable refresh is blocked, then reconciles and unsubscribes", async () => {
	vi.stubGlobal("EventSource", FakeEventSource);
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const invalidate = vi.spyOn(client, "invalidateQueries").mockImplementation(() => new Promise(() => {}));
	const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
	const { result, rerender, unmount } = renderHook(({ saved }) => useConversationLive("session", saved),
		{ wrapper, initialProps: { saved: snapshot } });
	const source = FakeEventSource.instances[0];
	act(() => { source.onopen?.(); source.emit(frame()); });
	await waitFor(() => expect(result.current?.items[0]).toMatchObject({ text: "Existing live text" }));
	expect(invalidate).toHaveBeenCalled();
	rerender({ saved: { ...snapshot, liveSequence: 2, items: [{ ...snapshot.items[0], text: "Existing live text" }] } as ConversationSnapshot });
	expect(result.current?.items[0]).toMatchObject({ text: "Existing live text" });
	unmount();
	expect(source.close).toHaveBeenCalledOnce();
	client.clear();
});

it.each(["reset", "gap", "checkpoint"] as const)("finishes an active resync and follows %s progress", async (kind) => {
	vi.useFakeTimers();
	vi.stubGlobal("EventSource", FakeEventSource);
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	let finishFirst!: () => void;
	let finishSecond!: () => void;
	const invalidate = vi.spyOn(client, "invalidateQueries")
		.mockImplementationOnce(() => new Promise<void>((resolve) => { finishFirst = resolve; }))
		.mockImplementationOnce(() => new Promise<void>((resolve) => { finishSecond = resolve; }))
		.mockResolvedValue(undefined);
	const cancel = vi.spyOn(client, "cancelQueries");
	const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
	const { result, rerender, unmount } = renderHook(({ saved }) => useConversationLive("session", saved),
		{ wrapper, initialProps: { saved: snapshot } });
	const source = FakeEventSource.instances[0];
	const target = (sequence: number): Frame => ({ ...frame([]), sequence,
		afterSequence: kind === "gap" ? sequence : 0, resetSequence: kind === "gap" ? 0 : sequence });
	await act(async () => { source.emit(target(kind === "checkpoint" ? 11 : 10)); });
	expect(invalidate).toHaveBeenCalledTimes(1);
	await act(async () => { source.emit(target(11)); });
	expect(invalidate).toHaveBeenCalledTimes(1);
	// The older response still needs reconciliation, so the boolean remains true.
	// Advancing the target must neither cancel that read nor lose its follow-up.
	rerender({ saved: { ...snapshot, liveSequence: 10 } });
	await act(async () => { finishFirst(); });
	expect(invalidate).toHaveBeenCalledTimes(2);
	expect(cancel).toHaveBeenCalledTimes(1);
	expect(invalidate).toHaveBeenLastCalledWith({ queryKey: ["conversation", "session"] }, { cancelRefetch: false });
	// An unchanged response does not start permanent polling while storage is
	// unavailable. A later checkpoint still releases the live overlay.
	await act(async () => { finishSecond(); await vi.advanceTimersByTimeAsync(10_000); });
	expect(invalidate).toHaveBeenCalledTimes(2);
	rerender({ saved: { ...snapshot, liveSequence: 11 } });
	act(() => { source.emit({ ...frame(["continued"]), afterSequence: 11, sequence: 12,
		events: [{ ...frame(["continued"]).events[0], sequence: 12 }] }); });
	expect(result.current?.items[0]).toMatchObject({ text: "Existing continued" });
	unmount();
	client.clear();
});

it("resyncs a new session independently of a pending old-session request", async () => {
	vi.stubGlobal("EventSource", FakeEventSource);
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	let rejectFirst!: (error: Error) => void;
	const invalidate = vi.spyOn(client, "invalidateQueries")
		.mockImplementationOnce(() => new Promise<void>((_resolve, reject) => { rejectFirst = reject; }))
		.mockResolvedValue(undefined);
	const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
	const { rerender, unmount } = renderHook(({ sessionId, saved }) => useConversationLive(sessionId, saved),
		{ wrapper, initialProps: { sessionId: "session", saved: snapshot } });
	const reset = { ...frame([]), sequence: 1, resetSequence: 1 };
	await act(async () => { FakeEventSource.instances[0].emit(reset); });
	expect(invalidate).toHaveBeenCalledTimes(1);
	rerender({ sessionId: "other", saved: { ...snapshot, sessionId: "other" } });
	await act(async () => { FakeEventSource.instances[1].emit(reset); });
	expect(invalidate).toHaveBeenCalledTimes(2);
	expect(invalidate).toHaveBeenLastCalledWith({ queryKey: ["conversation", "other"] }, { cancelRefetch: false });
	unmount();
	await act(async () => { rejectFirst(new Error("old request cancelled")); });
	expect(invalidate).toHaveBeenCalledTimes(2);
	client.clear();
});
