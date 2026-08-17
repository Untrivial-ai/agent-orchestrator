import type { Page } from "@playwright/test";

import type { AoBridge } from "../../src/preload";
import type { DaemonStatus } from "../../src/shared/daemon-status";

// The e2e suite runs the renderer under `dev:web` (VITE_NO_ELECTRON=1) with no
// Electron preload, so `window.ao` is undefined and lib/bridge.ts falls back to
// a browser stub that reports the daemon as permanently "stopped" and the app
// version as "0.0.0-preview". The daemon/version smoke cases (DMN-*, INS-004)
// need a deterministic *ready* daemon and a known version string, so we inject
// a complete `window.ao` before any page script runs — the same seam the real
// Electron preload fills. This is a fake *bridge*, not a fake agent: no worker
// process and no GitHub repo are involved, matching the T0 POD constraints.
//
// In a real Linux pod running the packaged Electron build, `window.ao` is the
// live preload; the injected bridge is only the deterministic stand-in for the
// browser harness.
//
// SCOPE / CAVEAT — renderer smoke, not full e2e. Because `window.ao`,
// `EventSource`, and the workspace snapshot are all faked here, this harness
// CANNOT catch daemon, storage, API, preload, PTY, or filesystem regressions —
// those are the packaged-app pod gate's job (#2697). In particular,
// `useWorkspaceQuery` reads an already-shaped `WorkspaceSummary` straight from
// `window.__aoFakeAgent.snapshot()`, BYPASSING the generated API client + DTO
// mapping; DTO/client coverage comes from the pod gate + unit tests, never from
// these specs. Treat green here as "the renderer renders the injected state,"
// not "the boundary works."

export type FakeBridgeOptions = {
	/** Version string surfaced by app.getVersion() (Settings > Updates). */
	version?: string;
	/** Daemon lifecycle state the renderer should observe. */
	daemonState?: "ready" | "starting" | "stopped" | "error";
	/** REST port advertised when ready (mock data is served regardless). */
	daemonPort?: number;
};

