import { useEffect, useSyncExternalStore } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { formatResourceBytes, pressureStateFromRaw, type PressureState } from "@aoagents/product-ui";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { useUiStore } from "../stores/ui-store";

export type SessionMemoryReading = components["schemas"]["SessionMemoryResponse"];
export type SessionStepReading = components["schemas"]["SessionStepResponse"];
export type SystemMemoryReading = components["schemas"]["SystemMemoryResponse"];
export type AppMemoryReading = components["schemas"]["AppMemoryResponse"];
export type MemoryPressureReading = components["schemas"]["MemoryPressureResponse"];

export const sessionMemoryQueryRoot = ["session-memory"] as const;
export const sessionMemoryQueryKey = (projectId?: string) =>
	[...sessionMemoryQueryRoot, projectId ?? "all"] as const;

/**
 * The full sample walks every process, so with the memory window closed it
 * runs once a minute; the board's colour comes from the cheap pressure read
 * below instead. While the window is open the same query speeds up, and
 * closing it slows down again. Never faster than a second or two: the
 * numbers only jitter.
 */
export const sessionMemoryRefetchIntervalMs = 60_000;
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
	const reading = { sessions: data?.sessions ?? [], system: data?.system, app: data?.app, fetchedAt: new Date().toISOString() };
	// Machine and AO figures are the same for every project; record them once.
	if (!projectId) recordCPU(reading);
	return reading;
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

/** Memory monitoring is a Developer mode tool: with it off, nothing polls the daemon. */
function useMemoryEnabled(): boolean {
	return useUiStore((state) => state.developerMode);
}

export function useSessionMemory(projectId?: string) {
	const enabled = useMemoryEnabled();
	return useQuery({
		enabled,
		...sessionMemoryQueryOptions(projectId),
		select: (data: SessionMemoryResponse) =>
			new Map(data.sessions.map((item) => [item.sessionId, item] as const)),
	});
}

/** Host RAM and pressure. Shares the session-memory query, so mounting both
 * hooks costs one fetch, not two. Absent where unsupported. */
export function useSystemMemory(projectId?: string) {
	const enabled = useMemoryEnabled();
	return useQuery({
		enabled,
		...sessionMemoryQueryOptions(projectId),
		select: (data: SessionMemoryResponse) => data.system,
	});
}

/** Everything AO runs, app-wide, for the status bar. Same query as the
 * sessions so the bar and the window it opens never disagree. */
export function useAppMemory() {
	const enabled = useMemoryEnabled();
	return useQuery({
		enabled,
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

/** The light's colour polls on its own: one kernel query, no process walk,
 * so it stays live while the full sample runs once a minute. */
export const memoryPressureRefetchIntervalMs = 10_000;
export const memoryPressureQueryKey = ["memory-pressure"] as const;

export function useMemoryPressure() {
	const enabled = useMemoryEnabled();
	return useQuery({
		enabled,
		queryKey: memoryPressureQueryKey,
		queryFn: async (): Promise<MemoryPressureReading> => {
			const { data, error } = await apiClient.GET("/api/v1/usage/memory/pressure");
			if (error) throw error;
			return data;
		},
		refetchInterval: memoryPressureRefetchIntervalMs,
		// 501 where the host can't be read is permanent for the run.
		retry: false,
	});
}

/** The last state any caller saw, so a change refreshes the numbers once. */
const lastPressure: { state?: PressureState } = {};

/** The machine's pressure state, or undefined where the host can't be read. */
export function usePressureState(): PressureState | undefined {
	const queryClient = useQueryClient();
	const light = useMemoryPressure().data;
	const system = useAppMemory().data?.system;
	const reading = light ?? system;
	const state = reading ? pressureStateFromRaw(reading.pressureRaw, reading.pressureSource) : undefined;
	// A change of colour is when the numbers matter: fetch them now instead
	// of at the next minute, so the light and its figure agree.
	useEffect(() => {
		if (!state) return;
		if (lastPressure.state !== undefined && lastPressure.state !== state) {
			void queryClient.invalidateQueries({ queryKey: sessionMemoryQueryRoot });
		}
		lastPressure.state = state;
	}, [state, queryClient]);
	return state;
}

/** How many samples the window's graph keeps: two minutes at the fast cadence. */
export const sampleHistoryLength = 60;

/** One point of the CPU graph: the host's busy share and AO's share of the machine. */
export type CPUSample = { host: number; ao: number };

/** One point from a reading, or undefined when the daemon had nothing earlier
 * to measure CPU against: that zero is unknown, not an idle machine. */
export function cpuSampleOf(system?: SystemMemoryReading, app?: AppMemoryReading): CPUSample | undefined {
	if (!system || !app || !system.cpuMeasured || !app.cpuMeasured) return undefined;
	return { host: system.cpuPercent, ao: Math.min(100, app.cpuPercent / Math.max(1, system.cpuCount)) };
}

/**
 * The CPU graph's recent points. Kept here rather than in the window so
 * closing and reopening it keeps the graph: every app-wide reading feeds it,
 * including the once-a-minute ones while the window is closed.
 */
const cpuHistory = { samples: [] as CPUSample[], listeners: new Set<() => void>() };

function recordCPU(reading: SessionMemoryResponse) {
	const sample = cpuSampleOf(reading.system, reading.app);
	if (!sample) return;
	cpuHistory.samples = [...cpuHistory.samples, sample].slice(-sampleHistoryLength);
	for (const listener of cpuHistory.listeners) listener();
}

function subscribeCPUHistory(listener: () => void) {
	cpuHistory.listeners.add(listener);
	return () => {
		cpuHistory.listeners.delete(listener);
	};
}

export function useCPUHistory(): CPUSample[] {
	return useSyncExternalStore(subscribeCPUHistory, () => cpuHistory.samples);
}

/** Bytes as the monitor shows them everywhere: 10 MB steps, GB above a thousand. */
export const formatMemory = formatResourceBytes;

/** Whole percent of one core. */
export function formatCPU(percent: number): string {
	return `${Math.round(percent)}%`;
}
