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
	restoreSession,
	sendMessage,
	type DashboardPR,
	type DashboardSession,
	type DashboardStats,
	type OrchestratorLink,
	type ProjectInfo,
	type SessionMode,
} from "./api";
import { isConfigured, loadConfig, type ServerConfig } from "./config";
import { shouldKeepPolling } from "./connectionError";
import { primeInstallId } from "./installId";
import { collectPRs } from "./prView";
import { MOBILE_EVENTS } from "./telemetry/events";
import { mobileTelemetry, trackFeature } from "./telemetry/runtime";
import { useConversationEventTransport } from "./chat/conversationEvents";

const ACTIVE_PROJECT_KEY = "ao.activeProject";
const POLL_INTERVAL_MS = 8000;

// Board-level connection state is derived from the REST poll. The session screen
// tracks its own terminal mux connection separately.
export type ConnStatus = "closed" | "connecting" | "open";

// An options object rather than four optional positionals: `spawn(a, b, c, d)`
// with every argument optional and same-typed is where call-site mistakes live.
export type SpawnOptions = {
	/** Falls back to the active project, or the only project. */
	projectId?: string;
	prompt?: string;
	harness?: string;
	model?: string;
	/** Mobile defaults to Chat; TUI remains an explicit compatibility choice. */
	mode?: SessionMode;
};

type AppState = {
	config: ServerConfig | null;
	configured: boolean;
	projects: ProjectInfo[];
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
	// actions
	reloadConfig: () => Promise<void>;
	refresh: () => Promise<void>;
	setActiveProject: (id: string) => void;
	spawn: (opts: SpawnOptions) => Promise<DashboardSession>;
	launchConductor: (projectId: string, clean?: boolean, mode?: SessionMode) => Promise<OrchestratorLink>;
	merge: (pr: DashboardPR) => Promise<void>;
	kill: (id: string) => Promise<void>;
	restore: (id: string) => Promise<void>;
	send: (id: string, message: string) => Promise<void>;
};

const AppContext = createContext<AppState | null>(null);

