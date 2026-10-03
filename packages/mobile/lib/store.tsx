import AsyncStorage from "@react-native-async-storage/async-storage";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
// Aliased: this file already declares its own `AppState` type for the
// provider's context value, so the React Native app-lifecycle API imports
// under a different name to avoid colliding with it.
import { AppState as RNAppState } from "react-native";
import { shouldPoll } from "./appStatePoll";
import {
	ApiError,
	delegateTask,
	getNotifications,
	getSessions,
	killSession,
	launchOrchestrator as apiLaunchOrchestrator,
	mergePR as apiMergePR,
	pinSession as apiPinSession,
	renameSession as apiRenameSession,
	restoreSession,
	resumeSessionAgent,
	sendMessage,
	unpinSession as apiUnpinSession,
	type DashboardPR,
	type DashboardSession,
	type DashboardStats,
	type OrchestratorLink,
	type ProjectInfo,
	type SessionMode,
	type SpawnAttachmentInput,
} from "./api";
import { isConfigured, machineIdentity, type ServerConfig } from "./config";
import { resolveActiveConfig, runtimeResolveDeps } from "./resolveConfig";
import { pollIntervalFor } from "./pollInterval";
import { rejectedEndpointNeedsRace, type ConnectOptions } from "./connectRuntime";
import type { Endpoint } from "./endpoints";
import { activeHost, loadHosts, sameHostConnections, setActiveHost, type Host } from "./hosts";
import { shouldReRace } from "./reRace";
import { shouldRaceForUpgrade, UPGRADE_RACE_CHECK_MS } from "./upgradeRace";
import { pollResultIsCurrent, sameServerConfig } from "./sameConfig";
import { shouldShowLoading } from "./configLoading";
import { isDesktopUnreachable, shouldKeepPolling, userFacingError } from "./connectionError";
import { IncompatibleHostVersionError } from "./race";
import { primeInstallId } from "./installId";
import { collectPRs } from "./prView";
import { ALL_PROJECTS, NO_PROJECTS_KNOWN, projectsForMachine, resolveActiveProject, retainProjects, sessionRowsForMachine, type KnownProjects } from "./projectFilter";
import { MOBILE_EVENTS } from "./telemetry/events";
import { mobileTelemetry, trackFeature } from "./telemetry/runtime";
import { useConversationEventTransport } from "./chat/conversationEvents";
import { hostRouteMatches } from "./hostRoute";
import { emptyHostSnapshot, useOtherHosts, type HostSnapshot } from "./otherHosts";
import type { HostedOrchestrator, HostedProject, HostedSession } from "./hostedRows";

const ACTIVE_PROJECT_KEY = "ao.activeProject";

// Board-level connection state is derived from the REST poll. The session screen
// tracks its own terminal mux connection separately.
export type ConnStatus = "closed" | "connecting" | "open";

// An options object rather than four optional positionals: `spawn(a, b, c, d)`
// with every argument optional and same-typed is where call-site mistakes live.
export type SpawnOptions = {
	/** Prevent a stale composer from posting a same-ID project to a new machine. */
	hostId: string;
	/** Falls back to the active project, or the only project. */
	projectId?: string;
	prompt?: string;
	harness?: string;
	model?: string;
	attachments?: SpawnAttachmentInput[];
	/** Mobile defaults to Chat; TUI remains an explicit compatibility choice. */
	mode?: SessionMode;
	clientRequestId?: string;
};

