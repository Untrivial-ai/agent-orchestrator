import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { formatResourceBytes, pressureState, type PressureState } from "@aoagents/product-ui";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";

export type SessionMemoryReading = components["schemas"]["SessionMemoryResponse"];
export type SessionStepReading = components["schemas"]["SessionStepResponse"];
export type SystemMemoryReading = components["schemas"]["SystemMemoryResponse"];
export type AppMemoryReading = components["schemas"]["AppMemoryResponse"];

export const sessionMemoryQueryRoot = ["session-memory"] as const;
export const sessionMemoryQueryKey = (projectId?: string) =>
	[...sessionMemoryQueryRoot, projectId ?? "all"] as const;

/**
 * Memory is a live reading. The status bar polls slowly; while the memory
 * window is open the same query speeds up, and closing it slows down again.
 * Never faster than a second or two: the numbers only jitter.
 */
export const sessionMemoryRefetchIntervalMs = 10_000;
export const sessionMemoryFastRefetchIntervalMs = 2_000;

type SessionMemoryResponse = {
	sessions: SessionMemoryReading[];
	system?: SystemMemoryReading;
	app?: AppMemoryReading;
	/** When this response arrived; the graph keys its samples on it. */
	fetchedAt: string;
};

export async function fetchSessionMemory(projectId?: string): Promise<SessionMemoryResponse> {
	const { data, error } = await apiClient.GET("/api/v1/usage/sessions/memory", {
		params: { query: projectId ? { projectId } : {} },
	});
	if (error) throw error;
	return { sessions: data?.sessions ?? [], system: data?.system, app: data?.app, fetchedAt: new Date().toISOString() };
}

/** How many mounted consumers want the fast cadence; the query reads it. */
const fastWatchers = { count: 0 };

export function sessionMemoryQueryOptions(projectId?: string) {
	return {
		queryKey: sessionMemoryQueryKey(projectId),
		queryFn: () => fetchSessionMemory(projectId),
		refetchInterval: () => (fastWatchers.count > 0 ? sessionMemoryFastRefetchIntervalMs : sessionMemoryRefetchIntervalMs),
		// 501 on Windows is permanent for the run; do not hammer the daemon.
		retry: false,
	};
}

/** Mount while the memory window is open: samples arrive every two seconds
 * instead of ten, and the first one is fetched right away. */
export function useFastMemorySampling() {
	const queryClient = useQueryClient();
	useEffect(() => {
		fastWatchers.count += 1;
		void queryClient.invalidateQueries({ queryKey: sessionMemoryQueryRoot });
		return () => {
			fastWatchers.count -= 1;
		};
	}, [queryClient]);
}

export function useSessionMemory(projectId?: string) {
	return useQuery({
		...sessionMemoryQueryOptions(projectId),
		select: (data: SessionMemoryResponse) =>
			new Map(data.sessions.map((item) => [item.sessionId, item] as const)),
	});
}

/** Host RAM and pressure. Shares the session-memory query, so mounting both
 * hooks costs one fetch, not two. Absent where unsupported. */
export function useSystemMemory(projectId?: string) {
	return useQuery({
		...sessionMemoryQueryOptions(projectId),
		select: (data: SessionMemoryResponse) => data.system,
	});
}

/** Everything AO runs, app-wide, for the status bar. Same query as the
 * sessions so the bar and the window it opens never disagree. */
export function useAppMemory() {
	return useQuery({
		...sessionMemoryQueryOptions(),
		select: (data: SessionMemoryResponse) => ({
			app: data.app,
			system: data.system,
			fetchedAt: data.fetchedAt,
			// Sessions with a live runtime; one without a process tree is not counted.
			liveCount: data.sessions.length,
		}),
	});
}

/** The machine's pressure state, or undefined where the host can't be read. */
export function usePressureState(): PressureState | undefined {
	const system = useAppMemory().data?.system;
	return system ? pressureState(system) : undefined;
}

/** How many samples the window's graph keeps: two minutes at the fast cadence. */
export const sampleHistoryLength = 60;

/** One point of the CPU graph: the host's busy share and AO's share of the machine. */
export type CPUSample = { host: number; ao: number };

/**
 * A ring of recent readings for a graph, kept in the renderer: no backend
 * history needed. One entry per distinct sample, keyed on when it arrived.
 */
export function useSampleHistory<T>(sample: T | undefined, sampledAt: string | undefined): T[] {
	const [history, setHistory] = useState<T[]>([]);
	const lastSample = useRef<string | undefined>(undefined);
	useEffect(() => {
		if (sample === undefined || !sampledAt || sampledAt === lastSample.current) return;
		lastSample.current = sampledAt;
		setHistory((prev) => [...prev, sample].slice(-sampleHistoryLength));
	}, [sample, sampledAt]);
	return history;
}

/** Bytes as the monitor shows them everywhere: 10 MB steps, GB above a thousand. */
export const formatMemory = formatResourceBytes;

/** Whole percent of one core. */
export function formatCPU(percent: number): string {
	return `${Math.round(percent)}%`;
}
