import { useQueryClient } from "@tanstack/react-query";
import { RotateCcw } from "lucide-react";
import {
	createContext,
	useCallback,
	useContext,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
	type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { terminalTargetBelongsToSession, type TerminalTarget } from "../types/terminal";
import { sessionIsActive, type WorkspaceSession } from "../types/workspace";
import type { Theme } from "../stores/ui-store";
import {
	useTerminalSession,
	type AttachableTerminal,
	type TerminalSessionState,
} from "../hooks/useTerminalSession";
import { useSessionBrowserLink } from "../hooks/useSessionBrowserLink";
import { getApiBaseUrl } from "../lib/api-client";
import {
	createTerminalMux,
	createTerminalMuxPool,
	muxUrlFromApiBase,
	type TerminalMux,
	type TerminalMuxPool,
} from "../lib/terminal-mux";
import { cn } from "../lib/utils";
import { useWorkspaceQuery, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { useRestoreSession } from "../hooks/useRestoreSession";
import { useShellTerminals } from "../hooks/useShellTerminals";
import { useCloudCp } from "../hooks/useCloudCp";
import { createCloudTerminalMux } from "../lib/cloud-terminal-mux";
import { XtermTerminal } from "./XtermTerminal";
import { RestoreUnavailableDialog } from "./RestoreUnavailableDialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type TerminalPaneProps = {
	session?: WorkspaceSession;
	theme: Theme;
	daemonReady: boolean;
	terminalTarget?: TerminalTarget;
	fontSize: number;
	/** Resize this terminal without changing application zoom. */
	onChangeFontSize?: (delta: number) => void;
	isFullscreen?: boolean;
	/** Enter or exit fullscreen for the terminal pane that owns this xterm. */
	onToggleFullscreen?: () => void | Promise<void>;
	/** Refuse agent PTY input while a controller transition owns the source. */
	inputDisabled?: boolean;
	/** Focus the terminal when an in-flight controller asks for human input. */
	focusRequested?: boolean;
	/** Observe attachment state without taking ownership of the terminal lifecycle. */
	onTerminalStateChange?: (state: TerminalSessionState) => void;
	/** One-shot input initiated by an explicit UI action. */
	inputRequest?: { id: number; data: string };
	/** Reports whether the active attachment accepted a one-shot input request. */
	onInputRequestResult?: (id: number, accepted: boolean) => void;
	/** Provider-owned shared transport lease factory. */
	createMux?: () => TerminalMux;
};

type TerminalCacheDescriptor = {
	cacheKey: string;
	generation?: string;
	handleId: string;
	kind: "shell" | "worker";
	ownerKey: string;
	sessionId?: string;
};

type CachedTerminalEntry = TerminalCacheDescriptor & {
	activationId: number;
	activationPhase: "parked" | "visible";
	container: HTMLDivElement;
	discardOnDeactivate?: boolean;
	props: TerminalPaneProps;
	terminal?: AttachableTerminal;
};

type ActiveTerminalEntry = {
	key: string;
	slot: HTMLDivElement;
};

type TerminalCacheController = {
	activate: (descriptor: TerminalCacheDescriptor, props: TerminalPaneProps, slot: HTMLDivElement) => void;
	deactivate: (cacheKey: string, slot: HTMLDivElement) => void;
	update: (cacheKey: string, props: TerminalPaneProps) => void;
};

const TerminalCacheContext = createContext<TerminalCacheController | null>(null);

function terminalTargetMatches(left?: TerminalTarget, right?: TerminalTarget): boolean {
	if (left === right) return true;
	if (!left || !right || left.kind !== right.kind) return false;
	if (left.kind === "worker" && right.kind === "worker") return true;
	if (left.kind === "reviewer" && right.kind === "reviewer") {
		return left.handleId === right.handleId && left.harness === right.harness;
	}
	if (left.kind === "shell" && right.kind === "shell") {
		return (
			left.handleId === right.handleId &&
			left.generation === right.generation &&
			left.title === right.title
		);
	}
	return false;
}

function terminalPropsMatch(left: TerminalPaneProps, right: TerminalPaneProps): boolean {
	return (
		left.session === right.session &&
		left.theme === right.theme &&
		left.daemonReady === right.daemonReady &&
		left.fontSize === right.fontSize &&
		left.onChangeFontSize === right.onChangeFontSize &&
		left.isFullscreen === right.isFullscreen &&
		left.onToggleFullscreen === right.onToggleFullscreen &&
		left.inputDisabled === right.inputDisabled &&
		left.focusRequested === right.focusRequested &&
		left.onTerminalStateChange === right.onTerminalStateChange &&
		left.inputRequest === right.inputRequest &&
		left.onInputRequestResult === right.onInputRequestResult &&
		left.createMux === right.createMux &&
		terminalTargetMatches(left.terminalTarget, right.terminalTarget)
	);
}

function cacheDescriptor(
	session: WorkspaceSession | undefined,
	terminalTarget: TerminalTarget | undefined,
): TerminalCacheDescriptor | null {
	if (terminalTarget?.kind === "shell") {
		if (!terminalTargetBelongsToSession(terminalTarget, session?.id)) return null;
		const ownerKey = `shell:${session?.id ?? "standalone"}:${terminalTarget.handleId}`;
		return {
			cacheKey: `${ownerKey}|handle:${terminalTarget.handleId}|generation:${terminalTarget.generation}`,
			generation: terminalTarget.generation,
			handleId: terminalTarget.handleId,
			kind: "shell",
			ownerKey,
			sessionId: session?.id,
		};
	}

	// Reviewer terminals stream directly into a fresh mount and are intentionally
	// outside the retained worker/shell cache.
	if (terminalTarget?.kind === "reviewer") return null;
	const handleId = session?.terminalHandleId;
	if (!session?.id || !handleId) return null;
	const ownerKey = `session:${session.id}:worker`;
	const generation = session.terminalGeneration ?? "";
	return {
		cacheKey: `${ownerKey}|handle:${handleId}|generation:${generation}`,
		generation,
		handleId,
		kind: "worker",
		ownerKey,
		sessionId: session.id,
	};
}

function blurTerminal(container: HTMLElement): void {
	const active = document.activeElement;
	if (active instanceof HTMLElement && container.contains(active)) {
		active.blur();
	}
}

function setTerminalPhase(
	entry: CachedTerminalEntry,
	phase: CachedTerminalEntry["activationPhase"],
): void {
	entry.activationPhase = phase;
	entry.container.dataset.terminalActivationPhase = phase;
	const interactive = phase === "visible";
	entry.container.inert = !interactive;
	if (interactive) {
		entry.container.removeAttribute("aria-hidden");
		entry.container.style.pointerEvents = "";
	} else {
		entry.container.setAttribute("aria-hidden", "true");
		entry.container.style.pointerEvents = "none";
	}
	if (interactive) {
		entry.container.style.visibility = "";
	} else {
		entry.container.style.visibility = "hidden";
	}
}

function parkTerminal(entry: CachedTerminalEntry, parking: HTMLDivElement): void {
	entry.activationId += 1;
	const rect = entry.container.getBoundingClientRect();
	if (rect.width > 0) entry.container.style.width = `${rect.width}px`;
	if (rect.height > 0) entry.container.style.height = `${rect.height}px`;
	blurTerminal(entry.container);
	setTerminalPhase(entry, "parked");
	parking.appendChild(entry.container);
}

function showTerminal(entry: CachedTerminalEntry, slot: HTMLDivElement): void {
	entry.activationId += 1;
	// Do not hide a retained terminal while it crosses xterm's two paint-frame
	// preparation cycle: that made every return to a tab flash blank.
	setTerminalPhase(entry, "visible");
	entry.container.style.width = "100%";
	entry.container.style.height = "100%";
	slot.appendChild(entry.container);
}

function CachedTerminalPortal({
	active,
	entry,
	onFatal,
	onTerminalReady,
}: {
	active: boolean;
	entry: CachedTerminalEntry;
	onFatal: (cacheKey: string, message: string) => void;
	onTerminalReady: (cacheKey: string, terminal: AttachableTerminal) => void;
}) {
	const handleFatal = useCallback(
		(message: string) => onFatal(entry.cacheKey, message),
		[entry.cacheKey, onFatal],
	);
	const handleTerminalReady = useCallback(
		(terminal: AttachableTerminal) => {
			onTerminalReady(entry.cacheKey, terminal);
		},
		[entry.cacheKey, onTerminalReady],
	);
	useLayoutEffect(() => {
		const terminal = entry.terminal;
		if (!active || entry.activationPhase !== "visible" || !terminal) return;
		// Fit and scroll after the host is visible. This retains the cache's
		// settled viewport behavior without putting a blank frame in front of it.
		void terminal.prepareForActivation();
	}, [
		active,
		entry,
		entry.activationId,
		entry.activationPhase,
		entry.terminal,
	]);
	return createPortal(
		<AttachedTerminal
			{...entry.props}
			isVisible={active && entry.activationPhase === "visible"}
			onFatal={handleFatal}
			onTerminalReady={handleTerminalReady}
		/>,
		entry.container,
		entry.cacheKey,
	);
}

/**
 * Owns retained terminal renderers above the routed SessionView. Portal
 * containers never change; only the containers themselves move between the
 * active pane slot and an off-screen parking lot, so React, xterm, and the
 * terminal attachment all keep the same lifetime.
 */
export function TerminalCacheProvider({
	children,
	daemonReady,
	theme,
}: {
	children: ReactNode;
	daemonReady: boolean;
	theme: Theme;
}) {
	const workspaceQuery = useWorkspaceQuery();
	const shellTerminalsQuery = useShellTerminals();
	const entriesRef = useRef(new Map<string, CachedTerminalEntry>());
	const activeRef = useRef<ActiveTerminalEntry | null>(null);
	const parkingRef = useRef<HTMLDivElement | null>(null);
	const muxPoolRef = useRef<TerminalMuxPool | null>(null);
	if (!muxPoolRef.current) {
		muxPoolRef.current = createTerminalMuxPool(() =>
			createTerminalMux(muxUrlFromApiBase(getApiBaseUrl())),
		);
	}
	const muxPool = muxPoolRef.current;
	// Cloud sessions do not share the pooled local-daemon socket: each runs in its
	// own control-plane sandbox reached over its own ticketed WebSocket. Resolve a
	// stable, per-session cloud mux factory so terminal props stay referentially
	// equal across renders; local sessions keep the pooled daemon mux untouched.
	const { client: cloudClient, baseUrl: cloudBaseUrl } = useCloudCp();
	// A cloud pane must never fall back to the local daemon mux, even during the
	// startup window before settings resolve the control-plane URL: the local
	// daemon does not know a cloud session's handle and would report it as exited.
	// Read the control-plane client/URL through a ref so the factory (cached once
	// per session for referential stability) always uses the current values at
	// connect time rather than whatever was resolved on first render.
	const cloudCpRef = useRef({ client: cloudClient, baseUrl: cloudBaseUrl });
	cloudCpRef.current = { client: cloudClient, baseUrl: cloudBaseUrl };
	const cloudMuxFactoriesRef = useRef(new Map<string, () => TerminalMux>());
	const resolveCreateMux = useCallback(
		(paneSession?: WorkspaceSession): (() => TerminalMux) => {
			const cloud = paneSession?.cloud;
			if (!cloud) return muxPool.acquire;
			const cached = cloudMuxFactoriesRef.current.get(paneSession.id);
			if (cached) return cached;
			const sessionId = paneSession.id;
			const orgId = cloud.orgId;
			const factory = () =>
				createCloudTerminalMux({
					wsBaseUrl: `${cloudCpRef.current.baseUrl.replace(/^http/i, "ws").replace(/\/+$/, "")}/api/cloud/v1`,
					kind: "agent",
					mintTicket: async () => {
						const response = await cloudCpRef.current.client.createTerminalTicket(orgId, sessionId, {
							kind: "agent",
						});
						return response.ticket;
					},
				});
			cloudMuxFactoriesRef.current.set(sessionId, factory);
			return factory;
		},
		[muxPool],
	);
	const [, setRevision] = useState(0);
	const rerender = useCallback(() => setRevision((current) => current + 1), []);

	const removeEntry = useCallback(
		(cacheKey: string) => {
			const entry = entriesRef.current.get(cacheKey);
			if (!entry) return;
			const active = activeRef.current;
			if (active?.key === cacheKey) {
				blurTerminal(entry.container);
				setTerminalPhase(entry, "parked");
				activeRef.current = null;
			}
			entriesRef.current.delete(cacheKey);
			entry.container.remove();
			rerender();
		},
		[rerender],
	);

	const activate = useCallback(
		(descriptor: TerminalCacheDescriptor, props: TerminalPaneProps, slot: HTMLDivElement) => {
			const parking = parkingRef.current;
			if (!parking) return;
			const cachedProps = { ...props, createMux: resolveCreateMux(props.session) };

			const previous = activeRef.current;
			if (previous && previous.key !== descriptor.cacheKey) {
				const previousEntry = entriesRef.current.get(previous.key);
				if (previousEntry) {
					parkTerminal(previousEntry, parking);
					if (previousEntry.discardOnDeactivate) {
						entriesRef.current.delete(previous.key);
						previousEntry.container.remove();
					}
				}
			}

			// A logical terminal can have only one generation. A replacement
			// handle immediately disposes the retained renderer and writer for the
			// old generation, even if an opaque handle is later reused elsewhere.
			for (const entry of entriesRef.current.values()) {
				if (entry.ownerKey === descriptor.ownerKey && entry.cacheKey !== descriptor.cacheKey) {
					if (activeRef.current?.key === entry.cacheKey) parkTerminal(entry, parking);
					entriesRef.current.delete(entry.cacheKey);
					entry.container.remove();
				}
			}

			let entry = entriesRef.current.get(descriptor.cacheKey);
			if (!entry) {
				const container = document.createElement("div");
				// The normal terminal surface is intentionally translucent. A retained
				// terminal host must be opaque, though: during route/cache hand-off an
				// older frame can still be composited beneath it for a paint, which would
				// otherwise produce a visible double terminal.
				container.className = "h-full min-h-0 w-full overflow-hidden bg-terminal-opaque";
				container.style.position = "relative";
				container.dataset.terminalCacheKey = descriptor.cacheKey;
				entry = {
					...descriptor,
					activationId: 0,
					activationPhase: "parked",
					container,
					props: cachedProps,
				};
				entriesRef.current.set(entry.cacheKey, entry);
			} else {
				entry.props = cachedProps;
			}
			showTerminal(entry, slot);
			activeRef.current = { key: entry.cacheKey, slot };
			rerender();
		},
		[muxPool, rerender],
	);

	const deactivate = useCallback(
		(cacheKey: string, slot: HTMLDivElement) => {
			const active = activeRef.current;
			if (active?.key !== cacheKey || active.slot !== slot) return;
			const entry = entriesRef.current.get(cacheKey);
			const parking = parkingRef.current;
			activeRef.current = null;
			if (!entry) return;
			if (entry.discardOnDeactivate || !parking) {
				entriesRef.current.delete(cacheKey);
				entry.container.remove();
				rerender();
				return;
			}
			parkTerminal(entry, parking);
			rerender();
		},
		[rerender],
	);

	const update = useCallback(
		(cacheKey: string, props: TerminalPaneProps) => {
			const entry = entriesRef.current.get(cacheKey);
			const cachedProps = { ...props, createMux: resolveCreateMux(props.session) };
			if (!entry || terminalPropsMatch(entry.props, cachedProps)) return;
			entry.props = cachedProps;
			rerender();
		},
		[muxPool, rerender],
	);

	const markFatal = useCallback(
		(cacheKey: string, _message: string) => {
			const entry = entriesRef.current.get(cacheKey);
			if (!entry) return;
			if (activeRef.current?.key !== cacheKey) {
				removeEntry(cacheKey);
				return;
			}
			// Renderer initialization failed. AttachedTerminal owns the visible
			// error surface; mark the entry only so it is discarded when the user
			// leaves instead of retaining an unusable renderer.
			entry.discardOnDeactivate = true;
		},
		[removeEntry],
	);

	const markTerminalReady = useCallback(
		(cacheKey: string, terminal: AttachableTerminal) => {
			const entry = entriesRef.current.get(cacheKey);
			if (!entry) return;
			entry.terminal = terminal;
			rerender();
		},
		[rerender],
	);

	// Daemon readiness and theme are shell-wide. Parked entries must observe
	// them too so reconnect and rendering behavior never depends on the route
	// that happened to be active when they were parked.
	useEffect(() => {
		let changed = false;
		for (const entry of entriesRef.current.values()) {
			if (entry.props.daemonReady === daemonReady && entry.props.theme === theme) continue;
			entry.props = { ...entry.props, daemonReady, theme };
			changed = true;
		}
		if (changed) rerender();
	}, [daemonReady, rerender, theme]);

	// Project/session teardown is an ownership boundary, not an LRU event.
	// Dispose retained terminal clients as soon as the authoritative workspace
	// snapshot no longer contains their logical session.
	useEffect(() => {
		if (!workspaceQuery.isSuccess) return;
		const sessions = new Map(
			(workspaceQuery.data ?? []).flatMap((workspace) =>
				workspace.sessions.map((session) => [session.id, session] as const),
			),
		);
		for (const entry of entriesRef.current.values()) {
			const session = entry.sessionId ? sessions.get(entry.sessionId) : undefined;
			if (entry.sessionId && !session) {
				removeEntry(entry.cacheKey);
				continue;
			}
			if (
				entry.kind === "worker" &&
				session &&
				(session.terminalHandleId !== entry.handleId ||
					(session.terminalGeneration ?? "") !== (entry.generation ?? ""))
			) {
				removeEntry(entry.cacheKey);
				continue;
			}
			if (session && entry.props.session !== session) {
				entry.props = { ...entry.props, session };
				rerender();
			}
		}
	}, [removeEntry, workspaceQuery.data, workspaceQuery.isSuccess]);

	// Shell handles have their own lifecycle outside WorkspaceSession. Closing a
	// shell must close its retained mux writer even if it was parked.
	useEffect(() => {
		if (!shellTerminalsQuery.isSuccess) return;
		const shells = new Map(
			(shellTerminalsQuery.data ?? []).map((terminal) => [terminal.handleId, terminal] as const),
		);
		for (const entry of entriesRef.current.values()) {
			if (entry.kind !== "shell") continue;
			const shell = shells.get(entry.handleId);
			if (
				!shell ||
				shell.createdAt !== entry.generation ||
				shell.sessionId !== entry.sessionId
			) {
				removeEntry(entry.cacheKey);
				continue;
			}
			const target = entry.props.terminalTarget;
			if (target?.kind === "shell" && target.title !== shell.title) {
				entry.props = {
					...entry.props,
					terminalTarget: { ...target, title: shell.title },
				};
				rerender();
			}
		}
	}, [removeEntry, shellTerminalsQuery.data, shellTerminalsQuery.isSuccess]);

	// The provider is the final shell ownership boundary. React disposes the
	// portals; remove their externally-created host nodes as well.
	useEffect(
		() => () => {
			activeRef.current = null;
			for (const entry of entriesRef.current.values()) entry.container.remove();
			entriesRef.current.clear();
			muxPool.dispose();
		},
		[muxPool],
	);

	const controller = useMemo<TerminalCacheController>(
		() => ({ activate, deactivate, update }),
		[activate, deactivate, update],
	);

	return (
		<TerminalCacheContext.Provider value={controller}>
			<div
				ref={parkingRef}
				aria-hidden="true"
				className="pointer-events-none invisible fixed top-0 -left-[100000px]"
				data-testid="terminal-cache-parking"
			/>
			{/* The parking ref must commit before routed children run their layout
			    effects; those effects synchronously move the active container into
			    the pane slot before the browser can paint. */}
			{children}
			{[...entriesRef.current.values()].map((entry) => (
				<CachedTerminalPortal
					active={activeRef.current?.key === entry.cacheKey}
					entry={entry}
					key={entry.cacheKey}
					onFatal={markFatal}
					onTerminalReady={markTerminalReady}
				/>
			))}
		</TerminalCacheContext.Provider>
	);
}

function CachedTerminalSlot({
	descriptor,
	props,
}: {
	descriptor: TerminalCacheDescriptor;
	props: TerminalPaneProps;
}) {
	const cache = useContext(TerminalCacheContext);
	const slotRef = useRef<HTMLDivElement | null>(null);

	useLayoutEffect(() => {
		const slot = slotRef.current;
		if (!cache || !slot) return;
		cache.activate(descriptor, props, slot);
		return () => cache.deactivate(descriptor.cacheKey, slot);
	}, [cache, descriptor.cacheKey, descriptor.handleId, descriptor.kind, descriptor.ownerKey, descriptor.sessionId]);

	useLayoutEffect(() => {
		cache?.update(descriptor.cacheKey, props);
	}, [cache, descriptor.cacheKey, props]);

	return <div className="h-full min-h-0 w-full" data-testid="session-terminal-slot" ref={slotRef} />;
}

export function TerminalPane({
	session,
	theme,
	daemonReady,
	terminalTarget: requestedTerminalTarget,
	fontSize,
	onChangeFontSize,
	isFullscreen,
	onToggleFullscreen,
	inputDisabled,
	focusRequested,
	onTerminalStateChange,
	inputRequest,
	onInputRequestResult,
}: TerminalPaneProps) {
	const { t } = useTranslation();
	const terminalTarget =
		requestedTerminalTarget &&
		terminalTargetBelongsToSession(requestedTerminalTarget, session?.id)
			? requestedTerminalTarget
			: ({ kind: "worker" } satisfies TerminalTarget);
	const isOptimisticShell =
		terminalTarget.kind === "shell" && terminalTarget.handleId.startsWith("pending-shell:");
	const cache = useContext(TerminalCacheContext);
	const terminalKey =
		terminalTarget?.kind === "reviewer" || terminalTarget?.kind === "shell"
			? terminalTarget.handleId
			: `${session?.terminalHandleId ?? "empty"}:${session?.terminalGeneration ?? ""}`;

	if (!window.ao) {
		// A standalone shell has no agent and no branch, so it previews as a plain
		// prompt rather than borrowing the session's agent transcript.
		if (terminalTarget?.kind === "shell") {
			return (
				<pre
					className="h-full overflow-auto bg-terminal p-4 font-mono leading-relaxed text-terminal"
					style={{ fontSize }}
				>
					<span className="text-terminal-dim">{terminalTarget.title}</span> $ {"\n"}
					<span className="text-terminal-dim">
						{"(standalone shell — a live PTY here in the desktop app)"}
						{"\n"}
					</span>
				</pre>
			);
		}
		const provider = terminalTarget?.kind === "reviewer" ? terminalTarget.harness : (session?.provider ?? "claude");
		const lines =
			terminalTarget?.kind === "reviewer" ? reviewerPreviewLines(session) : workerPreviewLines(session, provider);
		return (
			<pre
				className="h-full overflow-auto bg-terminal p-4 font-mono leading-relaxed text-terminal-foreground"
				data-testid="session-terminal"
				style={{ fontSize }}
			>
				<span className="text-terminal-dim">~/{session?.workspaceName ?? "reverbcode"}</span>{" "}
				{session?.branch ? <span className="text-accent">{session.branch}</span> : null}
				{session?.branch ? " " : ""}$ {provider}
				{"\n"}
				{lines.map((line, index) => (
					<span
						key={`${line}:${index}`}
						className={
							line.startsWith("PASS") || line.startsWith("DONE")
								? "text-success"
								: line.startsWith("WARN") || line.startsWith("TODO")
									? "text-warning"
									: line.startsWith("$") || line.startsWith("▲")
										? "text-accent"
										: "text-terminal-foreground"
						}
					>
						{line}
						{"\n"}
					</span>
				))}
			</pre>
		);
	}
	// The tab is intentionally selected before the daemon allocates its handle.
	// Keep that short bridge visually indistinguishable from an empty terminal,
	// rather than surfacing a separate loading state or attaching xterm to a
	// handle that cannot exist yet.
	if (isOptimisticShell) {
		return (
			<div
				aria-label={t("terminal.shellAria")}
				className="terminal-surface h-full"
				data-testid="optimistic-terminal"
			/>
		);
	}

	const props = {
		session,
		theme,
		daemonReady,
		terminalTarget,
		fontSize,
		onChangeFontSize,
		isFullscreen,
		onToggleFullscreen,
		inputDisabled,
		focusRequested,
		onTerminalStateChange,
		inputRequest,
		onInputRequestResult,
	};
	const descriptor = cacheDescriptor(session, terminalTarget);
	if (cache && descriptor) {
		return <CachedTerminalSlot descriptor={descriptor} props={props} />;
	}

	return (
		<AttachedTerminal
			key={terminalKey}
			session={session}
			theme={theme}
			daemonReady={daemonReady}
			fontSize={fontSize}
			isFullscreen={isFullscreen}
			inputDisabled={inputDisabled}
			onChangeFontSize={onChangeFontSize}
			onToggleFullscreen={onToggleFullscreen}
			focusRequested={focusRequested}
			onTerminalStateChange={onTerminalStateChange}
			inputRequest={inputRequest}
			onInputRequestResult={onInputRequestResult}
			terminalTarget={terminalTarget}
		/>
	);
}

function workerPreviewLines(session: WorkspaceSession | undefined, provider: string): string[] {
	if (session?.id === "ao-demo-orchestrator") {
		return [
			"> Go through my Linear backlog and let's plan which tasks to spawn off",
			"",
			"Ran 3 shell commands",
			"",
			"Here's the backlog triage. Half of it is already in flight — don't spawn those.",
			"",
			"Already covered — don't spawn (session → PR):",
			"— terminal polish → PR #318, changes requested",
			"— browser preview stack → PRs #319/#320, in review",
			"— README screenshot assets → PR #323, approved and mergeable",
			"",
			"Plan: 3 sessions worth spawning",
			"",
			"┌───┬────────────────────┬──────────────────────────────────────────┬──────────────────────────────────┐",
			"│ # │ Session            │ Scope                                    │ Why now                          │",
			"├───┼────────────────────┼──────────────────────────────────────────┼──────────────────────────────────┤",
			"│ 1 │ new-task-flake     │ NewTaskDialog smoke test flakes on Enter │ Failing PR #324's e2e; small fix │",
			"│ 2 │ checkout-retries   │ e2e retries leak state between runs      │ Flakes 1-in-5 on CI; well scoped │",
			"│ 3 │ session-pr-surface │ PR checks missing on board cards         │ Additive; touches board only     │",
			"└───┴────────────────────┴──────────────────────────────────────────┴──────────────────────────────────┘",
			"",
			"Want me to spawn all three? I'd put #1–2 on codex and #3 on claude-code.",
			"",
			"> yes, spawn all three",
			"",
			"Running 3 shell commands…",
			'└ $ ao spawn --project ao-demo --name "new-task-flake" --agent codex --prompt',
			'  "Fix the flaky NewTaskDialog smoke test: submit is debounced 300ms while the',
			'  e2e check asserts synchronously. Reproduce, fix, and push to update PR #324."',
			"PASS 3 sessions spawned — board updated",
		];
	}
	if (session?.id === "demo-review-stack") {
		return [
			'$ rg "previewUrl|Browser" frontend/src/renderer',
			"frontend/src/renderer/components/SessionInspector.tsx: Browser tab selected after ao preview",
			"frontend/src/renderer/hooks/useBrowserView.ts: preview revision re-navigates the view",
			"$ ao preview http://localhost:5173",
			"DONE preview target set for demo-review-stack",
			"$ npm --prefix frontend run typecheck",
			"PASS TypeScript project references are clean",
			"TODO wait for reviewer on PR #320 before merging the stack",
		];
	}
	if (session?.id === "demo-working") {
		return [
			`$ ${provider} --continue`,
			"Reading renderer board and inspector components...",
			"Updated demo workspace data for README screenshots",
			"$ npm --prefix frontend test -- SessionsBoard SessionInspector",
			"PASS 18 tests passed",
			"DONE board has Working, Needs you, In review, and Ready to merge populated",
		];
	}
	if (session?.id === "demo-needs-input") {
		return [
			"$ git diff --stat",
			"frontend/src/renderer/components/TerminalPane.tsx | 41 +++++++++++++++++",
			"frontend/src/renderer/styles.css                 | 27 +++++++++++",
			"WARN reviewer requested a tighter terminal activity sample",
			"TODO confirm whether to keep the toolbar density change",
		];
	}
	if (session?.id === "demo-ci-failed") {
		return [
			"╭────────────────────────────────────────────╮",
			"│ >_ OpenAI Codex (v0.133.0)                 │",
			"│ model:        gpt-5.5 high  /model to change",
			"│ directory:    ~/ao-demo/demo-new-task-flake",
			"│ permissions:  YOLO mode                    │",
			"╰────────────────────────────────────────────╯",
			"",
			"• I'll start from the failing check output, reproduce the Enter-submit",
			"  path locally, then patch and push.",
			"",
			"• Ran npm test -- NewTaskDialog",
			"└ PASS 12 tests passed",
			"",
			"▲ ao send · CI failed on PR #324. The failing checks are e2e (NewTaskDialog",
			"  submits with Enter). Investigate and push a fix.",
			"",
			'• Ran rg -n "onKeyDown|Enter" src/components/NewTaskDialog.tsx',
			'└ src/components/NewTaskDialog.tsx:88:  onKeyDown={(e) => e.key === "Enter" && scheduleSubmit()}',
			"  src/components/NewTaskDialog.tsx:141: const scheduleSubmit = debounce(submit, 300)",
			"  … +42 lines (ctrl + t to view transcript)",
			"",
			"Found it: submit is debounced 300ms, the check asserts immediately.",
			"Patching the handler and re-running e2e…",
		];
	}
	return [
		`$ ${provider} --status`,
		"Reading task context and local diff...",
		"Running focused validation for the current session",
		"PASS demo terminal is populated for screenshots",
	];
}

function reviewerPreviewLines(session: WorkspaceSession | undefined): string[] {
	return [
		"$ ao review submit --session " + (session?.id ?? "demo-session"),
		"Reviewing PR #319: browser preview rail renders inside AO",
		"PASS implementation matches the requested README screenshot flow",
		"Reviewing PR #320: stacked PR review rows",
		"WARN keep multiple review rows visible before taking the screenshot",
		"DONE submitted batched review results",
	];
}

// Agents whose full-screen TUI keeps its own transcript and scrolls it only by
// keyboard, ignoring SGR wheel reports. The terminal routes the wheel to
// PageUp/PageDown for these (see XtermTerminal's paneScrollsByKeyboard).
// kilocode is a fork of opencode and shares its TUI surface; grok also uses a
// full-screen keyboard-scroll TUI, so both scroll the same way. Muse Code uses
// the normal terminal buffer instead and must keep the SGR -> tmux scroll path.
const KEYBOARD_SCROLL_PROVIDERS = new Set(["opencode", "kilocode", "grok"]);

// Whether the given provider's TUI is one of the keyboard-scroll agents above.
export function providerScrollsByKeyboard(provider?: string): boolean {
	return provider ? KEYBOARD_SCROLL_PROVIDERS.has(provider) : false;
}

function bannerText(
	state: TerminalSessionState,
	t: TFunction,
	hasAttached: boolean,
	isCloud: boolean,
	error?: string,
): string | undefined {
	// Cloud only: before the first successful open (the sandbox worker is still
	// coming up), show a calm "Connecting…" rather than the alarming
	// "disconnected — reattaching". A local terminal keeps its original wording
	// verbatim, so local behavior is unchanged.
	if (state === "reattaching") {
		return isCloud && !hasAttached ? t("terminal.connecting") : t("terminal.reattaching");
	}
	if (state === "error") return t("terminal.error", { error: error ?? t("terminal.connectionFailed") });
	return undefined;
}

function AttachedTerminal({
	session,
	theme,
	daemonReady,
	terminalTarget,
	fontSize,
	onChangeFontSize,
	isFullscreen,
	onToggleFullscreen,
	inputDisabled,
	focusRequested,
	onTerminalStateChange,
	inputRequest,
	onInputRequestResult,
	createMux,
	isVisible = true,
	onFatal,
	onTerminalReady,
}: TerminalPaneProps & {
	isVisible?: boolean;
	onFatal?: (message: string) => void;
	onTerminalReady?: (terminal: AttachableTerminal) => void;
}) {
	const { t } = useTranslation();
	const attachSession =
		terminalTarget?.kind === "shell"
			? undefined
			: session && terminalTarget?.kind === "reviewer"
			? { ...session, terminalHandleId: terminalTarget.handleId }
			: session;
	// One terminal instance per logical-terminal + handle generation. The shell
	// cache retains this component across route switches; a replacement handle
	// gets a new component rather than inheriting stale screen/input state.
	const [terminal, setTerminal] = useState<AttachableTerminal | null>(null);
	const lastInputRequestIdRef = useRef<number | null>(null);
	const [initFailed, setInitFailed] = useState(false);
	const [isRestoring, setIsRestoring] = useState(false);
	const [restoreError, setRestoreError] = useState<string | undefined>();
	const [restoreUnavailable, setRestoreUnavailable] = useState(false);
	const queryClient = useQueryClient();
	const restoreSessionById = useRestoreSession();
	// A shell pane has no session, so it hands the hook its handle directly
	// instead of reading one off `attachSession`.
	const shellTerminalHandleId = terminalTarget?.kind === "shell" ? terminalTarget.handleId : undefined;
	const { attach, state, error, replaySettled, hasAttached, syncVisibleSize } = useTerminalSession(attachSession, {
		coverInitialReplay: terminalTarget?.kind !== "reviewer",
		createMux,
		daemonReady,
		inputDisabled,
		isVisible,
		shellTerminalHandleId,
	});
	useEffect(() => {
		onTerminalStateChange?.(state);
	}, [onTerminalStateChange, state]);
	useEffect(() => {
		if (!terminal || state !== "attached" || !inputRequest) return;
		if (lastInputRequestIdRef.current === inputRequest.id) return;
		const accepted = terminal.sendUserInput(inputRequest.data);
		if (accepted) lastInputRequestIdRef.current = inputRequest.id;
		onInputRequestResult?.(inputRequest.id, accepted);
	}, [inputRequest, onInputRequestResult, state, terminal]);
	// xterm's write callback means the replay has been parsed, not that the
	// browser has painted its final viewport. Keep the first-load cover mounted
	// through the same render/paint preparation used when activating a retained
	// terminal; otherwise large TUI replays can visibly repaint from their first
	// row immediately after the loading cover disappears.
	const [replayPaintPending, setReplayPaintPending] = useState(!replaySettled);
	useLayoutEffect(() => {
		if (!replaySettled) {
			setReplayPaintPending(true);
			return;
		}
		if (!replayPaintPending || !terminal) return;
		let current = true;
		void terminal.prepareForActivation().then(() => {
			if (current) setReplayPaintPending(false);
		});
		return () => {
			current = false;
		};
	}, [replayPaintPending, replaySettled, terminal]);
	const handleId = shellTerminalHandleId ?? attachSession?.terminalHandleId;
	const provider = terminalTarget?.kind === "reviewer" ? terminalTarget.harness : session?.provider;
	const isSessionActive = session ? sessionIsActive(session) : false;
	// A standalone shell is never restorable: there is no session row to restore.
	const canRestoreSession =
		terminalTarget?.kind !== "reviewer" &&
		terminalTarget?.kind !== "shell" &&
		session !== undefined &&
		!isSessionActive;

	const handleReady = useCallback((handle: AttachableTerminal) => {
		setTerminal(handle);
	}, []);
	useLayoutEffect(() => {
		if (terminal) onTerminalReady?.(terminal);
	}, [onTerminalReady, terminal]);
	const handleInitError = useCallback((err: unknown) => {
		console.error("xterm failed to initialize", err);
		setInitFailed(true);
	}, []);
	useEffect(() => {
		if (initFailed) {
			onFatal?.("renderer initialization failed");
			onTerminalStateChange?.("error");
			return;
		}
	}, [initFailed, onFatal, onTerminalStateChange]);
	const handleLinkOpen = useSessionBrowserLink(session);
	const restoreSession = useCallback(async () => {
		if (!session?.id || !canRestoreSession || isRestoring) return;
		setIsRestoring(true);
		setRestoreError(undefined);
		try {
			const result = await restoreSessionById(session.id);
			if (result.status === "not_resumable") {
				setRestoreUnavailable(true);
				return;
			}
			if (result.status === "error") {
				setRestoreError(result.message);
			}
		} catch (err) {
			setRestoreError(err instanceof Error ? err.message : t("terminal.unableRestore"));
		} finally {
			setIsRestoring(false);
		}
	}, [canRestoreSession, isRestoring, restoreSessionById, session?.id, t]);

	useEffect(() => {
		if (!terminal) return;
		let current = true;
		let detach: (() => void) | undefined;
		// A new xterm starts at its constructor default (80×24). Opening the PTY
		// before FitAddon has measured its real slot makes full-screen worker TUIs
		// redraw once at 80×24 and again at the actual grid. Settle that first fit
		// before attaching so the daemon receives only the authoritative size.
		void terminal.prepareForActivation().then(() => {
			if (!current) return;
			detach = attach(terminal);
		});
		return () => {
			current = false;
			detach?.();
		};
	}, [terminal, handleId, attach, attachSession?.id]);

	if (initFailed) {
		return (
			<div className="terminal-surface grid h-full place-items-center p-4 font-mono text-xs text-muted-foreground">
				{t("terminal.initFailed")}
			</div>
		);
	}

	const banner = bannerText(state, t, hasAttached, Boolean(attachSession?.cloud), error);
	const showEmptyState = !handleId;
	// Cover xterm while the attachment buffers the initial replay, so the pane
	// appears already drawn at the tail instead of visibly scrolling down to it.
	// Deliberately NOT the empty state above: that renders a centered "Starting
	// session" card, and flashing it on every session switch would be worse than
	// the scroll it replaces.
	// Only while a replay is actually imminent. Gating on the state as well as
	// the gate keeps the cover from reappearing over a pane that is visibly
	// disconnected: an open timeout lifts it, the backoff reconnect would
	// otherwise pull it straight back down, and the "reattaching" banner already
	// explains that window better than a blank overlay does.
	const showReplayCover =
		Boolean(handleId) &&
		(!replaySettled || replayPaintPending) &&
		(state === "connecting" || state === "attached");
	const showEndedState = state === "exited" || canRestoreSession;
	const emptyStateTitle = session ? t("terminal.startingSession") : "Agent Orchestrator";
	const emptyStateMessage = session
		? session.kind === "orchestrator"
			? t("terminal.preparingOrchestrator")
			: t("terminal.preparingWorker")
		: t("terminal.noSessionSelected");

	return (
		<div className="terminal-surface flex h-full min-h-0 flex-col" data-testid="session-terminal">
			{showEndedState && (
				<TerminalEndedStrip
					canRestore={canRestoreSession}
					error={restoreError}
					isRestoring={isRestoring}
					onRestore={restoreSession}
					variant={
						terminalTarget?.kind === "reviewer" ? "reviewer" : terminalTarget?.kind === "shell" ? "shell" : "session"
					}
				/>
			)}
			{/* Keep a small gutter where terminal output starts, but let xterm use the
			    full top/right/bottom extent. The host fills the remaining
			    content box, so FitAddon still measures it correctly and the absolute
			    overlays (empty state, banner) keep covering the full padding box. */}
			<div className="relative min-h-0 flex-1 pl-2">
				<XtermTerminal
					ariaLabel={terminalTarget?.kind === "shell" ? t("terminal.shellAria") : t("terminal.sessionAria")}
					fontSize={fontSize}
					focusRequested={focusRequested}
					isFullscreen={isFullscreen}
					isVisible={isVisible}
					onChangeFontSize={onChangeFontSize}
					onError={handleInitError}
					onLinkOpen={handleLinkOpen}
					onReady={handleReady}
					onToggleFullscreen={onToggleFullscreen}
					onVisibleSize={syncVisibleSize}
					paneScrollsByKeyboard={providerScrollsByKeyboard(provider)}
					supportsCursorColorScheme={provider === "cursor"}
					theme={theme}
				/>
				{showEmptyState && (
					<div className="terminal-surface absolute inset-0 grid place-items-center font-mono text-control">
						<div className="text-center">
							<div className="text-terminal">{emptyStateTitle}</div>
							<div className="mt-2 text-terminal-dim">{emptyStateMessage}</div>
						</div>
					</div>
				)}
				{showReplayCover && <ReplayCover />}
				{banner && (
					<div className="absolute inset-x-3 top-2 rounded-md border border-border bg-surface/95 px-3 py-1.5 font-mono text-caption text-muted-foreground">
						{banner}
					</div>
				)}
			</div>
			{session && (
				<RestoreUnavailableDialog
					open={restoreUnavailable}
					session={session}
					onOpenChange={setRestoreUnavailable}
					onRecreated={async () => {
						await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
					}}
				/>
			)}
		</div>
	);
}

function ReplayCover() {
	return (
		// Keep this cover silent: its only job is to hide the initial replay's
		// intermediate paints. xterm remains live underneath, and pointer events
		// pass through so selection and wheel input never wait on attachment.
		<div
			aria-hidden="true"
			className="bg-terminal-opaque pointer-events-none absolute inset-0"
			data-testid="terminal-replay-cover"
		/>
	);
}

type TerminalEndedStripProps = {
	canRestore: boolean;
	error?: string;
	isRestoring: boolean;
	onRestore: () => void;
	variant: "reviewer" | "session" | "shell";
};

function TerminalEndedStrip({ canRestore, error, isRestoring, onRestore, variant }: TerminalEndedStripProps) {
	const { t } = useTranslation();
	const message = canRestore
		? t("terminal.restoreToContinue")
		: variant === "reviewer"
			? t("terminal.reviewerEnded")
			: variant === "shell"
				? t("terminal.shellExited")
				: t("terminal.sessionEndedNotTerminated");

	return (
		<div className="shrink-0 border-b border-border bg-surface/80 px-4 py-2">
			<div className="flex min-h-control-board items-center gap-3">
				<div className="min-w-0 flex-1">
					<div className="font-mono text-caption font-medium uppercase tracking-wide-md text-muted-foreground">
						{t("terminal.ended")}
					</div>
					<div className="mt-0.5 truncate text-xs text-muted-foreground">{message}</div>
				</div>
				{error && <div className="max-w-content-max truncate text-xs text-destructive">{error}</div>}
				{canRestore && (
					<Tooltip>
						<TooltipTrigger asChild>
							<span className="inline-flex">
								<button
									type="button"
									aria-label={t("terminal.restoreSession")}
									className="inline-flex size-control-form shrink-0 items-center justify-center rounded-md border border-border bg-raised text-foreground transition hover:bg-interactive-hover disabled:cursor-not-allowed disabled:opacity-50"
									disabled={isRestoring}
									onClick={onRestore}
								>
									<RotateCcw className={cn("size-icon-base", isRestoring && "animate-spin")} aria-hidden="true" />
								</button>
							</span>
						</TooltipTrigger>
						<TooltipContent side="bottom">{t("terminal.restoreSession")}</TooltipContent>
					</Tooltip>
				)}
			</div>
		</div>
	);
}
