import { Pencil } from "lucide-react";
import { Reorder, useDragControls } from "motion/react";
import {
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
	type PointerEvent,
	type ReactNode,
	type WheelEvent as ReactWheelEvent,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useTabScrollEdges } from "../hooks/useTabScrollEdges";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { MAX_SESSION_DISPLAY_NAME_LEN, useSessionRename } from "../hooks/useSessionRename";
import { useTruncatedText } from "../hooks/useTruncatedText";
import type { ShellTerminal } from "../hooks/useShellTerminals";
import { TERMINAL_FONT_SIZE_DEFAULT, TERMINAL_FONT_SIZE_MAX, TERMINAL_FONT_SIZE_MIN } from "../lib/design-tokens";
import { getAgentActivityView } from "../lib/session-presentation";
import { agentLabel } from "../lib/agent-options";
import { isLinuxPlatform, isMacPlatform } from "../lib/platform";
import { aoBridge } from "../lib/bridge";
import { handleTerminalTabListKeyDown } from "../lib/terminal-tabs";
import { cn } from "../lib/utils";
import { sidebarOccupiesLayout, useUiStore, type Theme } from "../stores/ui-store";
import type { TerminalTarget } from "../types/terminal";
import {
	isOrchestratorSession,
	type WorkspaceSession,
} from "../types/workspace";
import { AgentAvatar } from "./AgentAvatar";
import { ShellTerminalTab } from "./ShellTerminalTab";
import { TerminalTabFrame } from "./TerminalTabFrame";
import { TerminalPane } from "./TerminalPane";
import { SessionTopbarPortal } from "./SessionTopbarPortal";
import { ContextMenu, ContextMenuContent, ContextMenuItem, ContextMenuTrigger } from "./ui/context-menu";

type CenterPaneProps = {
	session?: WorkspaceSession;
	theme: Theme;
	daemonReady: boolean;
	terminalTarget?: TerminalTarget;
	reviewerTerminal?: { handleId: string; harness: string };
	onSelectReviewerTerminal?: (target: { handleId: string; harness: string }) => void;
	/** Standalone shells to render as tabs beside the session's own pane. */
	shellTerminals?: ShellTerminal[];
	onSelectSessionTerminal?: () => void;
	onSelectShellTerminal?: (handleId: string) => void;
	onCloseShellTerminal?: (handleId: string) => void;
	onRenameShellTerminal?: (handleId: string, title: string) => void;
	/** Workspace-level controls (e.g. shell topbar) rendered beside the tab strip. */
	topbarActions?: ReactNode;
	/** Agent-session actions (interface switch, handoff) on the primary session tab. */
	sessionTabAction?: ReactNode;
	/** Widen the primary tab's action slot while an interface switch spinner is showing. */
	sessionTabActionWide?: boolean;
	/** Pinned beside the tab strip, before the workspace topbar actions. */
	tabStripAction?: ReactNode;
	handoffDialogOpen?: boolean;
	workspaceTabs?: CenterPaneWorkspaceTab[];
	workspaceTabActions?: ReactNode;
	workspaceActiveTabKey?: string;
	/** A file overlay hides the pane, so it must not acknowledge switch UI. */
	workspaceFileActive?: boolean;
	/** Session-owned order shared with the Chat surface. */
	auxiliaryTabOrder?: string[];
	onAuxiliaryTabOrderChange?: (keys: string[]) => void;
	/** Stop forwarding the agent pane's keystrokes while its controller drains. */
	agentInputDisabled?: boolean;
};

export type CenterPaneWorkspaceTab = {
	key: string;
	content: ReactNode;
	onSelect: () => void;
	onClose?: () => void;
};

type AuxiliaryTab =
	| { key: string; kind: "reviewer"; terminal: NonNullable<CenterPaneProps["reviewerTerminal"]> }
	| { key: string; kind: "shell"; terminal: ShellTerminal }
	| { key: string; kind: "workspace"; tab: CenterPaneWorkspaceTab };

const terminalFontSizeStorageKey = "ao.terminal.fontSize";
const WHEEL_ZOOM_THRESHOLD = 80;
const WHEEL_ZOOM_RESET_MS = 250;
const isMac = isMacPlatform();
const isLinux = isLinuxPlatform();