type AppState = {
	config: ServerConfig | null;
	configured: boolean;
	/** Paired machine owning this view, even while its endpoint is offline. */
	currentHostId: string | undefined;
	selectedHostName: string | null;
	/** One independently connected snapshot per paired machine. */
	hostStates: HostSnapshot[];
	allSessions: HostedSession[];
	allProjects: HostedProject[];
	allOrchestrators: HostedOrchestrator[];
	/** Whether the first config resolution has finished. Until it has, an
	 *  unconfigured store means "still finding the machine", not "unpaired". */
	configResolved: boolean;
	/** Every way the active machine says it can be reached, for telling a
	 *  rotated tunnel hostname apart from being simply out of range. */
	activeEndpoints: Endpoint[];
	projects: ProjectInfo[];
	/** Whether the current projects value came from the latest daemon response. */
	projectsKnown: boolean;
	sessions: DashboardSession[];
	orchestrators: OrchestratorLink[];
	orchestratorId: string | null;
	stats: DashboardStats;
	activeProjectId: string; // 'all' or a projectId
	connection: ConnStatus;
	/** Unread notification count, for the board's bell badge. 0 when unknown. */
	notificationsUnread: number;
	loading: boolean;
	error: string | null;
	// HTTP status behind `error`, or null when the server was never reached.
	errorStatus: number | null;
	/**
	 * The last poll failed because nothing answered, so a reconnect can clear it.
	 * False for rejections (401/403/429), which stop the poll, and for 5xx.
	 */
	unreachable: boolean;
	/**
	 * When the last successful poll landed, in epoch milliseconds. 0 if none has.
	 *
	 * Deliberately a getter rather than a value: a timestamp that changed on every
	 * successful tick would re-render every consumer of this store once per poll.
	 * Read it through useStaleness, which owns the clock.
	 */
	getLastSyncAt: () => number;
	// actions
	/**
	 * Races the selected machine's endpoints. Null means none passed the host
	 * identity check, so a cached address must not receive its credential.
	 */
	reloadConfig: (options?: ConnectOptions) => Promise<ServerConfig | null>;
	switchHost: (id: string) => Promise<void>;
	refresh: () => Promise<void>;
	refreshHost: (hostId: string) => Promise<void>;
	refreshAll: () => Promise<void>;
	configForHost: (hostId: string) => ServerConfig | null;
	setActiveProject: (id: string) => void;
	spawn: (opts: SpawnOptions) => Promise<DashboardSession>;
	launchConductor: (projectId: string, clean?: boolean, mode?: SessionMode, hostId?: string) => Promise<OrchestratorLink>;
	merge: (pr: DashboardPR, hostId?: string) => Promise<void>;
	kill: (id: string, hostId?: string) => Promise<void>;
	renameWorker: (id: string, displayName: string, hostId?: string) => Promise<void>;
	setWorkerPinned: (id: string, pinned: boolean, hostId?: string) => Promise<void>;
	restore: (id: string, hostId?: string) => Promise<void>;
	/** Restart a stopped agent without restoring a terminated AO session. */
	resumeAgent: (id: string, hostId?: string) => Promise<void>;
	send: (id: string, message: string, hostId?: string) => Promise<void>;
};

const AppContext = createContext<AppState | null>(null);

export function useApp(): AppState {
	const ctx = useContext(AppContext);
	if (!ctx) throw new Error("useApp must be used within <AppProvider>");
	return ctx;
}

/** Reuse the normal session/project screens with a different daemon behind them. */
export function HostScope({ hostId, children }: { hostId: string; children: ReactNode }) {
	const app = useApp();
	const host = app.hostStates.find((item) => item.hostId === hostId);
	const isSelected = app.currentHostId === hostId;
	const scoped = useMemo<AppState>(() => {
		if (isSelected) return app;
		const config = host?.config ?? null;
		const projects = host?.projects ?? [];
		return {
			...app,
			config,
			configured: !!config && isConfigured(config),
			currentHostId: host?.hostId,
			selectedHostName: host?.name ?? null,
			activeEndpoints: host?.endpoints ?? [],
			projects,
			projectsKnown: host?.projectsKnown ?? false,
			sessions: host?.sessions ?? [],
			orchestrators: host?.orchestrators ?? [],
			orchestratorId: host?.orchestratorId ?? null,
			stats: host?.stats ?? {},
			activeProjectId: resolveActiveProject(app.activeProjectId, projects, host?.projectsKnown ?? false),
			connection: host?.connection ?? "closed",
			notificationsUnread: host?.notificationsUnread ?? 0,
			loading: host?.loading ?? false,
			error: host?.error ?? null,
			errorStatus: host?.errorStatus ?? null,
			unreachable: isDesktopUnreachable({ connection: host?.connection ?? "closed", error: host?.error ?? null, errorStatus: host?.errorStatus ?? null }),
			getLastSyncAt: () => host?.lastSyncAt ?? 0,
			refresh: () => app.refreshHost(hostId),
			spawn: (opts) => app.spawn({ ...opts, hostId }),
			launchConductor: (projectId, clean, mode) => app.launchConductor(projectId, clean, mode, hostId),
			merge: (pr) => app.merge(pr, hostId),
			kill: (id) => app.kill(id, hostId),
			renameWorker: (id, name) => app.renameWorker(id, name, hostId),
			setWorkerPinned: (id, pinned) => app.setWorkerPinned(id, pinned, hostId),
			restore: (id) => app.restore(id, hostId),
			resumeAgent: (id) => app.resumeAgent(id, hostId),
			send: (id, message) => app.send(id, message, hostId),
		};
	}, [app, host, hostId, isSelected]);
	return <AppContext.Provider value={scoped}>{children}</AppContext.Provider>;
}

function HostConversationTransport({ config }: { config: ServerConfig }) {
	useConversationEventTransport(config);
	return null;
}

