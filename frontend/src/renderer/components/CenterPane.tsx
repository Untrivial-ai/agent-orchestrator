import { ArrowRight, ChevronLeft, ChevronRight, Maximize2, Minimize2, Minus, Plus } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type ReactNode, type WheelEvent } from "react";
import { useTranslation } from "react-i18next";
import { defaultShortcutBindings, shortcutBindingLabel } from "../../shared/shortcuts";
import { useOverflowScroll } from "../hooks/useOverflowScroll";
import { findActiveAgentSwitch, useAgentSwitches } from "../hooks/useAgentSwitches";
import { useSwitchAgentState } from "../hooks/useSwitchAgent";
import { useTruncatedText } from "../hooks/useTruncatedText";
import type { ShellTerminal } from "../hooks/useShellTerminals";
import { TERMINAL_FONT_SIZE_DEFAULT, TERMINAL_FONT_SIZE_MAX, TERMINAL_FONT_SIZE_MIN } from "../lib/design-tokens";
import { getAgentActivityView } from "../lib/session-presentation";
import { agentLabel } from "../lib/agent-options";
import { isLinuxPlatform, isMacPlatform } from "../lib/platform";
import { aoBridge } from "../lib/bridge";
import { handleTerminalTabListKeyDown } from "../lib/terminal-tabs";
import { cn } from "../lib/utils";
import { useUiStore, type Theme } from "../stores/ui-store";
import type { TerminalTarget } from "../types/terminal";
import { isOrchestratorSession, type WorkspaceSession } from "../types/workspace";
import { AgentAvatar } from "./AgentAvatar";
import { ShellTerminalTab } from "./ShellTerminalTab";
import { TerminalPane } from "./TerminalPane";
import { SessionTopbarPortal } from "./SessionTopbarPortal";
import { TerminalSwitchAgentButton } from "./TerminalSwitchAgentButton";
import { Button } from "./ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

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
	/** Opens a new shell tab in this session's worktree (the button at the end of the tab bar). */
	onNewShellTerminal?: () => void;
	/** Session actions consolidated into the terminal bar by SessionView. */
	topbarActions?: ReactNode;
	/** Stop forwarding the agent pane's keystrokes while its controller drains. */
	agentInputDisabled?: boolean;
};