function DraggableWorkspaceTab({ children, value }: { children: ReactNode; value: string }) {
	const dragControls = useDragControls();
	const startDrag = (event: PointerEvent<HTMLDivElement>) => {
		if ((event.target as HTMLElement).closest("[data-terminal-tab-action],input,a")) return;
		dragControls.start(event);
	};

	return (
		<Reorder.Item
			as="div"
			className="flex shrink-0 self-stretch touch-pan-y"
			data-terminal-tab-key={value}
			drag="x"
			dragControls={dragControls}
			dragListener={false}
			onPointerDown={startDrag}
			value={value}
		>
			{children}
		</Reorder.Item>
	);
}

function clampTerminalFontSize(size: number): number {
	return Math.min(TERMINAL_FONT_SIZE_MAX, Math.max(TERMINAL_FONT_SIZE_MIN, size));
}

function initialTerminalFontSize(): number {
	if (typeof window === "undefined") return TERMINAL_FONT_SIZE_DEFAULT;
	const raw = window.localStorage?.getItem(terminalFontSizeStorageKey);
	const parsed = raw === null ? Number.NaN : Number(raw);
	if (!Number.isFinite(parsed)) return TERMINAL_FONT_SIZE_DEFAULT;
	return clampTerminalFontSize(parsed);
}