// Convenience selectors -------------------------------------------------------

export function useVisibleSessions(): DashboardSession[] {
	const { sessions, activeProjectId } = useApp();
	return useMemo(
		() => (activeProjectId === "all" ? sessions : sessions.filter((s) => s.projectId === activeProjectId)),
		[sessions, activeProjectId],
	);
}

export function usePRs() {
	const sessions = useVisibleSessions();
	return useMemo(() => collectPRs(sessions), [sessions]);
}

// Provider --------------------------------------------------------------------

export function AppProvider({ children }: { children: ReactNode }) {
	const [pairedHosts, setPairedHosts] = useState<Host[]>([]);
	const [selectedHostId, setSelectedHostId] = useState<string | null>(null);
	const [config, setConfig] = useState<ServerConfig | null>(null);
	// Whether resolution has finished at least once. Distinguishes "no config
	// yet" from "no machine paired" — identical as state, opposite to the user.
	const [configResolved, setConfigResolved] = useState(false);
	const [selectedHostName, setSelectedHostName] = useState<string | null>(null);
	const [activeEndpoints, setActiveEndpoints] = useState<Endpoint[]>([]);
	const [knownProjects, setKnownProjects] = useState<KnownProjects>(NO_PROJECTS_KNOWN);
	const [sessions, setSessions] = useState<DashboardSession[]>([]);
	const [orchestrators, setOrchestrators] = useState<OrchestratorLink[]>([]);
	const [sessionMachine, setSessionMachine] = useState("");
	const [orchestratorId, setOrchestratorId] = useState<string | null>(null);
	const [stats, setStats] = useState<DashboardStats>({});
	const [chosenProjectId, setChosenProjectId] = useState<string>(ALL_PROJECTS);
	const [connection, setConnection] = useState<ConnStatus>("closed");
	const [notificationsUnread, setNotificationsUnread] = useState(0);
	const [loading, setLoading] = useState(true);
	const [error, setError] = useState<string | null>(null);
	const [errorStatus, setErrorStatus] = useState<number | null>(null);
	const cfgRef = useRef<ServerConfig | null>(null);
	const configResolution = useRef(0);
	// Gate for the connected event: emit only on the not-open -> open transition,
	// never on every poll tick. openRef tracks the current state; everConnectedRef
	// tells a fresh launch apart from a later reconnect.
	const openRef = useRef(false);
	// Whether the most recent poll reached the daemon. Distinct from openRef,
	// which latches on first connect and never clears.
	const lastTickOkRef = useRef(false);
	// When the last successful poll landed, for the stale-data banner. 0 means
	// "never synced".
	//
	// A ref rather than state, and read through a stable getter below, because a
	// fresh timestamp in the context value on every successful tick would
	// re-render every consumer of this store once per poll — which is precisely
	// what "re-render the board on a change, not on the poll tick" removed. Only
	// the banner subscribes to the passage of time; the board does not.
	const lastSyncAtRef = useRef(0);
	// Whether the last failure had no HTTP status — nothing answered at all,
	// which is what leaving a network looks like.
	const lastFailUnreachableRef = useRef(false);
	const everConnectedRef = useRef(false);
	// Mirrors appActive for code that runs mid-flight, where reading the state
	// value would see a stale closure. fetchAll consults it between requests so a
	// poll interrupted by backgrounding does not fire its remaining calls — each
	// would carry the install-id header and keep the device "live" on the desktop
	// past the point the user left the app.
	const pollActiveRef = useRef(true);

	// The poll is the daemon's liveness signal (see shouldPoll), so it must stop
	// while backgrounded rather than rely on the OS suspending the JS timer
	// whenever it feels like it.
	const [appActive, setAppActive] = useState(() => shouldPoll(RNAppState.currentState));
	const otherHosts = useOtherHosts(pairedHosts, selectedHostId, appActive);

	// Warm the install id cache as early as possible so the first REST poll tick
	// (fired from the config effect below) can send X-AO-Install-Id synchronously
	// via cachedInstallId() in api.ts's req(). A module-load side effect would run
	// this before React Native's AsyncStorage native module is guaranteed ready;
	// a mount-time effect matches this file's existing pattern (see the active
	// project load just below) and keeps the async I/O inside the component
	// lifecycle instead of hidden at import time.
	useEffect(() => {
		void primeInstallId();
	}, []);

	// Load persisted active project once.
	useEffect(() => {
		AsyncStorage.getItem(ACTIVE_PROJECT_KEY).then((v) => {
			if (v) setChosenProjectId(v);
		});
	}, []);

	// Tracks a run of failed polls so a dead endpoint can trigger another race.
	const failStreak = useRef(0);
	const lastReRaceAt = useRef(0);
	// Set when the app returns to the foreground, consumed by the upgrade check.
	const resumedRef = useRef(false);

	const reloadConfig = useCallback(async (options?: ConnectOptions): Promise<ServerConfig | null> => {
		const resolution = ++configResolution.current;
		// Races the active machine's endpoints rather than reading one stored
		// address, so the app lands on LAN at home and the tunnel from anywhere
		// else without the user choosing. Failed identity probes leave the
		// selected machine offline instead of reusing an unverified address.
		// Marked resolved whatever happens below. An unhandled failure here would
		// otherwise leave the loader up forever, which is a worse failure than
		// the blank screen this flag exists to prevent.
		try {
			const selected = await activeHost();
			if (resolution !== configResolution.current) return null;
			setSelectedHostName(selected?.name ?? null);
			setSelectedHostId(selected?.id ?? null);
			if ((selected?.id ?? "") !== (cfgRef.current?.hostId ?? "")) {
				// Hide the previous machine's board and stop its in-flight polls
				// before waiting for the newly selected machine to answer.
				cfgRef.current = null;
				setConfig(null);
				setConfigResolved(false);
				setSessions([]);
				setOrchestrators([]);
				setSessionMachine("");
				setOrchestratorId(null);
				setStats({});
				setNotificationsUnread(0);
				lastSyncAtRef.current = 0;
			}
			// Let every other paired machine connect while the selected one's
			// endpoint race is still waiting for an unreachable LAN or tunnel.
			const hosts = await loadHosts().catch(() => null);
			if (resolution !== configResolution.current) return null;
			if (hosts) setPairedHosts((previous) => sameHostConnections(previous, hosts) ? previous : hosts);
			const c = await resolveActiveConfig(runtimeResolveDeps(options));
			if (resolution !== configResolution.current) return null;
		// Keep the previous object when the endpoint has not actually changed.
		// Resolution builds a fresh one every time, and the live conversation
		// stream, the poll loop and the terminal mux all key on this value's
		// identity — handing them a new object for the same endpoint tears them
		// down and rebuilds them, which showed up as chat replies arriving only
		// on the next poll instead of streaming in.
		// Stamped here so every race counts towards the cooldown, however it was
		// triggered — otherwise a failure race and an upgrade race can fire back
		// to back and thrash the connection.
			lastReRaceAt.current = Date.now();
			const prev = cfgRef.current;
			const next = sameServerConfig(prev, c) ? (prev as typeof c) : c;
			cfgRef.current = next;
			setConfig(next);
			// Read alongside the config so a failure can be explained: a stored
			// tunnel that no longer answers is a rotated hostname, not a machine
			// that is merely out of range.
			const active = await activeHost().catch(() => selected);
			if (resolution !== configResolution.current) return null;
			setActiveEndpoints(active?.endpoints ?? []);
			setSelectedHostId(active?.id ?? null);
			return next;
		} catch (cause) {
			if (!(cause instanceof IncompatibleHostVersionError)) throw cause;
			if (resolution !== configResolution.current) return null;
			cfgRef.current = null;
			setConfig(null);
			setError(cause.message);
			setErrorStatus(426);
			setConnection("closed");
			return null;
		} finally {
			if (resolution === configResolution.current) {
				setConfigResolved(true);
				// An unreachable selected host must not hide other paired hosts.
				const hosts = await loadHosts().catch(() => null);
				if (resolution === configResolution.current && hosts) {
					setPairedHosts((previous) => sameHostConnections(previous, hosts) ? previous : hosts);
				}
			}
		}
	}, []);

	useEffect(() => {
		const sub = RNAppState.addEventListener("change", (state) => {
			const active = shouldPoll(state);
			if (active === pollActiveRef.current) return;
			pollActiveRef.current = active;
			if (!active) {
				// A LAN address can point at a different machine when the phone wakes.
				// Keep display rows, but never reuse its old bearer connection.
				configResolution.current++;
				cfgRef.current = null;
				setConfig(null);
				setConfigResolved(false);
				setConnection("closed");
			} else {
				resumedRef.current = true;
				void reloadConfig();
			}
			setAppActive(active);
		});
		return () => sub.remove();
	}, [reloadConfig]);

	const switchHost = useCallback(async (id: string) => {
		// Stop A's in-flight polls before the persisted selection changes. A
		// response that lands after this point must not repopulate B's screen.
		configResolution.current++;
		cfgRef.current = null;
		setConfig(null);
		setConfigResolved(false);
		try {
			await setActiveHost(id);
		} catch (error) {
			await reloadConfig();
			throw error;
		}
		await reloadConfig();
	}, [reloadConfig]);

	useEffect(() => {
		reloadConfig();
	}, [reloadConfig]);

	// An offline selected host still needs to reconnect when its network returns.
	// Without this, a failed initial endpoint race leaves it closed until a tap.
	useEffect(() => {
		if (!appActive || !configResolved || selectedHostId === null || config) return;
		const timer = setInterval(() => void reloadConfig(), 15_000);
		return () => clearInterval(timer);
	}, [appActive, configResolved, selectedHostId, config, reloadConfig]);

	// Nothing re-picks a path while the current one answers, so once the app
	// fell to the tunnel it stayed there even after Wi-Fi came back — observed
	// on device, holding a Cloudflare connection with a working LAN unused.
	// This is the only thing that moves the app back up the preference order.
	useEffect(() => {
		if (!config || !isConfigured(config) || !appActive) return;
		let stopped = false;
		const check = async () => {
			if (stopped) return;
			const resumed = resumedRef.current;
			resumedRef.current = false;
			let known: Endpoint[] = [];
			try {
				known = (await activeHost())?.endpoints ?? [];
			} catch {
				return; // Storage unavailable: leave the working connection alone.
			}
			if (stopped) return;
			if (
				shouldRaceForUpgrade({
					currentKind: config.endpointKind,
					known,
					lastRaceAt: lastReRaceAt.current,
					now: Date.now(),
					resumed,
				})
			) {
				// Racing is safe even when nothing better answers: reloadConfig
				// keeps the previous config object when the endpoint is unchanged,
				// so the streams keyed on it are not torn down for nothing.
				void reloadConfig();
			}
		};
		void check();
		const id = setInterval(check, UPGRADE_RACE_CHECK_MS);
		return () => {
			stopped = true;
			clearInterval(id);
		};
	}, [config, appActive, reloadConfig]);

	// fetchAll returns false when it hit an auth failure (missing/wrong password
	// or a 429 lockout). The poll loop uses that to STOP hammering: a phone that
	// keeps polling with a bad password would otherwise rack up a failed attempt
	// every few seconds and keep the daemon's brute-force lockout armed forever.
	// Polling resumes when the config changes (the user fixes the password and
	// reconnects), which re-runs the effect below.
	const fetchAll = useCallback(async (): Promise<boolean> => {
		const c = cfgRef.current;
		if (!c || !isConfigured(c)) {
			setConnection("closed");
			setNotificationsUnread(0);
			setLoading(false);
			return false;
		}
		try {
			// getSessions returns projects, so don't fetch /projects again alongside
			// it — that duplicate doubled the auth attempts spent per failing tick.
			const sess = await getSessions(c, "all");
			// A poll that started against the previous pairing must not publish any
			// of its board state after the user has moved to another machine.
			if (!pollResultIsCurrent(c, cfgRef.current)) return false;
			setKnownProjects((prev) => retainProjects(
				prev,
				{ machine: machineIdentity(c), projects: sess.projects },
				machineIdentity(c),
			));
			setSessions(sess.sessions);
			setOrchestrators(sess.orchestrators);
			setSessionMachine(machineIdentity(c));
			setOrchestratorId(sess.orchestratorId);
			setStats(sess.stats);
			setError(null);
			setErrorStatus(null);
			setConnection("open");
			lastTickOkRef.current = true;
			lastSyncAtRef.current = Date.now();
			if (!openRef.current) {
				openRef.current = true;
				const trigger = everConnectedRef.current ? "reconnect" : "launch";
				everConnectedRef.current = true;
				mobileTelemetry()?.capture(MOBILE_EVENTS.connected, { trigger });
			}
			// Badge count for the board's bell. Deliberately after the session fetch
			// and separately caught: an older daemon without /notifications must not
			// knock the board offline. limit:1 because we only read unreadCount.
			// The app may have gone to the background while the sessions request was
			// in flight. Stop here rather than spending another request that would
			// re-mark this device live after the user left.
			if (!pollActiveRef.current || !pollResultIsCurrent(c, cfgRef.current)) return false;
			try {
				const page = await getNotifications(c, { status: "unread", limit: 1 });
				if (!pollResultIsCurrent(c, cfgRef.current)) return false;
				setNotificationsUnread(page.unreadCount);
			} catch {
				if (!pollResultIsCurrent(c, cfgRef.current)) return false;
				setNotificationsUnread(0);
			}
			return true;
		} catch (e) {
			if (!pollResultIsCurrent(c, cfgRef.current)) return false;
			lastTickOkRef.current = false;
			const msg = userFacingError(e, "Failed to load");
			setError(msg);
			// Keep the HTTP status alongside the raw message so screens can render
			// human copy via describeConnectionFailure instead of surfacing strings
			// like "401 - missing or invalid connection password". Null means the
			// server was never reached (DNS failure, refused, timeout).
			const status = e instanceof ApiError ? e.status : undefined;
			// No status means the server was never reached. That is the signal to
			// race again immediately rather than ride out another poll.
			lastFailUnreachableRef.current = status === undefined;
			setErrorStatus(status ?? null);
			openRef.current = false;
			setConnection("closed");
			// Auth failures are not transient — don't keep polling into a lockout.
			// Network/other errors are transient, so keep polling for recovery.
			// Decided from the status, not the message text: see shouldKeepPolling.
			const keepPolling = shouldKeepPolling(status);
			if (status === 426) {
				cfgRef.current = null;
				setConfig(null);
			}
			if (await rejectedEndpointNeedsRace(c, status) && pollResultIsCurrent(c, cfgRef.current)) {
				// A different machine can acquire the same LAN address while the app
				// stays foregrounded. Drop that URL before racing verified endpoints.
				cfgRef.current = null;
				setConfig(null);
				void reloadConfig().catch(() => {});
			}
			return keepPolling;
		} finally {
			if (pollResultIsCurrent(c, cfgRef.current)) setLoading(false);
		}
	}, [reloadConfig]);

	// (Re)start the REST poll whenever the config changes. Stops polling on an
	// auth failure so the phone can't lock itself out by hammering a bad password.
	useEffect(() => {
		// A config change (unpair / re-pair / new host) restarts polling; reset the
		// connected gate so the first open of the new session is a real transition.
		openRef.current = false;
		if (!config || !isConfigured(config)) {
			setConnection("closed");
			// Not simply false: until resolution has finished this is "still
			// finding a path", and turning the loader off here left the screen
			// rendering an empty list — a black screen — for the whole race.
			setLoading(shouldShowLoading({ resolved: configResolved, configured: false }));
			return;
		}
		if (!appActive) return; // backgrounded: stop polling, stop heartbeating
		setLoading(true);
		setConnection("connecting");
		let stopped = false;
		const tick = async () => {
			if (stopped) return;
			const keepGoing = await fetchAll();
			if (!keepGoing) {
				stopped = true;
				return;
			}
			// fetchAll reports success by opening the connection. A run of
			// failures means the endpoint we raced onto is gone — the usual cause
			// is leaving the Wi-Fi network the LAN address belonged to — so race
			// the candidates again and pick up the tunnel.
			if (lastTickOkRef.current) {
				failStreak.current = 0;
				return;
			}
			failStreak.current += 1;
			const now = Date.now();
			if (
				shouldReRace({
					consecutiveFailures: failStreak.current,
					lastReRaceAt: lastReRaceAt.current,
					now,
					unreachable: lastFailUnreachableRef.current,
				})
			) {
				lastReRaceAt.current = now;
				failStreak.current = 0;
				void reloadConfig();
			}
		};
		void tick();
		// Paced by which endpoint won: the event stream cannot deliver over the
		// tunnel, so the poll is the only live signal there and has to be quick.
		// The effect re-runs whenever the config changes, so switching paths
		// re-paces this without anything extra.
		const poll = setInterval(() => void tick(), pollIntervalFor(config));
		return () => {
			clearInterval(poll);
			// Clearing the interval does not stop a tick already in flight, and
			// every request it makes carries the install-id header, so an
			// in-flight fetchAll would keep the device "live" past backgrounding.
			// Marking the closure stopped ends the loop at the next await
			// boundary instead of one whole request-timeout later.
			stopped = true;
		};
	}, [config, fetchAll, appActive, reloadConfig, configResolved]);

	const setActiveProject = useCallback((id: string) => {
		setChosenProjectId(id);
		AsyncStorage.setItem(ACTIVE_PROJECT_KEY, id).catch(() => {});
	}, []);

	// During a re-pair, the previous machine's retained list is not evidence
	// about the new machine. Keep it hidden until the active machine answers.
	const activeMachine = config && isConfigured(config) ? machineIdentity(config) : "";
	const visibleSessions = useMemo(
		() => sessionRowsForMachine(sessions, sessionMachine, activeMachine),
		[sessions, sessionMachine, activeMachine],
	);
	const visibleOrchestrators = useMemo(
		() => sessionRowsForMachine(orchestrators, sessionMachine, activeMachine),
		[orchestrators, sessionMachine, activeMachine],
	);
	const { projects, known: projectsKnown } = projectsForMachine(
		knownProjects,
		activeMachine,
	);
	const activeProjectId = useMemo(
		() => resolveActiveProject(chosenProjectId, projects, projectsKnown),
		[chosenProjectId, projects, projectsKnown],
	);
	const hostStates = useMemo<HostSnapshot[]>(() => pairedHosts.map((host) => {
		if (host.id !== selectedHostId) return otherHosts.snapshots[host.id] ?? emptyHostSnapshot(host);
		return {
			hostId: host.id,
			name: host.name,
			endpoints: activeEndpoints,
			config,
			connection,
			loading,
			error,
			errorStatus,
			projects,
			projectsKnown,
			sessions: visibleSessions,
			orchestrators: visibleOrchestrators,
			orchestratorId,
			stats,
			notificationsUnread,
			lastSyncAt: lastSyncAtRef.current,
		};
	}), [pairedHosts, selectedHostId, otherHosts.snapshots, activeEndpoints, config, connection, loading, error, errorStatus, projects, projectsKnown, visibleSessions, visibleOrchestrators, orchestratorId, stats, notificationsUnread]);
	const allSessions = useMemo<HostedSession[]>(() => hostStates.flatMap((host) => host.sessions.map((session) => ({ ...session, hostId: host.hostId, hostName: host.name }))), [hostStates]);
	const allProjects = useMemo<HostedProject[]>(() => hostStates.flatMap((host) => host.projects.map((project) => ({ ...project, hostId: host.hostId, hostName: host.name }))), [hostStates]);
	const allOrchestrators = useMemo<HostedOrchestrator[]>(() => hostStates.flatMap((host) => host.orchestrators.map((link) => ({ ...link, hostId: host.hostId, hostName: host.name }))), [hostStates]);
	const configForHost = useCallback((hostId: string): ServerConfig | null => {
		if (hostId === selectedHostId) return cfgRef.current;
		return otherHosts.configForHost(hostId);
	}, [selectedHostId, otherHosts.configForHost]);
	const requireConfig = useCallback((hostId?: string): ServerConfig => {
		const c = hostId === undefined ? cfgRef.current : configForHost(hostId);
		if (!c || (hostId !== undefined && !hostRouteMatches(hostId, c.hostId))) throw new Error("Host is not connected");
		return c;
	}, [configForHost]);
	const refreshHost = useCallback(async (hostId: string) => {
		if (hostId === selectedHostId) await fetchAll();
		else await otherHosts.refreshHost(hostId);
	}, [selectedHostId, fetchAll, otherHosts.refreshHost]);
	const refreshAll = useCallback(async () => {
		await Promise.all(hostStates.map((host) => refreshHost(host.hostId)));
	}, [hostStates, refreshHost]);

	// Pick a sensible project for actions that need one (spawn / conductor).
	const targetProject = useCallback((): string | null => {
		if (activeProjectId !== ALL_PROJECTS) return activeProjectId;
		if (projects.length === 1) return projects[0].id;
		return null;
	}, [activeProjectId, projects]);

	const spawn = useCallback(
		async ({ hostId, projectId, prompt, harness, model, mode, attachments, clientRequestId }: SpawnOptions) => {
			const resolvedMode = mode ?? "chat";
			return trackFeature("spawn", async () => {
				const c = requireConfig(hostId);
				const hostProjects = hostStates.find((host) => host.hostId === hostId)?.projects ?? [];
				const proj = projectId ?? (hostId === selectedHostId ? targetProject() : hostProjects.length === 1 ? hostProjects[0].id : null);
				if (!proj) throw new Error("Pick a project first");
				const session = await delegateTask(c, {
					projectId: proj,
					brief: prompt ?? "",
					agent: harness,
					model,
					mode: resolvedMode,
					attachments,
					clientRequestId,
				});
				await refreshHost(hostId);
				return session;
			}, { mode: resolvedMode });
		},
		[targetProject, requireConfig, hostStates, selectedHostId, refreshHost],
	);

	const launchConductor = useCallback(
		async (projectId: string, clean = false, mode: SessionMode = "chat", hostId?: string) =>
			trackFeature("conductor", async () => {
				const c = requireConfig(hostId);
				const link = await apiLaunchOrchestrator(c, projectId, clean, mode);
				await refreshHost(c.hostId ?? "");
				return link;
			}),
		[requireConfig, refreshHost],
	);

	const merge = useCallback(
		async (pr: DashboardPR, hostId?: string) =>
			trackFeature("merge", async () => {
				const c = requireConfig(hostId);
				await apiMergePR(c, pr);
				await refreshHost(c.hostId ?? "");
			}),
		[requireConfig, refreshHost],
	);

	const kill = useCallback(
		async (id: string, hostId?: string) =>
			trackFeature("kill", async () => {
				const c = requireConfig(hostId);
				await killSession(c, id);
				await refreshHost(c.hostId ?? "");
			}),
		[requireConfig, refreshHost],
	);

	const renameWorker = useCallback(
		async (id: string, displayName: string, hostId?: string) => {
			const c = requireConfig(hostId);
			await apiRenameSession(c, id, displayName);
			await refreshHost(c.hostId ?? "");
		},
		[requireConfig, refreshHost],
	);

	const setWorkerPinned = useCallback(
		async (id: string, pinned: boolean, hostId?: string) => {
			const c = requireConfig(hostId);
			await (pinned ? apiPinSession(c, id) : apiUnpinSession(c, id));
			await refreshHost(c.hostId ?? "");
		},
		[requireConfig, refreshHost],
	);

	const restore = useCallback(
		async (id: string, hostId?: string) =>
			trackFeature("restore", async () => {
				const c = requireConfig(hostId);
				await restoreSession(c, id);
				await refreshHost(c.hostId ?? "");
			}),
		[requireConfig, refreshHost],
	);

	// Distinct from restore, and the chat screen already relies on the
	// difference: a terminated AO session is restored, a merely stopped
	// agent/controller is resumed without resurrecting the session around it.
	const resumeAgent = useCallback(
		async (id: string, hostId?: string) =>
			trackFeature("restore", async () => {
				const c = requireConfig(hostId);
				await resumeSessionAgent(c, id);
				await refreshHost(c.hostId ?? "");
			}),
		[requireConfig, refreshHost],
	);

	const send = useCallback(async (id: string, message: string, hostId?: string) => {
		await trackFeature("send", () => sendMessage(requireConfig(hostId), id, message));
	}, [requireConfig]);
	const refresh = useCallback(async () => {
		await fetchAll();
	}, [fetchAll]);

	// Memoized so the provider doesn't hand every useApp() consumer a brand-new
	// object (causing re-renders) on each render. Re-renders now track real state changes.
	// Stable for the life of the provider, which is what lets it sit in the memo's
	// dependency list below without ever busting it.
	const getLastSyncAt = useCallback(() => lastSyncAtRef.current, []);

	const value = useMemo<AppState>(
		() => ({
			config,
			configured: !!config && isConfigured(config),
			currentHostId: selectedHostId ?? undefined,
			selectedHostName,
			hostStates,
			allSessions,
			allProjects,
			allOrchestrators,
			configResolved,
			activeEndpoints,
			projects,
			projectsKnown,
			sessions: visibleSessions,
			orchestrators: visibleOrchestrators,
			orchestratorId: sessionMachine === activeMachine ? orchestratorId : null,
			stats: sessionMachine === activeMachine ? stats : {},
			activeProjectId,
			connection,
			notificationsUnread: sessionMachine === activeMachine ? notificationsUnread : 0,
			loading,
			error,
			errorStatus,
			unreachable: isDesktopUnreachable({ connection, error, errorStatus }),
			getLastSyncAt,
			reloadConfig,
			switchHost,
			refresh,
			refreshHost,
			refreshAll,
			configForHost,
			setActiveProject,
			spawn,
			launchConductor,
			merge,
			kill,
			renameWorker,
			setWorkerPinned,
			restore,
			resumeAgent,
			send,
		}),
		[
			config,
			selectedHostId,
			selectedHostName,
			hostStates,
			allSessions,
			allProjects,
			allOrchestrators,
			configResolved,
			projects,
			projectsKnown,
			visibleSessions,
			visibleOrchestrators,
			sessionMachine,
			activeMachine,
			orchestratorId,
			stats,
			activeProjectId,
			connection,
			notificationsUnread,
			loading,
			error,
			errorStatus,
			getLastSyncAt,
			reloadConfig,
			switchHost,
			refresh,
			refreshHost,
			refreshAll,
			configForHost,
			setActiveProject,
			spawn,
			launchConductor,
			merge,
			kill,
			renameWorker,
			setWorkerPinned,
			restore,
			resumeAgent,
			send,
		],
	);

	return <AppContext.Provider value={value}>
		{/* Exactly one live CDC stream per connected host, independent of which
		    routes remain mounted in the navigation stack. */}
		{hostStates.map((host) => host.config && host.connection === "open"
			? <HostConversationTransport key={host.hostId} config={host.config} />
			: null)}
		{children}
	</AppContext.Provider>;
}
