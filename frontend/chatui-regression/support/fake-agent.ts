import type { Page } from "@playwright/test";

import type { AoBridge } from "../../src/preload";
import type { DaemonStatus } from "../../src/shared/daemon-status";
import { coerceUiSettings, DEFAULT_UI_SETTINGS } from "../../src/shared/ui-locale";

// Local fork of the renderer fake-agent boundary. ChatUI needs interface-mode
// mutation and arbitrary CDC emission, but those capabilities must stay outside
// the ordinary E2E source graph so the opt-in harness remains local-only.

export type FakeWorker = {
	id: string;
	title: string;
	createdAt?: string;
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
	setMode: (id: string, mode: "chat" | "tui", terminalHandleId?: string) => void;
	setTerminalHandle: (id: string, handleId: string) => void;
	setPreview: (id: string, previewUrl: string, previewRevision?: number) => void;
	setBrowserError: (message: string | null) => void;
	emitCDC: (input: {
		type?: string;
		sessionId?: string;
		conversationId?: string;
	}) => void;
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
				terminalHandleId: (w.mode ?? "tui") === "tui" ? `${w.id}/terminal_0` : undefined,
				workspaceId: projectId,
				workspaceName: projectName,
				title: w.title,
				provider: w.provider ?? "codex",
				kind: "worker",
				mode: w.mode ?? "tui",
				branch: w.branch ?? `session/${w.id}`,
				status: w.status ?? "working",
				createdAt: w.createdAt ?? nowIso,
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
				setMode: (id, mode, terminalHandleId) => {
					const s = findSession(id);
					if (!s) return;
					s.mode = mode;
					if (mode === "tui") {
						s.terminalHandleId = terminalHandleId || `${id}/terminal_0`;
					} else {
						delete s.terminalHandleId;
					}
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
				emitCDC: ({
					type = "session_updated",
					sessionId,
					conversationId,
				}) => {
					const stream =
						type === "workspace_changed" && sessionId
							? `/api/v1/sessions/${encodeURIComponent(sessionId)}/workspace/events`
							: "/api/v1/events";
					emit(
						stream,
						type,
						JSON.stringify({
							sessionId,
							payload: { conversationId },
						}),
					);
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
			const chatDraftRiskBridge = { setChatDraftRisk: () => undefined };
			const ao = {
				app: {
					getVersion: async () => version,
					chooseDirectory: async () => null,
					openExternal: async () => undefined,
					scanImportFolder: async ({ path }: { path: string }) => ({ path, repos: [] }),
					checkAncestorRepo: async () => undefined,
					getPathForFile: () => "",
					onOpenFolderPath: () => () => undefined,
					onNewSessionShortcut: unsubscribe,
					onKeyboardShortcutsHelp: unsubscribe,
					onNewShellTerminalShortcut: unsubscribe,
					onCloseShellTerminalShortcut: unsubscribe,
					setCloseShellTerminalShortcutEnabled: () => undefined,
					...chatDraftRiskBridge,
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
				editorHandoff: {
					getState: async () => ({
						targets: [
							{ id: "cursor" as const, name: "Cursor", kind: "editor" as const },
							{ id: "file-manager" as const, name: "File Manager", kind: "file_manager" as const },
							{ id: "terminal" as const, name: "Terminal", kind: "terminal" as const },
						],
						preferredEditorId: "cursor" as const,
						workspaceAvailable: true,
					}),
					open: async () => ({ id: "cursor" as const, name: "Cursor", kind: "editor" as const }),
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
					get: async () => ({ ...DEFAULT_UI_SETTINGS }),
					set: async (settings) => coerceUiSettings({ ...DEFAULT_UI_SETTINGS, ...settings }),
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
				cloudCp: {
					request: async () => ({ status: 401, headers: {}, body: "" }),
					openStream: async () => ({ streamId: "stream_test" }),
					closeStream: () => undefined,
					onStreamEvent: unsubscribe,
				},
			} satisfies AoBridge;
			(window as unknown as { ao: unknown }).ao = ao;
		},
		{ version, daemonPort, projectId, projectName, platform, workers },
	);
}