export function useApp(): AppState {
	const ctx = useContext(AppContext);
	if (!ctx) throw new Error("useApp must be used within <AppProvider>");
	return ctx;
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
	const [config, setConfig] = useState<ServerConfig | null>(null);
	const [projects, setProjects] = useState<ProjectInfo[]>([]);
	const [sessions, setSessions] = useState<DashboardSession[]>([]);
	const [orchestrators, setOrchestrators] = useState<OrchestratorLink[]>([]);
	const [orchestratorId, setOrchestratorId] = useState<string | null>(null);
	const [stats, setStats] = useState<DashboardStats>({});
	const [activeProjectId, setActiveProjectId] = useState<string>("all");
	const [connection, setConnection] = useState<ConnStatus>("closed");
	const [notificationsUnread, setNotificationsUnread] = useState(0);
	const [loading, setLoading] = useState(true);
	const [error, setError] = useState<string | null>(null);
	const [errorStatus, setErrorStatus] = useState<number | null>(null);
	// Start authenticated streaming only after the REST probe succeeds. A stale
	// password must cost one failed request, not a poll plus a parallel SSE attempt.
	useConversationEventTransport(connection === "open" ? config : null);

	const cfgRef = useRef<ServerConfig | null>(null);
	// Gate for the connected event: emit only on the not-open -> open transition,
	// never on every poll tick. openRef tracks the current state; everConnectedRef
	// tells a fresh launch apart from a later reconnect.
	const openRef = useRef(false);
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

	useEffect(() => {
		const sub = RNAppState.addEventListener("change", (s) => {
			const active = shouldPoll(s);
			pollActiveRef.current = active;
			setAppActive(active);
		});
		return () => sub.remove();
	}, []);

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
			if (v) setActiveProjectId(v);
		});
	}, []);

	const reloadConfig = useCallback(async () => {
		const c = await loadConfig();
		cfgRef.current = c;
		setConfig(c);
	}, []);

	useEffect(() => {
		reloadConfig();
	}, [reloadConfig]);

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
			setProjects(sess.projects);
			setSessions(sess.sessions);
			setOrchestrators(sess.orchestrators);
			setOrchestratorId(sess.orchestratorId);
			setStats(sess.stats);
			setError(null);
			setErrorStatus(null);
			setConnection("open");
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
			if (!pollActiveRef.current) return true;
			try {
				const page = await getNotifications(c, { status: "unread", limit: 1 });
				setNotificationsUnread(page.unreadCount);
			} catch {
				setNotificationsUnread(0);
			}
			return true;
		} catch (e) {
			const msg = e instanceof Error ? e.message : "Failed to load";
			setError(msg);
			// Keep the HTTP status alongside the raw message so screens can render
			// human copy via describeConnectionFailure instead of surfacing strings
			// like "401 - missing or invalid connection password". Null means the
			// server was never reached (DNS failure, refused, timeout).
			const status = e instanceof ApiError ? e.status : undefined;
			setErrorStatus(status ?? null);
			openRef.current = false;
			setConnection("closed");
			// Auth failures are not transient — don't keep polling into a lockout.
			// Network/other errors are transient, so keep polling for recovery.
			// Decided from the status, not the message text: see shouldKeepPolling.
			return shouldKeepPolling(status);
		} finally {
			setLoading(false);
		}
	}, []);

	// (Re)start the REST poll whenever the config changes. Stops polling on an
	// auth failure so the phone can't lock itself out by hammering a bad password.
	useEffect(() => {
		// A config change (unpair / re-pair / new host) restarts polling; reset the
		// connected gate so the first open of the new session is a real transition.
		openRef.current = false;
		if (!config || !isConfigured(config)) {
			setConnection("closed");
			setLoading(false);
			return;
		}
		if (!appActive) return; // backgrounded: stop polling, stop heartbeating
		setLoading(true);
		setConnection("connecting");
		let stopped = false;
		const tick = async () => {
			if (stopped) return;
			const keepGoing = await fetchAll();
			if (!keepGoing) stopped = true;
		};
		void tick();
		const poll = setInterval(() => void tick(), POLL_INTERVAL_MS);
		return () => {
			clearInterval(poll);
			// Clearing the interval does not stop a tick already in flight, and
			// every request it makes carries the install-id header, so an
			// in-flight fetchAll would keep the device "live" past backgrounding.
			// Marking the closure stopped ends the loop at the next await
			// boundary instead of one whole request-timeout later.
			stopped = true;
		};
	}, [config, fetchAll, appActive]);

	const setActiveProject = useCallback((id: string) => {
		setActiveProjectId(id);
		AsyncStorage.setItem(ACTIVE_PROJECT_KEY, id).catch(() => {});
	}, []);

	// Pick a sensible project for actions that need one (spawn / conductor).
	const targetProject = useCallback((): string | null => {
		if (activeProjectId !== "all") return activeProjectId;
		if (projects.length === 1) return projects[0].id;
		return null;
	}, [activeProjectId, projects]);

	const spawn = useCallback(
		async ({ projectId, prompt, harness, model, mode }: SpawnOptions) =>
			trackFeature("spawn", async () => {
				const c = cfgRef.current;
				const proj = projectId ?? targetProject();
				if (!c || !proj) throw new Error("Pick a project first");
				const session = await delegateTask(c, {
					projectId: proj,
					brief: prompt ?? "",
					agent: harness,
					model,
					mode: mode ?? "chat",
				});
				await fetchAll();
				return session;
			}),
		[targetProject, fetchAll],
	);

	const launchConductor = useCallback(
		async (projectId: string, clean = false, mode: SessionMode = "chat") =>
			trackFeature("conductor", async () => {
				const c = cfgRef.current!;
				const link = await apiLaunchOrchestrator(c, projectId, clean, mode);
				await fetchAll();
				return link;
			}),
		[fetchAll],
	);

	const merge = useCallback(
		async (pr: DashboardPR) =>
			trackFeature("merge", async () => {
				await apiMergePR(cfgRef.current!, pr);
				await fetchAll();
			}),
		[fetchAll],
	);

	const kill = useCallback(
		async (id: string) =>
			trackFeature("kill", async () => {
				await killSession(cfgRef.current!, id);
				await fetchAll();
			}),
		[fetchAll],
	);

	const restore = useCallback(
		async (id: string) =>
			trackFeature("restore", async () => {
				await restoreSession(cfgRef.current!, id);
				await fetchAll();
			}),
		[fetchAll],
	);

	const send = useCallback(async (id: string, message: string) => {
		await trackFeature("send", () => sendMessage(cfgRef.current!, id, message));
	}, []);
	const refresh = useCallback(async () => {
		await fetchAll();
	}, [fetchAll]);

	// Memoized so the provider doesn't hand every useApp() consumer a brand-new
	// object (causing re-renders) on each render. Re-renders now track real state changes.
	const value = useMemo<AppState>(
		() => ({
			config,
			configured: !!config && isConfigured(config),
			projects,
			sessions,
			orchestrators,
			orchestratorId,
			stats,
			activeProjectId,
			connection,
			notificationsUnread,
			loading,
			error,
			errorStatus,
			reloadConfig,
			refresh,
			setActiveProject,
			spawn,
			launchConductor,
			merge,
			kill,
			restore,
			send,
		}),
		[
			config,
			projects,
			sessions,
			orchestrators,
			orchestratorId,
			stats,
			activeProjectId,
			connection,
			notificationsUnread,
			loading,
			error,
			errorStatus,
			reloadConfig,
			refresh,
			setActiveProject,
			spawn,
			launchConductor,
			merge,
			kill,
			restore,
			send,
		],
	);

	return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}