export async function installFakeBridge(page: Page, opts: FakeBridgeOptions = {}): Promise<void> {
	const version = opts.version ?? "9.9.9-test";
	const daemonState = opts.daemonState ?? "ready";
	const daemonPort = opts.daemonPort ?? 8080;

	await page.addInitScript(
		({ version, daemonState, daemonPort }) => {
			const unsubscribe = () => () => undefined;
			const status: DaemonStatus =
				daemonState === "ready" ? { state: "ready", port: daemonPort } : { state: daemonState };
			const navState = (viewId: string) => ({
				viewId,
				url: "",
				title: "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
			});

			// Full AoBridge surface (mirrors src/preload.ts) so any renderer call
			// resolves — an incomplete object would throw the moment the app touched
			// a missing method.
			const ao = {
				app: {
					getVersion: async () => version,
					chooseDirectory: async () => null,
					openExternal: async () => undefined,
					scanImportFolder: async ({ path }: { path: string }) => ({ path, repos: [] }),
					checkAncestorRepo: async () => undefined,
					onNewSessionShortcut: unsubscribe,
					onKeyboardShortcutsHelp: unsubscribe,
					onNewShellTerminalShortcut: unsubscribe,
					onCloseShellTerminalShortcut: unsubscribe,
					setCloseShellTerminalShortcutEnabled: () => undefined,
					onOpenSettingsShortcut: unsubscribe,
					onPreviousSessionShortcut: unsubscribe,
					onNextSessionShortcut: unsubscribe,
					onPreviousTabShortcut: unsubscribe,
					onNextTabShortcut: unsubscribe,
					onFocusTerminalShortcut: unsubscribe,
				},
				terminal: {
					saveDroppedFile: async () => "",
					setFocused: () => undefined,
					onFontSizeShortcut: () => () => undefined,
				},
				window: {
					isMaximized: async () => false,
					onMaximized: () => () => undefined,
					isFullScreen: async () => false,
					onFullScreen: () => () => undefined,
				},
				theme: { set: async () => undefined },
				menu: { action: async () => undefined, notifyShellFocus: () => undefined },
				clipboard: {
					writeText: async () => undefined,
					readText: async () => "",
				},
				daemon: {
					getStatus: async () => status,
					start: async () => status,
					stop: async () => ({ state: "stopped" }),
					restart: async () => status,
					onStatus: (listener: (s: typeof status) => void) => {
						listener(status);
						return unsubscribe();
					},
				},
				telemetry: {
					getBootstrap: async () => null,
				},
				browser: {
					nativeCompositionEnabled: true,
					ensure: async (sessionId: string) => navState(`preview:${sessionId}`),
					setBounds: () => undefined,
					setOverlayOpen: () => undefined,
					navigate: async ({ viewId }: { viewId: string }) => navState(viewId),
					clear: async (viewId: string) => navState(viewId),
					goBack: async (viewId: string) => navState(viewId),
					goForward: async (viewId: string) => navState(viewId),
					reload: async (viewId: string) => navState(viewId),
					stop: async (viewId: string) => navState(viewId),
					getTabs: async (viewId: string) => ({
						viewId,
						activeTabId: "t1",
						tabs: [{ id: "t1", url: "", title: "", active: true }],
					}),
					selectTab: async ({ viewId, tabId }: { viewId: string; tabId: string }) => ({
						viewId,
						activeTabId: tabId,
						tabs: [{ id: tabId, url: "", title: "", active: true }],
					}),
					closeTab: async ({ viewId }: { viewId: string; tabId: string }) => ({
						viewId,
						activeTabId: "t1",
						tabs: [{ id: "t1", url: "", title: "", active: true }],
					}),
					openTab: async ({ viewId }: { viewId: string; url?: string }) => ({
						viewId,
						activeTabId: "t1",
						tabs: [{ id: "t1", url: "", title: "", active: true }],
					}),
					devtools: async (input: { viewId: string }) => ({ viewId: input.viewId, open: false, activeTabId: "" }),
					destroy: () => undefined,
					// Annotation contract (mirrors src/preload.ts): useBrowserView subscribes
					// to these whenever SessionView mounts with window.ao.browser present, so
					// an incomplete browser shape would crash the session-detail/preview specs.
					setAnnotationMode: async () => undefined,
					onAnnotationSubmit: unsubscribe,
					onAnnotationCancel: unsubscribe,
					onNavState: unsubscribe,
					onTabsState: unsubscribe,
					onAgentActivity: unsubscribe,
					onDevToolsState: unsubscribe,
				},
				notifications: {
					show: async () => undefined,
					setBadge: async (_count: number) => undefined,
					devBounce: async () => undefined,
					onClick: unsubscribe,
				},
				tray: {
					setAttentionState: () => undefined,
					onOpenSession: unsubscribe,
				},
				appState: {
					getMigration: async () => ({ status: "completed" }),
					setMigration: async () => undefined,
				},
				updateSettings: {
					get: async () => ({ enabled: false, channel: "latest", nightlyAck: false, feature: null }),
					set: async () => undefined,
				},
				uiSettings: {
					get: async () => ({ locale: "en" }),
					set: async (settings) => settings,
				},
				keybindings: {
					get: async () => ({}),
					set: async (overrides) => overrides,
					setRecording: async () => undefined,
				},
				updates: {
					getStatus: async () => ({ state: "idle" }),
					check: async () => undefined,
					returnHome: async () => undefined,
					download: async () => undefined,
					install: async () => undefined,
					onStatus: unsubscribe,
					onTelemetry: unsubscribe,
				},
				// UpdatesSection calls featureBuilds.getActive() immediately on mount; an
				// omitted namespace would surface as a swallowed React Query error.
				featureBuilds: {
					list: async () => [],
					getActive: async () => null,
				},
				cloud: {
					getSession: async () => null,
					signIn: async () => undefined,
					signOut: async () => undefined,
					onSessionChanged: unsubscribe,
				},
			} satisfies AoBridge;
			(window as unknown as { ao: unknown }).ao = ao;
		},
		{ version, daemonState, daemonPort },
	);
}