const terminalFontSizeStorageKey = "ao.terminal.fontSize";
const WHEEL_ZOOM_THRESHOLD = 80;
const WHEEL_ZOOM_RESET_MS = 250;
const isMac = isMacPlatform();
const isLinux = isLinuxPlatform();
const newTerminalShortcutLabel = shortcutBindingLabel(defaultShortcutBindings("new-shell-terminal", isMac)[0], isMac);

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
	onNewShellTerminal,
	topbarActions,
	agentInputDisabled = false,
}: CenterPaneProps) {
	const { t } = useTranslation();
	const paneRef = useRef<HTMLDivElement | null>(null);
	const wheelZoomRemainderRef = useRef(0);
	const lastWheelZoomAtRef = useRef(0);
	const [fontSize, setFontSize] = useState(initialTerminalFontSize);
	const [isFullscreen, setIsFullscreen] = useState(false);
	const [terminalBounds, setTerminalBounds] = useState({ leftInset: 0, rightInset: 0, width: 0 });
	const isSidebarOpen = useUiStore((state) => state.isSidebarOpen);
	const tabOverflowWatch = `${session?.id ?? ""}|${shellTerminals.map((terminal) => terminal.handleId).join("|")}`;
	const tabsOverflow = useOverflowScroll<HTMLDivElement>(tabOverflowWatch);
	const agentSwitchesQuery = useAgentSwitches(session?.id ?? "");
	const agentSwitches = agentSwitchesQuery.data ?? [];
	const activeAgentSwitch = findActiveAgentSwitch(agentSwitches);
	const switchMutation = useSwitchAgentState(session?.id ?? "");
	const switchSource = activeAgentSwitch?.fromHarness ?? switchMutation.input?.session.provider;
	const switchTarget = activeAgentSwitch?.targetHarness ?? switchMutation.input?.targetHarness;
	const isSwitchingAgent = Boolean(
		(activeAgentSwitch || switchMutation.isPending) && switchSource && switchTarget,
	);
	const switchPermissionRequired = Boolean(
		activeAgentSwitch?.state === "preparing_handoff" &&
			activeAgentSwitch.agentHandoffStatus === "requested" &&
			(session?.activity?.state === "blocked" || session?.activity?.state === "waiting_input"),
	);
	const target = terminalTarget ?? { kind: "worker" };
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
				: sessionTabLabel;
	const selectAdjacentTab = useCallback(
		(direction: -1 | 1) => {
			const activeIndex =
				target.kind === "shell"
					? shellTerminals.findIndex((shell) => shell.handleId === target.handleId) + 1
					: 0;
			const nextIndex = (activeIndex + direction + shellTerminals.length + 1) % (shellTerminals.length + 1);
			if (nextIndex === 0) {
				onSelectSessionTerminal?.();
				return;
			}
			const nextShell = shellTerminals[nextIndex - 1];
			if (nextShell) onSelectShellTerminal?.(nextShell.handleId);
		},
		[onSelectSessionTerminal, onSelectShellTerminal, shellTerminals, target],
	);

	useEffect(() => {
		if (!switchMutation.isPending || activeAgentSwitch) return;
		void agentSwitchesQuery.refetch();
		const timer = window.setInterval(() => void agentSwitchesQuery.refetch(), 500);
		return () => window.clearInterval(timer);
	}, [activeAgentSwitch, agentSwitchesQuery.refetch, switchMutation.isPending]);

	useEffect(() => {
		const handleFullscreenChange = () => setIsFullscreen(document.fullscreenElement === paneRef.current);
		document.addEventListener("fullscreenchange", handleFullscreenChange);
		return () => document.removeEventListener("fullscreenchange", handleFullscreenChange);
	}, []);

	useEffect(
		() =>
			aoBridge.app.onCloseShellTerminalShortcut(() => {
				if (target.kind === "shell") onCloseShellTerminal?.(target.handleId);
			}),
		[target, onCloseShellTerminal],
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
			target.kind === "shell" && Boolean(onCloseShellTerminal),
		);
		return () => aoBridge.app.setCloseShellTerminalShortcutEnabled(false);
	}, [target.kind, onCloseShellTerminal]);

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
		(event: WheelEvent<HTMLDivElement>) => {
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
						"flex min-w-0 shrink items-center pr-1.5",
						!isFullscreen && !isSidebarOpen && isMac && "session-topbar-titlebar-clearance-mac",
						!isFullscreen && !isSidebarOpen && isLinux && "session-topbar-titlebar-clearance-linux",
					)}
					data-testid="session-terminal-region"
					style={{
						width: terminalBounds.width > 0 ? terminalBounds.width : "100%",
					}}
				>
					<div className="flex h-full min-w-flex-min flex-1 items-center">
						{tabsOverflow.canScrollLeft ? (
							<button
								aria-label={t("terminal.scrollTabsLeft")}
								className="inline-flex size-control-sm shrink-0 items-center justify-center rounded-sm text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50"
								onClick={() => tabsOverflow.scrollByDirection(-1)}
								title={t("terminal.scrollTabsLeft")}
								type="button"
							>
								<ChevronLeft aria-hidden="true" className="size-icon-md" />
							</button>
						) : null}
						{/* The permanent agent tab plus shells opened in this session's worktree. */}
						<div
							ref={tabsOverflow.ref}
							aria-label={t("terminal.tabsAria")}
							className="scrollbar-none flex min-w-flex-min flex-1 self-stretch items-center overflow-x-auto"
							onKeyDown={handleTerminalTabListKeyDown}
							role="tablist"
						>
							{session ? (
								<SessionPaneTab
									isActive={target.kind === "worker"}
									label={sessionTabLabel}
									onSelect={onSelectSessionTerminal}
									session={session}
								/>
							) : (
								<SessionPaneTab isActive={target.kind === "worker"} label={sessionTabLabel} />
							)}
							{reviewerTerminal ? (
								<SessionPaneTab
									icon={<AgentAvatar provider={reviewerTerminal.harness} className="size-icon-base" decorative />}
									isActive={target.kind === "reviewer"}
									label={t("terminal.reviewer")}
									onSelect={() => onSelectReviewerTerminal?.(reviewerTerminal)}
									title={reviewerTerminal.harness}
								/>
							) : null}
							{shellTerminals.map((shell) => (
								<ShellTerminalTab
									key={shell.handleId}
									appearance="connected"
									isActive={target.kind === "shell" && target.handleId === shell.handleId}
									onClose={() => onCloseShellTerminal?.(shell.handleId)}
									onRename={onRenameShellTerminal ? (title) => onRenameShellTerminal(shell.handleId, title) : undefined}
									onSelect={() => onSelectShellTerminal?.(shell.handleId)}
									shell={shell}
								/>
							))}
						</div>
						{tabsOverflow.canScrollRight ? (
							<button
								aria-label={t("terminal.scrollTabsRight")}
								className="inline-flex size-control-sm shrink-0 items-center justify-center rounded-sm text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50"
								onClick={() => tabsOverflow.scrollByDirection(1)}
								title={t("terminal.scrollTabsRight")}
								type="button"
							>
								<ChevronRight aria-hidden="true" className="size-icon-md" />
							</button>
						) : null}
						{!session || !isOrchestratorSession(session) ? (
							<Tooltip>
								<TooltipTrigger asChild>
									<Button
										aria-label={t("shortcut.new-shell-terminal")}
										className="shrink-0 text-muted-foreground"
										disabled={!onNewShellTerminal}
										onClick={onNewShellTerminal}
										size="icon-sm"
										type="button"
										variant="outline"
									>
										<Plus aria-hidden="true" className="size-icon-md" />
									</Button>
								</TooltipTrigger>
								<TooltipContent>{t("terminal.newWithShortcut", { shortcut: newTerminalShortcutLabel })}</TooltipContent>
							</Tooltip>
						) : null}
					</div>
					<div
						aria-label={t("terminal.controlsAria")}
						className="ml-1.5 flex shrink-0 items-center gap-0.5 border-l border-border/70 pl-1.5"
						role="toolbar"
					>
						<TerminalControl
							disabled={fontSize <= TERMINAL_FONT_SIZE_MIN}
							label={t("terminal.decreaseFontSize")}
							onClick={() => updateFontSize(-1)}
						>
							<Minus aria-hidden="true" className="size-icon-sm" />
						</TerminalControl>
						<span
							aria-label={t("terminal.fontSizeAria", { size: fontSize })}
							className="w-font-size-label text-center font-mono text-micro tabular-nums text-muted-foreground"
						>
							{fontSize}px
						</span>
						<TerminalControl
							disabled={fontSize >= TERMINAL_FONT_SIZE_MAX}
							label={t("terminal.increaseFontSize")}
							onClick={() => updateFontSize(1)}
						>
							<Plus aria-hidden="true" className="size-icon-sm" />
						</TerminalControl>
						<div aria-hidden="true" className="mx-1 h-4 w-px bg-border/70" />
						<TerminalControl
							isPressed={isFullscreen}
							label={isFullscreen ? t("terminal.exitFullscreen") : t("terminal.fullscreen")}
							onClick={() => void toggleFullscreen()}
						>
							{isFullscreen ? (
								<Minimize2 aria-hidden="true" className="size-icon-md" />
							) : (
								<Maximize2 aria-hidden="true" className="size-icon-md" />
							)}
						</TerminalControl>
					</div>
				</div>
				{isFullscreen ? null : (
					<div
						className="ml-auto flex shrink-0 items-center px-3"
						data-testid="session-action-region"
					>
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
					inert={isSwitchingAgent && !switchPermissionRequired ? true : undefined}
				>
					<TerminalPane
						daemonReady={daemonReady}
						fontSize={fontSize}
						focusRequested={switchPermissionRequired && target.kind === "worker"}
						inputDisabled={agentInputDisabled && target.kind === "worker"}
						session={session}
						terminalTarget={target}
						theme={theme}
					/>
				</div>
				{isSwitchingAgent && switchSource && switchTarget ? (
					<AgentSwitchTerminalOverlay
						permissionRequired={switchPermissionRequired}
						source={switchSource}
						target={switchTarget}
					/>
				) : null}
			</div>
		</div>
	);
}

