import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, getNotifications, getSessions, type DashboardSession, type DashboardStats, type OrchestratorLink, type ProjectInfo } from "./api";
import { isConfigured, type ServerConfig } from "./config";
import { connectToHost } from "./connectRuntime";
import { shouldKeepPolling, userFacingError } from "./connectionError";
import type { Endpoint } from "./endpoints";
import { findHost, type Host } from "./hosts";
import { pollIntervalFor } from "./pollInterval";
import { sameServerConfig } from "./sameConfig";
import { shouldRaceForUpgrade, UPGRADE_RACE_CHECK_MS } from "./upgradeRace";

export type HostSnapshot = {
	hostId: string;
	name: string;
	endpoints: Endpoint[];
	config: ServerConfig | null;
	connection: "closed" | "connecting" | "open";
	loading: boolean;
	error: string | null;
	errorStatus: number | null;
	projects: ProjectInfo[];
	projectsKnown: boolean;
	sessions: DashboardSession[];
	orchestrators: OrchestratorLink[];
	orchestratorId: string | null;
	stats: DashboardStats;
	notificationsUnread: number;
	lastSyncAt: number;
};

export function emptyHostSnapshot(host: Host): HostSnapshot {
	return {
		hostId: host.id,
		name: host.name,
		endpoints: host.endpoints,
		config: null,
		connection: "connecting",
		loading: true,
		error: null,
		errorStatus: null,
		projects: [],
		projectsKnown: false,
		sessions: [],
		orchestrators: [],
		orchestratorId: null,
		stats: {},
		notificationsUnread: 0,
		lastSyncAt: 0,
	};
}

type Runner = { config: ServerConfig | null; refresh: () => Promise<void>; stop: () => void };

/** One daemon's independent endpoint race and REST poll. */
export function startOtherHost(host: Host, previous: HostSnapshot | undefined, publish: (patch: Partial<HostSnapshot>) => void): Runner {
	let stopped = false;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let upgradeTimer: ReturnType<typeof setInterval> | undefined;
	let upgrading = false;
	let connected = false;
	let lastRaceAt = 0;
	let lastProjects = previous?.projects ?? [];
	let projectsKnown = previous?.projectsKnown ?? false;
	let busy: Promise<void> | null = null;
	const runner: Runner = {
		config: null,
		refresh: () => {
			if (busy) return busy;
			busy = (runner.config ? poll() : connect()).finally(() => { busy = null; });
			return busy;
		},
		stop: () => { stopped = true; if (timer) clearTimeout(timer); if (upgradeTimer) clearInterval(upgradeTimer); },
	};
	const update = (patch: Partial<HostSnapshot>) => { if (!stopped) publish(patch); };
	const schedule = (ms: number) => {
		if (timer) clearTimeout(timer);
		if (!stopped) timer = setTimeout(() => void runner.refresh(), ms);
	};
	const connect = async () => {
		if (stopped) return;
		// A saved address is not verified after a network change. A mounted chat
		// must not use its old bearer while the identity probe is still running.
		runner.config = null;
		connected = false;
		update({ config: null, connection: "connecting", loading: true });
		try {
			lastRaceAt = Date.now();
			const result = await connectToHost(host.id);
			if (stopped) return;
			if (!result.ok || result.hostId !== host.id) {
				runner.config = null;
				const incompatible = !result.ok && result.reason === "incompatible";
				update({ config: null, connection: "closed", loading: false, error: incompatible ? "AO versions are incompatible. Update AO on this phone and the machine." : "Host is unreachable", errorStatus: incompatible ? 426 : null });
				schedule(15_000);
				return;
			}
			runner.config = result.config;
			update({ config: result.config, connection: "connecting", endpoints: host.endpoints });
			await poll();
		} catch (cause) {
			if (stopped) return;
			runner.config = null;
			update({ config: null, connection: "closed", loading: false, error: userFacingError(cause), errorStatus: null });
			schedule(15_000);
		}
	};
	const checkUpgrade = async () => {
		const current = runner.config;
		if (stopped || upgrading || !connected || !current) return;
		upgrading = true;
		try {
			const known = (await findHost(host.id))?.endpoints ?? host.endpoints;
			if (stopped || runner.config !== current || !shouldRaceForUpgrade({
				currentKind: current.endpointKind,
				known,
				lastRaceAt,
				now: Date.now(),
				resumed: false,
			})) return;
			lastRaceAt = Date.now();
			const result = await connectToHost(host.id);
			if (stopped || !connected || runner.config !== current || !result.ok || result.hostId !== host.id) return;
			if (sameServerConfig(current, result.config)) return;
			runner.config = result.config;
			connected = false;
			update({ config: result.config, connection: "connecting", loading: true, endpoints: known });
			// An old-endpoint poll may still own `busy`; wait for it to finish
			// before starting the first poll on the verified upgrade path.
			if (busy) await busy;
			if (!stopped && runner.config === result.config) void runner.refresh();
		} catch {
			// A failed upgrade must leave the working endpoint alone.
		} finally {
			upgrading = false;
		}
	};
	upgradeTimer = setInterval(() => void checkUpgrade(), UPGRADE_RACE_CHECK_MS);
	const poll = async () => {
		const c = runner.config;
		if (!c || !isConfigured(c) || stopped) return;
		try {
			const answer = await getSessions(c, "all");
			if (stopped || runner.config !== c) return;
			if (answer.projects !== null) {
				lastProjects = answer.projects;
				projectsKnown = true;
			}
			update({
				config: c,
				connection: "open",
				loading: false,
				error: null,
				errorStatus: null,
				projects: lastProjects,
				projectsKnown,
				sessions: answer.sessions,
				orchestrators: answer.orchestrators,
				orchestratorId: answer.orchestratorId,
				stats: answer.stats,
				lastSyncAt: Date.now(),
			});
			connected = true;
			if (stopped || runner.config !== c) return;
			try {
				const page = await getNotifications(c, { status: "unread", limit: 1 });
				if (!stopped && runner.config === c) update({ notificationsUnread: page.unreadCount });
			} catch {
				// Older daemons may not serve notifications; the board remains usable.
			}
			if (!stopped && runner.config === c) schedule(pollIntervalFor(c));
		} catch (cause) {
			if (stopped || runner.config !== c) return;
			const status = cause instanceof ApiError ? cause.status : undefined;
			connected = false;
			update({ connection: "closed", loading: false, error: userFacingError(cause), errorStatus: status ?? null });
			if (!shouldKeepPolling(status)) {
				runner.config = null;
				update({ config: null }); // Do not leave a rejected bearer available to actions.
				return;
			}
			if (status === undefined || status === 421) {
				runner.config = null;
				update({ config: null }); // An old address is no longer verified for actions.
				schedule(2_000); // Race LAN/tunnel again after a network change.
			} else schedule(pollIntervalFor(c));
		}
	};
	return runner;
}