// ── Fake-agent timeline harness (FAKE tier) ─────────────────────────────────
//
// The FAKE cases in #2483 need an agent to *do* something over time (spawn a
// session, go active, hit waiting_input, move board columns, stream a terminal,
// raise a needs-input notification). Under the browser harness there is no Go
// daemon and no agent, so we simulate that timeline at the bridge:
//
//   1. A `window.ao` whose daemon is ready on a port — so the renderer sets its
//      REST base URL and opens its SSE streams (the same seam the packaged app
//      fills). The `browser.*` IPC is driven off a shared in-page state so the
//      preview surface is controllable.
//   2. A fake `window.EventSource` — the daemon's CDC (`/api/v1/events`) and
//      notification (`/api/v1/notifications/stream`) SSE streams. The controller
//      pushes `session_updated` / `notification_created` frames into it, which is
//      exactly what drives the renderer's cache-invalidation → refetch path (no
//      manual refresh), matching the real daemon's behaviour.
//   3. A mutable workspace snapshot read by `useWorkspaceQuery` via
//      `window.__aoFakeAgent.snapshot()` (dev:web seam). Controller mutations +
//      an SSE push = the card the renderer repaints.
//
// The real Go fake-agent plugin drives the same states in the later real-daemon
// pod run; here the same specs run against this bridge-level simulation.

export type FakeWorker = {
	id: string;
	title: string;
	mode?: "chat" | "tui";
	provider?: string;
	branch?: string;
	status?: string;
	activity?: "active" | "idle" | "waiting_input" | "exited";
	previewUrl?: string;
	previewRevision?: number;
};

export type FakeAgentOptions = {
	version?: string;
	daemonPort?: number;
	projectId?: string;
	projectName?: string;
	/** Override navigator.platform (e.g. "Linux …") so platform-gated UI mounts. */
	platform?: string;
	/** Worker sessions present at first paint. */
	workers?: FakeWorker[];
};

/**
 * The in-page fake-agent controller, callable from specs via `page.evaluate`.
 * Every mutator changes the snapshot AND pushes the matching SSE frame, so the
 * renderer repaints through its real invalidation path.
 */
export type FakeAgentController = {
	snapshot: () => unknown[];
	createWorker: (worker: FakeWorker) => void;
	removeWorker: (id: string) => void;
	setStatus: (id: string, status: string, activity?: string) => void;
	setTerminalHandle: (id: string, handleId: string) => void;
	setPreview: (id: string, previewUrl: string, previewRevision?: number) => void;
	setBrowserError: (message: string | null) => void;
	notify: (n: { id: string; type: string; title: string; body?: string; sessionId?: string }) => void;
};

declare global {
	interface Window {
		__aoFakeAgent?: FakeAgentController;
	}
}

/**
 * Install the fake-agent bridge + SSE + snapshot seam before any page script.
 * Drive the timeline from specs with
 * `page.evaluate(() => window.__aoFakeAgent!.setStatus(...))`.
 */