type AgentSwitchTerminalOverlayProps = {
	permissionRequired: boolean;
	source: string;
	target: string;
};

function AgentSwitchTerminalOverlay({
	permissionRequired,
	source,
	target,
}: AgentSwitchTerminalOverlayProps) {
	const { t } = useTranslation();
	const overlayRef = useRef<HTMLDivElement | null>(null);
	const title = t("switchAgent.progressTitle", {
		source: agentLabel(source),
		target: agentLabel(target),
	});

	useEffect(() => {
		if (!permissionRequired) overlayRef.current?.focus({ preventScroll: true });
	}, [permissionRequired, source, target]);

	return (
		<div
			ref={overlayRef}
			aria-label={title}
			className={cn(
				"absolute inset-0 z-20 flex items-center justify-center",
				permissionRequired
					? "pointer-events-none bg-terminal/25"
					: "cursor-wait bg-terminal/95 backdrop-blur-[3px]",
			)}
			data-testid="agent-switch-terminal-overlay"
			tabIndex={-1}
		>
			<div
				aria-label={title}
				aria-live="polite"
				className={cn(
					"flex flex-col items-center gap-5 px-6 text-center",
					permissionRequired && "absolute inset-x-0 top-4 gap-2",
				)}
				role="status"
			>
				<div className="flex items-center gap-5 sm:gap-7">
					<SwitchingAgentMark harness={source} />
					<div aria-hidden="true" className="flex items-center gap-2 text-accent">
						<div className="relative h-1 w-20 overflow-hidden rounded-full bg-border-strong/70 sm:w-28">
							<span className="agent-switch-transfer-pulse absolute inset-y-0 w-10 rounded-full bg-gradient-to-r from-transparent via-accent to-transparent" />
						</div>
						<ArrowRight className="size-icon-lg shrink-0" />
					</div>
					<SwitchingAgentMark harness={target} />
				</div>
				<p className="font-mono text-control font-medium text-foreground">{title}</p>
				{permissionRequired ? (
					<p className="rounded-md border border-warning/40 bg-surface/95 px-3 py-2 text-caption text-foreground shadow-lg">
						{t("switchAgent.permissionRequired")}
					</p>
				) : null}
			</div>
		</div>
	);
}