export function CenterPane({
	session,
	theme,
	daemonReady,
	terminalTarget,
	reviewerTerminal,
	onSelectReviewerTerminal,
	shellTerminals = [],
	onSelectSessionTerminal,
	onSelectShellTerminal,
	onCloseShellTerminal,
	onRenameShellTerminal,
	topbarActions,
	sessionTabAction,
	sessionTabActionWide = false,
	tabStripAction,
	handoffDialogOpen = false,
	workspaceTabs,
	workspaceTabActions,
	workspaceActiveTabKey,
	auxiliaryTabOrder,
	onAuxiliaryTabOrderChange,
	agentInputDisabled = false,
}: CenterPaneProps) {
	const { t } = useTranslation();
	const paneRef = useRef<HTMLDivElement | null>(null);
	const wheelZoomRemainderRef = useRef(0);
	const lastWheelZoomAtRef = useRef(0);
	const [fontSize, setFontSize] = useState(initialTerminalFontSize);
	const [isFullscreen, setIsFullscreen] = useState(false);
	const [terminalBounds, setTerminalBounds] = useState({ leftInset: 0, rightInset: 0, width: 0 });
	const [tabOrderBySession, setTabOrderBySession] = useState<Record<string, string[]>>({});
	const queryClient = useQueryClient();
	const refreshWorkspaces = useCallback(
		() => queryClient.invalidateQueries({ queryKey: workspaceQueryKey }),
		[queryClient],
	);
	const isSidebarOpen = useUiStore(sidebarOccupiesLayout);
	const sessionId = session?.id;
	const auxiliaryTabs = useMemo<AuxiliaryTab[]>(
		() => [
			...(reviewerTerminal
				? [
						{
							key: `reviewer:${reviewerTerminal.handleId}`,
							kind: "reviewer" as const,
							terminal: reviewerTerminal,
						},
					]
				: []),
			...shellTerminals.map((terminal) => ({ key: terminal.handleId, kind: "shell" as const, terminal })),
			...(workspaceTabs ?? []).map((tab) => ({ key: tab.key, kind: "workspace" as const, tab })),
		],
		[reviewerTerminal, shellTerminals, workspaceTabs],
	);
	const availableAuxiliaryKeys = useMemo(() => auxiliaryTabs.map((tab) => tab.key), [auxiliaryTabs]);
	const orderedAuxiliaryTabs = useMemo(() => {
		const preferred = auxiliaryTabOrder ?? (sessionId ? (tabOrderBySession[sessionId] ?? []) : []);
		const byKey = new Map(auxiliaryTabs.map((tab) => [tab.key, tab]));
		const ordered = preferred.flatMap((key) => {
			const tab = byKey.get(key);
			if (!tab) return [];
			byKey.delete(key);
			return [tab];
		});
		return [...ordered, ...byKey.values()];
	}, [auxiliaryTabOrder, auxiliaryTabs, sessionId, tabOrderBySession]);
	const tabOverflowWatch = `${sessionId ?? ""}|${availableAuxiliaryKeys.join("|")}`;
	const {
		scrollRef: tabsOverflowRef,
		scrollToEnd: scrollTabsToEnd,
		showLeftFade,
		showRightFade,
	} = useTabScrollEdges([tabOverflowWatch]);
	const previousTabCountRef = useRef(availableAuxiliaryKeys.length);
	const target = terminalTarget ?? { kind: "worker" };
	const activeWorkspaceTab = workspaceActiveTabKey
		? workspaceTabs?.find((tab) => tab.key === workspaceActiveTabKey)
		: undefined;
	const workerInputDisabled =
		target.kind === "worker" && (agentInputDisabled || handoffDialogOpen);
	const sessionTabLabel = session
		? isOrchestratorSession(session)
			? t("shell.orchestrator")
			: session.title
		: t("terminal.noSession");
	const activeTerminalLabel =
		target.kind === "shell"
			? (shellTerminals.find((shell) => shell.handleId === target.handleId)?.title ?? target.title)
			: target.kind === "reviewer"
				? `${t("terminal.reviewer")} · ${target.harness}`
				: (session?.title ?? sessionTabLabel);
	const reorderAuxiliaryTabs = useCallback(
		(nextKeys: string[]) => {
			if (!sessionId) return;
			const available = new Set(availableAuxiliaryKeys);
			const next = nextKeys.filter((key, index) => available.has(key) && nextKeys.indexOf(key) === index);
			for (const key of availableAuxiliaryKeys) {
				if (!next.includes(key)) next.push(key);
			}
			if (onAuxiliaryTabOrderChange) onAuxiliaryTabOrderChange(next);
			else setTabOrderBySession((current) => ({ ...current, [sessionId]: next }));
		},
		[availableAuxiliaryKeys, onAuxiliaryTabOrderChange, sessionId],
	);
	const selectAdjacentTab = useCallback(
		(direction: -1 | 1) => {
			const activeKey =
				workspaceActiveTabKey ?? (target.kind === "shell"
					? target.handleId
					: target.kind === "reviewer"
						? `reviewer:${target.handleId}`
						: "worker");
			const tabKeys = ["worker", ...orderedAuxiliaryTabs.map((tab) => tab.key)];
			const activeIndex = Math.max(0, tabKeys.indexOf(activeKey));
			const nextIndex = (activeIndex + direction + tabKeys.length) % tabKeys.length;
			if (nextIndex === 0) {
				onSelectSessionTerminal?.();
				return;
			}
			const nextTab = orderedAuxiliaryTabs[nextIndex - 1];
			if (nextTab?.kind === "reviewer") onSelectReviewerTerminal?.(nextTab.terminal);
			if (nextTab?.kind === "shell") onSelectShellTerminal?.(nextTab.terminal.handleId);
			if (nextTab?.kind === "workspace") nextTab.tab.onSelect();
		},
		[
			onSelectReviewerTerminal,
			onSelectSessionTerminal,
			onSelectShellTerminal,
			orderedAuxiliaryTabs,
			target,
			workspaceActiveTabKey,
		],
	);

	useEffect(() => {
		if (!sessionId) return;
		if (auxiliaryTabOrder) {
			const available = new Set(availableAuxiliaryKeys);
			const keys = auxiliaryTabOrder.filter((key) => available.has(key));
			for (const key of availableAuxiliaryKeys) {
				if (!keys.includes(key)) keys.push(key);
			}
			if (!keys.every((key, index) => key === auxiliaryTabOrder[index]) || keys.length !== auxiliaryTabOrder.length) {
				onAuxiliaryTabOrderChange?.(keys);
			}
			return;
		}
		setTabOrderBySession((current) => {
			const currentOrder = current[sessionId] ?? [];
			const available = new Set(availableAuxiliaryKeys);
			const keys = currentOrder.filter((key) => available.has(key));
			for (const key of availableAuxiliaryKeys) {
				if (!keys.includes(key)) keys.push(key);
			}
			if (keys.length === currentOrder.length && keys.every((key, index) => key === currentOrder[index])) return current;
			if (keys.length === 0) {
				const { [sessionId]: _removed, ...rest } = current;
				return rest;
			}
			return { ...current, [sessionId]: keys };
		});
	}, [auxiliaryTabOrder, availableAuxiliaryKeys, onAuxiliaryTabOrderChange, sessionId]);

	useEffect(() => {
		const handleFullscreenChange = () => setIsFullscreen(document.fullscreenElement === paneRef.current);
		document.addEventListener("fullscreenchange", handleFullscreenChange);
		return () => document.removeEventListener("fullscreenchange", handleFullscreenChange);
	}, []);

	useEffect(
		() =>
			aoBridge.app.onCloseShellTerminalShortcut(() => {
				if (activeWorkspaceTab?.onClose) activeWorkspaceTab.onClose();
				else if (target.kind === "shell") onCloseShellTerminal?.(target.handleId);
			}),
		[activeWorkspaceTab, target, onCloseShellTerminal],
	);

	useEffect(() => {
		const disposePrevious = aoBridge.app.onPreviousTabShortcut(() => selectAdjacentTab(-1));
		const disposeNext = aoBridge.app.onNextTabShortcut(() => selectAdjacentTab(1));
		return () => {
			disposePrevious();
			disposeNext();
		};
	}, [selectAdjacentTab]);

	useEffect(() => {
		aoBridge.app.setCloseShellTerminalShortcutEnabled(
			Boolean(activeWorkspaceTab?.onClose) || (target.kind === "shell" && Boolean(onCloseShellTerminal)),
		);
		return () => aoBridge.app.setCloseShellTerminalShortcutEnabled(false);
	}, [activeWorkspaceTab, target.kind, onCloseShellTerminal]);

	useEffect(() => {
		const element = tabsOverflowRef.current;
		if (!element) return;
		const handleWheel = (event: globalThis.WheelEvent) => {
			if (event.ctrlKey || event.metaKey || Math.abs(event.deltaX) >= Math.abs(event.deltaY)) return;
			if (event.deltaY === 0 || element.scrollWidth <= element.clientWidth) return;
			event.preventDefault();
			element.scrollBy({ left: event.deltaY });
		};
		element.addEventListener("wheel", handleWheel, { passive: false });
		return () => element.removeEventListener("wheel", handleWheel);
	}, [isFullscreen, tabOverflowWatch]);

	useEffect(() => {
		if (availableAuxiliaryKeys.length > previousTabCountRef.current) scrollTabsToEnd();
		previousTabCountRef.current = availableAuxiliaryKeys.length;
	}, [availableAuxiliaryKeys.length, scrollTabsToEnd]);

	useEffect(() => {
		const activeKey =
			workspaceActiveTabKey ?? (target.kind === "shell"
				? target.handleId
				: target.kind === "reviewer"
					? `reviewer:${target.handleId}`
					: undefined);
		if (!activeKey) return;
		const scrollRegion = tabsOverflowRef.current;
		if (!scrollRegion) return;
		const activeTab = Array.from(
			scrollRegion.querySelectorAll<HTMLElement>("[data-terminal-tab-key]"),
		).find((element) => element.dataset.terminalTabKey === activeKey);
		if (!activeTab) return;
		const scrollRect = scrollRegion.getBoundingClientRect();
		const tabRect = activeTab.getBoundingClientRect();
		let nextScrollLeft = scrollRegion.scrollLeft;
		if (tabRect.left < scrollRect.left) nextScrollLeft -= scrollRect.left - tabRect.left;
		if (tabRect.right > scrollRect.right) nextScrollLeft += tabRect.right - scrollRect.right;
		if (nextScrollLeft === scrollRegion.scrollLeft) return;
		scrollRegion.scrollTo({ behavior: "smooth", left: Math.max(0, nextScrollLeft) });
	}, [orderedAuxiliaryTabs, target, workspaceActiveTabKey]);

	useEffect(() => {
		const pane = paneRef.current;
		if (!pane) return;
		const workspaceSurface = pane.closest<HTMLElement>(".center-panel-surface");
		const measure = () => {
			const paneRect = pane.getBoundingClientRect();
			// leftInset/rightInset are kept for the terminal region width calculation
			// but no longer used for viewport-alignment padding (topbar is inside the surface).
			const workspaceRect = workspaceSurface?.getBoundingClientRect() ?? paneRect;
			const next = {
				leftInset: workspaceRect.left,
				rightInset: Math.max(0, window.innerWidth - workspaceRect.right),
				width: paneRect.width,
			};
			setTerminalBounds((current) =>
				current.leftInset === next.leftInset && current.rightInset === next.rightInset && current.width === next.width
					? current
					: next,
			);
		};
		measure();
		const observer = new ResizeObserver(measure);
		observer.observe(pane);
		if (workspaceSurface) observer.observe(workspaceSurface);
		return () => observer.disconnect();
	}, []);

	const updateFontSize = useCallback((delta: number) => {
		setFontSize((current) => {
			const next = clampTerminalFontSize(current + delta);
			window.localStorage?.setItem(terminalFontSizeStorageKey, String(next));
			return next;
		});
	}, []);

	const toggleFullscreen = useCallback(async () => {
		const pane = paneRef.current;
		if (!pane) return;
		try {
			if (document.fullscreenElement === pane) {
				await document.exitFullscreen();
				return;
			}
			await pane.requestFullscreen();
		} catch (error) {
			console.warn("Unable to toggle terminal fullscreen", error);
		}
	}, []);

	const handleWheelZoom = useCallback(
		(event: ReactWheelEvent<HTMLDivElement>) => {
			if (!event.ctrlKey && !event.metaKey) return;
			event.preventDefault();
			event.stopPropagation();

			if (event.timeStamp - lastWheelZoomAtRef.current > WHEEL_ZOOM_RESET_MS) {
				wheelZoomRemainderRef.current = 0;
			}
			lastWheelZoomAtRef.current = event.timeStamp;
			wheelZoomRemainderRef.current += event.deltaY;

			const steps = Math.floor(Math.abs(wheelZoomRemainderRef.current) / WHEEL_ZOOM_THRESHOLD);
			if (steps === 0) return;

			const direction = wheelZoomRemainderRef.current > 0 ? -1 : 1;
			updateFontSize(direction * steps);
			wheelZoomRemainderRef.current -= Math.sign(wheelZoomRemainderRef.current) * steps * WHEEL_ZOOM_THRESHOLD;
		},
		[updateFontSize],
	);

	const terminalTopbar = (
		<div className="flex h-inspector-tabs w-full shrink-0 items-stretch bg-sidebar">

			<div className="session-topbar-surface flex min-w-0 flex-1" data-testid="session-workspace-topbar">
				<div
					className={cn(
						"flex min-w-0 shrink items-stretch",
						!isFullscreen && !isSidebarOpen && isMac && "session-topbar-titlebar-clearance-mac",
						!isFullscreen && !isSidebarOpen && isLinux && "session-topbar-titlebar-clearance-linux",
					)}
					data-testid="session-terminal-region"
					style={{
						width: terminalBounds.width > 0 ? terminalBounds.width : "100%",
					}}
				>
					<div
							aria-label={t("terminal.tabsAria")}
							className="flex h-full min-w-0 flex-1 items-stretch"
							onKeyDown={handleTerminalTabListKeyDown}
							role="tablist"
						>
							<div className="relative min-w-0 flex-1 self-stretch overflow-hidden">
								<div
									ref={tabsOverflowRef}
									className="session-tab-scroll-region scrollbar-none flex h-full min-w-flex-min min-w-0 items-stretch overflow-x-auto"
								>
								<div className="flex w-max items-stretch">
									{/* The owning session scrolls with its auxiliary terminals, but remains fixed in order. */}
									{session ? (
										<SessionPaneTab
											isActive={target.kind === "worker" && !workspaceActiveTabKey}
											label={sessionTabLabel}
											onSelect={onSelectSessionTerminal}
											onRenamed={refreshWorkspaces}
											session={session}
											tabAction={sessionTabAction}
											tabActionWide={sessionTabActionWide}
										/>
									) : (
										<SessionPaneTab isActive={target.kind === "worker"} label={sessionTabLabel} />
									)}
									<Reorder.Group
										as="div"
										axis="x"
										className="flex items-stretch self-stretch"
										onReorder={reorderAuxiliaryTabs}
										values={orderedAuxiliaryTabs.map((tab) => tab.key)}
									>
										{orderedAuxiliaryTabs.map((tab) => (
											<DraggableWorkspaceTab key={tab.key} value={tab.key}>
												{tab.kind === "reviewer" ? (
													<SessionPaneTab
														appearance="connected"
														icon={
															<AgentAvatar
																provider={tab.terminal.harness}
																className="size-terminal-agent-icon"
																decorative
															/>
														}
														isActive={target.kind === "reviewer" && !workspaceActiveTabKey}
														label={t("terminal.reviewer")}
														onSelect={() => onSelectReviewerTerminal?.(tab.terminal)}
														title={tab.terminal.harness}
													/>
												) : tab.kind === "shell" ? (
													<ShellTerminalTab
														appearance="connected"
														isActive={target.kind === "shell" && target.handleId === tab.terminal.handleId && !workspaceActiveTabKey}
														onClose={() => onCloseShellTerminal?.(tab.terminal.handleId)}
														onRename={
															onRenameShellTerminal
																? (title) => onRenameShellTerminal(tab.terminal.handleId, title)
																: undefined
														}
														onSelect={() => onSelectShellTerminal?.(tab.terminal.handleId)}
														shell={tab.terminal}
													/>
												) : (
													tab.tab.content
												)}
											</DraggableWorkspaceTab>
										))}
									</Reorder.Group>
									{workspaceTabActions}
								</div>
							</div>
								{showLeftFade ? <div aria-hidden="true" className="session-tab-scroll-fade session-tab-scroll-fade--left" /> : null}
								{showRightFade ? <div aria-hidden="true" className="session-tab-scroll-fade" /> : null}
							</div>
					</div>
				</div>
				{isFullscreen ? null : (
					<div
						className="ml-auto flex shrink-0 items-center gap-1 pl-2 pr-3"
			data-testid="session-action-region"
		>
			{tabStripAction ? <div data-testid="session-tab-strip-action">{tabStripAction}</div> : null}
			{topbarActions}
					</div>
				)}
			</div>
		</div>
	);

	return (
		<div
			ref={paneRef}
			className="terminal-pane-frame flex h-full min-h-0 min-w-flex-min flex-col"
			onWheelCapture={handleWheelZoom}
		>
			{isFullscreen ? terminalTopbar : <SessionTopbarPortal>{terminalTopbar}</SessionTopbarPortal>}
			<div
				aria-label={t("terminal.panelAria", { title: activeTerminalLabel })}
				className="relative min-h-0 flex-1"
				role="tabpanel"
			>
				<div
					className="h-full min-h-0"
					data-testid="terminal-interaction-surface"
					inert={workerInputDisabled ? true : undefined}
				>
					<TerminalPane
						daemonReady={daemonReady}
						fontSize={fontSize}
						// A terminal you can type into should already hold the caret when you
						// open or switch to the session, the same way the chat composer does.
						// Worker input is off during an interface transition; every other
						// target is interactive as soon as it is on screen.
						focusRequested={target.kind !== "worker" || !workerInputDisabled}
						isFullscreen={isFullscreen}
						inputDisabled={workerInputDisabled}
						onChangeFontSize={updateFontSize}
						onToggleFullscreen={toggleFullscreen}
						session={session}
						terminalTarget={target}
						theme={theme}
					/>
				</div>
			</div>
		</div>
	);
}