/** The selected host keeps its existing store/poll. These runners own every other paired host. */
export function useOtherHosts(hosts: Host[], selectedHostId: string | null, appActive: boolean) {
	const [snapshots, setSnapshots] = useState<Record<string, HostSnapshot>>({});
	const runners = useRef(new Map<string, Runner>());

	useEffect(() => {
		for (const runner of runners.current.values()) runner.stop();
		runners.current.clear();
		if (!appActive) {
			setSnapshots((current) => Object.fromEntries(hosts.filter((host) => host.id !== selectedHostId).map((host) => [
				host.id,
				{ ...(current[host.id] ?? emptyHostSnapshot(host)), name: host.name, endpoints: host.endpoints, config: null, connection: "closed" as const, loading: false },
			])));
			return;
		}

		const others = hosts.filter((host) => host.id !== selectedHostId);
		setSnapshots((current) => {
			const next: Record<string, HostSnapshot> = {};
			for (const host of others) next[host.id] = {
				...(current[host.id] ?? emptyHostSnapshot(host)),
				name: host.name,
				endpoints: host.endpoints,
				config: null,
				connection: "connecting",
				loading: true,
			};
			return next;
		});

		for (const host of others) {
			const runner = startOtherHost(host, snapshots[host.id], (patch) => {
				setSnapshots((current) => ({
					...current,
					[host.id]: { ...(current[host.id] ?? emptyHostSnapshot(host)), ...patch },
				}));
			});
			runners.current.set(host.id, runner);
			void runner.refresh();
		}
		return () => {
			for (const runner of runners.current.values()) runner.stop();
			runners.current.clear();
		};
	}, [hosts, selectedHostId, appActive]);

	const refreshHost = useCallback(async (hostId: string) => {
		await runners.current.get(hostId)?.refresh();
	}, []);
	const configForHost = useCallback((hostId: string) => runners.current.get(hostId)?.config ?? null, []);
	return { snapshots, refreshHost, configForHost };
}