function SwitchingAgentMark({ harness }: { harness: string }) {
	return (
		<div className="flex min-w-20 flex-col items-center gap-2">
			<span className="grid size-14 place-items-center rounded-xl border border-border-strong bg-surface/90 shadow-lg shadow-black/20">
				<AgentAvatar className="size-8" decorative provider={harness} />
			</span>
			<span className="text-caption font-medium text-muted-foreground">{agentLabel(harness)}</span>
		</div>
	);
}

type SessionPaneTabProps = {
	label: string;
	isActive: boolean;
	onSelect?: () => void;
	session?: WorkspaceSession;
	icon?: ReactNode;
	title?: string;
};

// Shared tab chrome: the open tab is highlighted with the same rounded
// background as the inspector rail tabs (Summary · Reviews · Browser), and
// the full label only becomes the hover tooltip when the tab strip is
// crowded enough to truncate it.
function SessionPaneTab({ label, isActive, onSelect, session, icon, title }: SessionPaneTabProps) {
	const { t } = useTranslation();
	const { ref, isTruncated } = useTruncatedText<HTMLButtonElement>(label);
	const activity = session ? getAgentActivityView(session.activity, t) : undefined;
	const tabIcon = session ? <AgentAvatar className="size-icon-base" decorative provider={session.provider} /> : icon;
	return (
		<span
			data-terminal-role="primary"
			className={cn(
				"group relative inline-flex min-w-shell-tab-min self-stretch items-center gap-1.5 border-r border-border bg-surface px-3 text-foreground transition-colors",
				isActive
					? "bg-overlay text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground/80"
					: "text-muted-foreground hover:bg-raised hover:text-foreground",
			)}
		>
			<button
				ref={ref}
				aria-current={isActive}
				aria-label={activity ? `${label} · ${activity.label}` : label}
				aria-selected={isActive}
				className={cn(
					"inline-flex min-w-flex-min max-w-shell-tab-max items-center gap-1.5 text-control font-medium leading-none transition-colors",
					isActive ? "text-foreground" : "text-passive group-hover:text-foreground",
				)}
				onClick={onSelect}
				role="tab"
				tabIndex={isActive ? 0 : -1}
				title={title ?? (isTruncated ? label : t("terminal.sessionAria"))}
				type="button"
			>
				{tabIcon}
				<span className="truncate">{label}</span>
				{activity ? (
					<span
						aria-hidden="true"
						className="inline-flex shrink-0 self-center items-center"
						style={{ color: activity.tone }}
						title={activity.label}
					>
						<span
							className={cn("size-1.5 rounded-full", activity.breathe && "animate-status-pulse")}
							style={{ background: activity.tone }}
						/>
					</span>
				) : null}
			</button>
			{session ? <TerminalSwitchAgentButton key={session.id} session={session} /> : null}
		</span>
	);
}

type TerminalControlProps = {
	children: ReactNode;
	disabled?: boolean;
	isPressed?: boolean;
	label: string;
	onClick: () => void;
};

function TerminalControl({ children, disabled, isPressed, label, onClick }: TerminalControlProps) {
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Button
					aria-label={label}
					aria-pressed={isPressed}
					className="size-control-sm p-0 text-passive"
					disabled={disabled}
					onClick={onClick}
					size="icon-sm"
					type="button"
					variant="ghost"
				>
					{children}
				</Button>
			</TooltipTrigger>
			<TooltipContent>{label}</TooltipContent>
		</Tooltip>
	);
}