type SessionPaneTabProps = {
	label: string;
	isActive: boolean;
	appearance?: "primary" | "connected";
	onSelect?: () => void;
	onRenamed?: () => void | Promise<void>;
	session?: WorkspaceSession;
	icon?: ReactNode;
	title?: string;
	/** Session-scoped controls (interface switch, handoff) beside the tab label. */
	tabAction?: ReactNode;
	/** Only while switching: reserve action width so the spinner does not cover the title. */
	tabActionWide?: boolean;
};

// Shared tab chrome: the open tab is highlighted with the same rounded
// background as the inspector rail tabs (Summary · Reviews · Browser), and
// the full label only becomes the hover tooltip when the tab strip is
// crowded enough to truncate it.
export function SessionPaneTab({
	label,
	isActive,
	appearance = "primary",
	onSelect,
	onRenamed,
	session,
	icon,
	title,
	tabAction,
	tabActionWide = false,
}: SessionPaneTabProps) {
	const { t } = useTranslation();
	const { ref, isTruncated } = useTruncatedText<HTMLButtonElement>(label);
	const activityLabel = session ? getAgentActivityView(session.activity, t).label : undefined;
	const providerLabel = session ? agentLabel(session.provider) : undefined;
	const tabIcon = session ? <AgentAvatar className="size-terminal-agent-icon" decorative provider={session.provider} /> : icon;
	const connected = appearance === "connected";
	// A session object supplies the tab presentation; refresh wiring explicitly
	// opts the owning surface into rename so shared preview/cloud tabs cannot
	// persist a title without updating their query cache.
	const renameSession = onRenamed ? session : undefined;
	const rename = useSessionRename(renameSession, onRenamed);
	const editingContent = renameSession && rename.isEditing ? (
		<div className="flex h-full min-w-0 flex-1 items-center gap-2 px-2">
			{tabIcon}
			<input
				aria-label={t("shell.renameSession", { title: renameSession.title })}
				autoFocus
				className="min-w-0 flex-1 rounded-xs border border-accent bg-background px-1 text-control text-foreground outline-none ring-1 ring-accent"
				maxLength={MAX_SESSION_DISPLAY_NAME_LEN}
				onBlur={() => void rename.commit()}
				onChange={(event) => rename.setDraft(event.target.value)}
				onFocus={(event) => event.currentTarget.select()}
				onKeyDown={(event) => {
					if (event.key === "Enter") {
						event.preventDefault();
						event.currentTarget.blur();
					} else if (event.key === "Escape") {
						event.preventDefault();
						rename.cancel();
					}
				}}
				value={rename.draft}
			/>
		</div>
	) : undefined;
	const tabFrame = (
		<TerminalTabFrame
			action={!rename.isEditing && tabAction ? <span data-testid="session-tab-action">{tabAction}</span> : undefined}
			// Idle tabs keep the overlay ⋮ slot (title looks like before). Only while
			// switching do we reserve width so the spinner cannot cover the label.
			actionLayout={tabActionWide ? "inline" : "overlay"}
			active={isActive}
			buttonProps={{
				"aria-current": isActive,
				"aria-keyshortcuts": renameSession ? "F2" : undefined,
				"aria-label": [label, providerLabel, activityLabel].filter(Boolean).join(" · "),
				"aria-selected": isActive,
				onClick: (event) => {
					if (event.detail > 1) return;
					onSelect?.();
				},
				onDoubleClick: renameSession
					? (event) => {
							event.preventDefault();
							rename.begin();
						}
					: undefined,
				onKeyDown: renameSession
					? (event) => {
							if (event.key !== "F2") return;
							event.preventDefault();
							rename.begin();
						}
					: undefined,
				role: "tab",
				tabIndex: isActive ? 0 : -1,
				title: title ?? (isTruncated ? label : t("terminal.sessionAria")),
				type: "button",
			}}
			buttonRef={ref}
			className="w-shell-tab-connected min-w-shell-tab-min"
			data-terminal-role={connected ? undefined : "primary"}
			editingContent={editingContent}
		>
			{tabIcon}
			<span className="truncate">{label}</span>
		</TerminalTabFrame>
	);
	if (!renameSession || rename.isEditing) return tabFrame;
	return (
		<ContextMenu>
			<ContextMenuTrigger asChild>
				<span className="contents">{tabFrame}</span>
			</ContextMenuTrigger>
			<ContextMenuContent className="min-w-44">
				<ContextMenuItem aria-label={t("shell.renameSession", { title: renameSession.title })} onSelect={rename.begin}>
					<Pencil aria-hidden="true" />
					{t("shell.rename")}
				</ContextMenuItem>
			</ContextMenuContent>
		</ContextMenu>
	);
}