export async function installFakeAgent(page: Page, opts: FakeAgentOptions = {}): Promise<void> {
	const version = opts.version ?? "9.9.9-test";
	const daemonPort = opts.daemonPort ?? 8080;
	const projectId = opts.projectId ?? "fake-proj";
	const projectName = opts.projectName ?? "fake-proj";
	const platform = opts.platform ?? null;
	const workers = opts.workers ?? [];

	await page.addInitScript(
		({ version, daemonPort, projectId, projectName, platform, workers }) => {
			if (platform) {
				try {
					Object.defineProperty(navigator, "platform", { get: () => platform, configurable: true });
				} catch {
					/* ignore */
				}
			}

			const nowIso = new Date().toISOString();
			type Session = Record<string, unknown>;
			const makeWorker = (w: (typeof workers)[number]): Session => ({
				id: w.id,
				terminalHandleId: `${w.id}/terminal_0`,
				workspaceId: projectId,
				workspaceName: projectName,
				title: w.title,
				provider: w.provider ?? "codex",
				kind: "worker",
				mode: w.mode ?? "tui",
				branch: w.branch ?? `session/${w.id}`,
				status: w.status ?? "working",
				createdAt: nowIso,
				updatedAt: new Date().toISOString(),
				activity: { state: w.activity ?? "active", lastActivityAt: new Date().toISOString() },
				previewUrl: w.previewUrl,
				previewRevision: w.previewRevision,
				prs: [],
			});

			const project: Record<string, unknown> = {
				id: projectId,
				name: projectName,
				kind: "single_repo",
				path: `/repos/${projectName}`,
				type: "main",
				orchestratorAgent: "codex",
				sessions: [
					{
						id: `${projectId}-orchestrator`,
						terminalHandleId: `${projectId}-orchestrator/terminal_0`,
						workspaceId: projectId,
						workspaceName: projectName,
						title: "Project orchestrator",
						provider: "codex",
						kind: "orchestrator",
						branch: "main",
						status: "working",
						createdAt: nowIso,
						updatedAt: nowIso,
						activity: { state: "active", lastActivityAt: nowIso },
						prs: [],
					},
					...workers.map(makeWorker),
				],
			};

			interface FakeEventSourceLike {
				url: string;
				readyState: number;
				onopen: ((ev: unknown) => void) | null;
				onerror: ((ev: unknown) => void) | null;
				onmessage: ((ev: unknown) => void) | null;
				_listeners: Record<string, ((ev: unknown) => void)[]>;
				close: () => void;
				_dispatch: (type: string, data?: string) => void;
			}

			const state = {
				browserError: null as string | null,
				eventSources: [] as FakeEventSourceLike[],
				workspaces: [project],
			};

			const findSession = (id: string) => (project.sessions as Session[]).find((s) => s.id === id);
			const touch = (s: Session) => {
				s.updatedAt = new Date().toISOString();
			};

			class FakeEventSource implements FakeEventSourceLike {
				static readonly CONNECTING = 0;
				static readonly OPEN = 1;
				static readonly CLOSED = 2;
				readonly CONNECTING = 0;
				readonly OPEN = 1;
				readonly CLOSED = 2;
				url: string;
				readyState = 1;
				onopen: ((ev: unknown) => void) | null = null;
				onerror: ((ev: unknown) => void) | null = null;
				onmessage: ((ev: unknown) => void) | null = null;
				_listeners: Record<string, ((ev: unknown) => void)[]> = {};
				constructor(url: string) {
					this.url = String(url);
					state.eventSources.push(this);
					// event-transport assigns `.onopen` right after construction; fire on
					// the next tick so it is already wired.
					setTimeout(() => {
						if (this.readyState === 1 && this.onopen) this.onopen({ type: "open" });
					}, 0);
				}
				addEventListener(t: string, cb: (ev: unknown) => void) {
					(this._listeners[t] ||= []).push(cb);
				}
				removeEventListener(t: string, cb: (ev: unknown) => void) {
					const arr = this._listeners[t];
					if (!arr) return;
					const i = arr.indexOf(cb);
					if (i >= 0) arr.splice(i, 1);
				}
				close() {
					this.readyState = 2;
				}
				_dispatch(type: string, data = "") {
					const ev = { type, data };
					if (type === "message" && this.onmessage) this.onmessage(ev);
					for (const cb of this._listeners[type] ?? []) {
						try {
							cb(ev);
						} catch {
							/* ignore */
						}
					}
				}
			}
			(window as unknown as { EventSource: unknown }).EventSource = FakeEventSource;

			const emit = (match: string, type: string, data?: string) => {
				for (const es of state.eventSources) {
					if (es.readyState === 2) continue;
					if (!es.url.includes(match)) continue;
					es._dispatch(type, data);
				}
			};
			const pushWorkspaces = (type = "session_updated") => emit("/api/v1/events", type);

			const controller: FakeAgentController = {
				snapshot: () => JSON.parse(JSON.stringify(state.workspaces)),
				createWorker: (w) => {
					if (!findSession(w.id)) (project.sessions as Session[]).push(makeWorker(w));
					pushWorkspaces("session_created");
				},
				removeWorker: (id) => {
					const sessions = project.sessions as Session[];
					const index = sessions.findIndex((session) => session.id === id);
					if (index >= 0) sessions.splice(index, 1);
					pushWorkspaces();
				},
				setStatus: (id, status, activity) => {
					const s = findSession(id);
					if (!s) return;
					s.status = status;
					s.displayStatus = undefined;
					if (activity) s.activity = { state: activity, lastActivityAt: new Date().toISOString() };
					touch(s);
					pushWorkspaces();
				},
				setTerminalHandle: (id, handleId) => {
					const s = findSession(id);
					if (!s) return;
					s.terminalHandleId = handleId;
					touch(s);
					pushWorkspaces();
				},
				setPreview: (id, previewUrl, previewRevision) => {
					const s = findSession(id);
					if (!s) return;
					s.previewUrl = previewUrl;
					s.previewRevision = previewRevision ?? (typeof s.previewRevision === "number" ? s.previewRevision : 0) + 1;
					touch(s);
					pushWorkspaces();
				},
				setBrowserError: (message) => {
					state.browserError = message;
				},
				notify: (n) => {
					const payload = JSON.stringify({
						id: n.id,
						type: n.type,
						title: n.title,
						body: n.body ?? "",
						sessionId: n.sessionId ?? "",
						projectId,
						target: { kind: "session", sessionId: n.sessionId ?? "" },
						status: "unread",
						createdAt: new Date().toISOString(),
					});
					emit("/api/v1/notifications/stream", "notification_created", payload);
				},
			};
			(window as unknown as { __aoFakeAgent: unknown }).__aoFakeAgent = controller;

			const unsubscribe = () => () => undefined;
			const status: DaemonStatus = { state: "ready", port: daemonPort };
			const navState = (viewId: string, url = "", error?: string) => ({
				viewId,
				url,
				title: url ? "AO preview" : "",
				canGoBack: false,
				canGoForward: false,
				isLoading: false,
				...(error ? { error } : {}),
			});
			const ao = {
				app: {
					getVersion: async () => version,
					chooseDirectory: async () => null,
					openExternal: async () => undefined,
					scanImportFolder: async ({ path }: { path: string }) => ({ path, repos: [] }),
					checkAncestorRepo: async () => undefined,
					onNewSessionShortcut: unsubscribe,
					onKeyboardShortcutsHelp: unsubscribe,
					onNewShellTerminalShortcut: unsubscribe,
					onCloseShellTerminalShortcut: unsubscribe,
					setCloseShellTerminalShortcutEnabled: () => undefined,
					onOpenSettingsShortcut: unsubscribe,
					onPreviousSessionShortcut: unsubscribe,
					onNextSessionShortcut: unsubscribe,
					onPreviousTabShortcut: unsubscribe,
					onNextTabShortcut: unsubscribe,
					onFocusTerminalShortcut: unsubscribe,
				},
				terminal: {
					saveDroppedFile: async () => "",
					setFocused: () => undefined,
					onFontSizeShortcut: () => () => undefined,
				},
				window: {
					isMaximized: async () => false,
					onMaximized: () => () => undefined,
					isFullScreen: async () => false,
					onFullScreen: () => () => undefined,
				},
				theme: { set: async () => undefined },
				menu: { action: async () => undefined, notifyShellFocus: () => undefined },
				clipboard: { writeText: async () => undefined, readText: async () => "" },
				daemon: {
					getStatus: async () => status,
					start: async () => status,
					stop: async () => ({ state: "stopped" }),
					restart: async () => status,
					onStatus: (listener: (s: typeof status) => void) => {
						listener(status);
						return unsubscribe();
					},
				},
				telemetry: { getBootstrap: async () => null },
				browser: {
					nativeCompositionEnabled: true,
					ensure: async (sessionId: string) => navState(`preview:${sessionId}`),
					setBounds: () => undefined,
					setOverlayOpen: () => undefined,
					navigate: async ({ viewId, url }: { viewId: string; url: string }) =>
						state.browserError ? navState(viewId, "", state.browserError) : navState(viewId, url),
					clear: async (viewId: string) => navState(viewId),
					goBack: async (viewId: string) => navState(viewId),
					goForward: async (viewId: string) => navState(viewId),
					reload: async (viewId: string) => navState(viewId),
					stop: async (viewId: string) => navState(viewId),
					getTabs: async (viewId: string) => ({
						viewId,
						activeTabId: "t1",
						tabs: [{ id: "t1", url: "", title: "", active: true }],
					}),
					selectTab: async ({ viewId, tabId }: { viewId: string; tabId: string }) => ({
						viewId,
						activeTabId: tabId,
						tabs: [{ id: tabId, url: "", title: "", active: true }],
					}),
					closeTab: async ({ viewId }: { viewId: string; tabId: string }) => ({
						viewId,
						activeTabId: "t1",
						tabs: [{ id: "t1", url: "", title: "", active: true }],
					}),
					openTab: async ({ viewId }: { viewId: string; url?: string }) => ({
						viewId,
						activeTabId: "t1",
						tabs: [{ id: "t1", url: "", title: "", active: true }],
					}),
					devtools: async (input: { viewId: string }) => ({ viewId: input.viewId, open: false, activeTabId: "" }),
					destroy: () => undefined,
					// Annotation contract (mirrors src/preload.ts): useBrowserView subscribes
					// to these whenever SessionView mounts with window.ao.browser present, so
					// an incomplete browser shape would crash the session-detail/preview specs.
					setAnnotationMode: async () => undefined,
					onAnnotationSubmit: unsubscribe,
					onAnnotationCancel: unsubscribe,
					onNavState: unsubscribe,
					onTabsState: unsubscribe,
					onAgentActivity: unsubscribe,
					onDevToolsState: unsubscribe,
				},
				notifications: {
					show: async () => undefined,
					setBadge: async (_count: number) => undefined,
					devBounce: async () => undefined,
					onClick: unsubscribe,
				},
				tray: { setAttentionState: () => undefined, onOpenSession: unsubscribe },
				appState: { getMigration: async () => ({ status: "completed" }), setMigration: async () => undefined },
				updateSettings: {
					get: async () => ({ enabled: false, channel: "latest", nightlyAck: false, feature: null }),
					set: async () => undefined,
				},
				uiSettings: {
					get: async () => ({ locale: "en" }),
					set: async (settings) => settings,
				},
				keybindings: {
					get: async () => ({}),
					set: async (overrides) => overrides,
					setRecording: async () => undefined,
				},
				updates: {
					getStatus: async () => ({ state: "idle" }),
					check: async () => undefined,
					returnHome: async () => undefined,
					download: async () => undefined,
					install: async () => undefined,
					onStatus: unsubscribe,
					onTelemetry: unsubscribe,
				},
				// UpdatesSection calls featureBuilds.getActive() immediately on mount; an
				// omitted namespace would surface as a swallowed React Query error.
				featureBuilds: {
					list: async () => [],
					getActive: async () => null,
				},
				cloud: {
					getSession: async () => null,
					signIn: async () => undefined,
					signOut: async () => undefined,
					onSessionChanged: unsubscribe,
				},
			} satisfies AoBridge;
			(window as unknown as { ao: unknown }).ao = ao;
		},
		{ version, daemonPort, projectId, projectName, platform, workers },
	);
}
