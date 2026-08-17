import {
	ArrowRight,
	CheckCircle2,
	TriangleAlert,
	X,
} from "lucide-react";
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
import { useTranslation } from "react-i18next";
import {
	findActiveAgentSwitch,
	findRecoveryRequiredAgentSwitch,
	isTerminalAgentSwitch,
	useAgentSwitches,
} from "../hooks/useAgentSwitches";
import { useSwitchAgentState } from "../hooks/useSwitchAgent";
import { useTruncatedText } from "../hooks/useTruncatedText";
import type { ShellTerminal } from "../hooks/useShellTerminals";
import { TERMINAL_FONT_SIZE_DEFAULT, TERMINAL_FONT_SIZE_MAX, TERMINAL_FONT_SIZE_MIN } from "../lib/design-tokens";
import { getAgentActivityView } from "../lib/session-presentation";
import {
	deriveAgentSwitchPresentation,
	type AgentSwitchPresentation,
} from "../lib/agent-switch-presentation";
import { agentLabel } from "../lib/agent-options";
import { isLinuxPlatform, isMacPlatform } from "../lib/platform";
import { aoBridge } from "../lib/bridge";
import { handleTerminalTabListKeyDown } from "../lib/terminal-tabs";
import { cn } from "../lib/utils";
import { useUiStore, type Theme } from "../stores/ui-store";
import type { TerminalTarget } from "../types/terminal";
import {
	isOrchestratorSession,
	type AgentSwitchSummary,
	type WorkspaceSession,
} from "../types/workspace";
import { AgentAvatar } from "./AgentAvatar";
import { ShellTerminalTab } from "./ShellTerminalTab";
import { TerminalPane } from "./TerminalPane";
import { SessionTopbarPortal } from "./SessionTopbarPortal";
import { TerminalSwitchAgentButton } from "./TerminalSwitchAgentButton";

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
	/** Session actions consolidated into the terminal bar by SessionView. */
	topbarActions?: ReactNode;
	/** Stop forwarding the agent pane's keystrokes while its controller drains. */
	agentInputDisabled?: boolean;
};

type AuxiliaryTerminal =
	| { key: string; kind: "reviewer"; terminal: NonNullable<CenterPaneProps["reviewerTerminal"]> }
	| { key: string; kind: "shell"; terminal: ShellTerminal };

type TerminalOrder = { sessionId: string; keys: string[] };

const terminalFontSizeStorageKey = "ao.terminal.fontSize";
const WHEEL_ZOOM_THRESHOLD = 80;
const WHEEL_ZOOM_RESET_MS = 250;
const isMac = isMacPlatform();
const isLinux = isLinuxPlatform();

function DraggableTerminalTab({ children, value }: { children: ReactNode; value: string }) {
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
	agentInputDisabled = false,
}: CenterPaneProps) {
	const { t } = useTranslation();
	const paneRef = useRef<HTMLDivElement | null>(null);
	const tabsOverflowRef = useRef<HTMLDivElement | null>(null);
	const wheelZoomRemainderRef = useRef(0);
	const lastWheelZoomAtRef = useRef(0);
	const [fontSize, setFontSize] = useState(initialTerminalFontSize);
	const [isFullscreen, setIsFullscreen] = useState(false);
	const [terminalBounds, setTerminalBounds] = useState({ leftInset: 0, rightInset: 0, width: 0 });
	const [switchSelectorOpen, setSwitchSelectorOpen] = useState(false);
	const [switchSelectorContainer, setSwitchSelectorContainer] = useState<HTMLDivElement | null>(null);
	const [terminalOrder, setTerminalOrder] = useState<TerminalOrder | null>(null);
	const isSidebarOpen = useUiStore((state) => state.isSidebarOpen);
	const sessionId = session?.id;
	const auxiliaryTerminals = useMemo<AuxiliaryTerminal[]>(
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
		],
		[reviewerTerminal, shellTerminals],
	);
	const availableAuxiliaryKeys = useMemo(() => auxiliaryTerminals.map((terminal) => terminal.key), [auxiliaryTerminals]);
	const orderedAuxiliaryTerminals = useMemo(() => {
		const preferred = terminalOrder && terminalOrder.sessionId === sessionId ? terminalOrder.keys : [];
		const byKey = new Map(auxiliaryTerminals.map((terminal) => [terminal.key, terminal]));
		const ordered = preferred.flatMap((key) => {
			const terminal = byKey.get(key);
			if (!terminal) return [];
			byKey.delete(key);
			return [terminal];
		});
		return [...ordered, ...byKey.values()];
	}, [auxiliaryTerminals, sessionId, terminalOrder]);
	const tabOverflowWatch = `${sessionId ?? ""}|${availableAuxiliaryKeys.join("|")}`;
	const agentSwitchesQuery = useAgentSwitches(session?.id ?? "");
	const agentSwitches = agentSwitchesQuery.data ?? [];
	const switchMutation = useSwitchAgentState(session?.id ?? "");
	const observedNonterminalSwitchIdsRef = useRef(new Set<string>());
	const mountedSessionIdRef = useRef(session?.id);
	const sourceFocusSwitchIdRef = useRef<string | undefined>(undefined);
	const announcedAlertKeysRef = useRef(new Set<string>());
	const [transientSuccessSwitchId, setTransientSuccessSwitchId] = useState<string>();
	const [dismissedFailureSwitchId, setDismissedFailureSwitchId] = useState<string>();
	const [alertAnnouncement, setAlertAnnouncement] = useState<{ key: string; text: string }>();
	if (mountedSessionIdRef.current !== session?.id) {
		mountedSessionIdRef.current = session?.id;
		observedNonterminalSwitchIdsRef.current = new Set();
		sourceFocusSwitchIdRef.current = undefined;
		announcedAlertKeysRef.current = new Set();
	}
	const sessionAgentSwitch = session?.activeAgentSwitch;
	const detailedSessionSwitch = sessionAgentSwitch
		? agentSwitches.find((entry) => entry.id === sessionAgentSwitch.id)
		: undefined;
	const activeHistorySwitch = findActiveAgentSwitch(agentSwitches);
	const recoveryHistorySwitch = findRecoveryRequiredAgentSwitch(agentSwitches);
	const latestCompletedSwitch = agentSwitches[0]?.state === "completed" ? agentSwitches[0] : undefined;
	if (sessionAgentSwitch && !isTerminalAgentSwitch(sessionAgentSwitch)) {
		observedNonterminalSwitchIdsRef.current.add(sessionAgentSwitch.id);
	}
	if (activeHistorySwitch) observedNonterminalSwitchIdsRef.current.add(activeHistorySwitch.id);
	const observedTerminalSwitch = agentSwitches.find(
		(entry) =>
			isTerminalAgentSwitch(entry) && observedNonterminalSwitchIdsRef.current.has(entry.id),
	);
	const currentAgentSwitch =
		detailedSessionSwitch ??
		sessionAgentSwitch ??
		recoveryHistorySwitch ??
		activeHistorySwitch;
	const admissionAgentSwitch: AgentSwitchSummary | undefined =
		!currentAgentSwitch && switchMutation.isPending && switchMutation.input
			? {
				agentHandoffStatus: "not_attempted",
				fromHarness: switchMutation.input.session.provider,
				id: `admission:${switchMutation.input.idempotencyKey}`,
				state: "preparing_handoff",
				targetHarness: switchMutation.input.targetHarness,
			}
			: undefined;
	const agentSwitch =
		currentAgentSwitch ??
		admissionAgentSwitch ??
		latestCompletedSwitch ??
		observedTerminalSwitch;
	if (agentSwitch && !isTerminalAgentSwitch(agentSwitch)) {
		observedNonterminalSwitchIdsRef.current.add(agentSwitch.id);
	}
	const presentation =
		agentSwitch && session
			? deriveAgentSwitchPresentation({
				agentSwitch,
				activityState: session.activity?.state,
				currentHarness: session.provider,
				isTerminated: Boolean(session.isTerminated),
				terminalHandleId: session.terminalHandleId,
			})
			: undefined;
	if (
		agentSwitch?.state === "completed" &&
		presentation?.outcome === "in_progress" &&
		presentation.stage === "confirming_takeover"
	) {
		observedNonterminalSwitchIdsRef.current.add(agentSwitch.id);
	}
	const observedSettledSwitch = Boolean(
		agentSwitch &&
			presentation?.outcome === "success" &&
			observedNonterminalSwitchIdsRef.current.has(agentSwitch.id),
	);
	const target = terminalTarget ?? { kind: "worker" };
	const switchLocksWorkerInput = Boolean(
		presentation?.lockAgentTerminal && !presentation.allowSourceInput,
	);
	const workerInputDisabled =
		target.kind === "worker" && (agentInputDisabled || switchLocksWorkerInput || switchSelectorOpen);
	const shownPresentation =
		presentation?.outcome === "failure" && dismissedFailureSwitchId === agentSwitch?.id
			? undefined
			: presentation?.outcome === "success"
			? transientSuccessSwitchId === agentSwitch?.id
				? presentation
				: undefined
			: presentation;
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
	useEffect(() => {
		setSwitchSelectorOpen(false);
	}, [session?.id]);

	const reorderAuxiliaryTerminals = useCallback(
		(nextKeys: string[]) => {
			if (!sessionId) return;
			const available = new Set(availableAuxiliaryKeys);
			const next = nextKeys.filter((key, index) => available.has(key) && nextKeys.indexOf(key) === index);
			for (const key of availableAuxiliaryKeys) {
				if (!next.includes(key)) next.push(key);
			}
			setTerminalOrder({ keys: next, sessionId });
		},
		[availableAuxiliaryKeys, sessionId],
	);
	const selectAdjacentTab = useCallback(
		(direction: -1 | 1) => {
			const activeKey =
				target.kind === "shell"
					? target.handleId
					: target.kind === "reviewer"
						? `reviewer:${target.handleId}`
						: "worker";
			const tabKeys = ["worker", ...orderedAuxiliaryTerminals.map((terminal) => terminal.key)];
			const activeIndex = Math.max(0, tabKeys.indexOf(activeKey));
			const nextIndex = (activeIndex + direction + tabKeys.length) % tabKeys.length;
			if (nextIndex === 0) {
				onSelectSessionTerminal?.();
				return;
			}
			const nextTerminal = orderedAuxiliaryTerminals[nextIndex - 1];
			if (nextTerminal?.kind === "reviewer") onSelectReviewerTerminal?.(nextTerminal.terminal);
			if (nextTerminal?.kind === "shell") onSelectShellTerminal?.(nextTerminal.terminal.handleId);
		},
		[
			onSelectReviewerTerminal,
			onSelectSessionTerminal,
			onSelectShellTerminal,
			orderedAuxiliaryTerminals,
			target,
		],
	);

	useEffect(() => {
		setTerminalOrder((current) => {
			if (!current) return current;
			if (!sessionId || current.sessionId !== sessionId) return null;
			const available = new Set(availableAuxiliaryKeys);
			const keys = current.keys.filter((key) => available.has(key));
			if (keys.length === current.keys.length) return current;
			return keys.length > 0 ? { ...current, keys } : null;
		});
	}, [availableAuxiliaryKeys, sessionId]);

	useEffect(() => {
		if (!switchMutation.isPending || currentAgentSwitch) return;
		void agentSwitchesQuery.refetch();
		const timer = window.setInterval(() => void agentSwitchesQuery.refetch(), 500);
		return () => window.clearInterval(timer);
	}, [agentSwitchesQuery.refetch, currentAgentSwitch, switchMutation.isPending]);

	useEffect(() => {
		setTransientSuccessSwitchId(undefined);
		setDismissedFailureSwitchId(undefined);
		setAlertAnnouncement(undefined);
	}, [session?.id]);

	useEffect(() => {
		if (!observedSettledSwitch || !agentSwitch) return;
		setTransientSuccessSwitchId(agentSwitch.id);
		const timer = window.setTimeout(() => {
		setTransientSuccessSwitchId((current) =>
			current === agentSwitch.id ? undefined : current,
		);
		}, 3_000);
		return () => window.clearTimeout(timer);
	}, [agentSwitch?.id, observedSettledSwitch]);

	useEffect(() => {
		if (!agentSwitch || !presentation?.allowSourceInput) return;
		if (sourceFocusSwitchIdRef.current === agentSwitch.id) return;
		sourceFocusSwitchIdRef.current = agentSwitch.id;
		if (target.kind !== "worker") onSelectSessionTerminal?.();
	}, [agentSwitch, onSelectSessionTerminal, presentation?.allowSourceInput, target.kind]);

	const alertKey =
		agentSwitch && presentation?.allowSourceInput
			? `${agentSwitch.id}:source-input`
			: agentSwitch && (presentation?.outcome === "failure" || presentation?.outcome === "recovery")
				? `${agentSwitch.id}:${presentation.outcome}`
				: undefined;
	const alertText = presentation
		? t(
				presentation.allowSourceInput ? presentation.descriptionKey : presentation.titleKey,
				presentation.values,
			)
		: undefined;
	useEffect(() => {
		if (!alertKey || !alertText) {
			setAlertAnnouncement(undefined);
			return;
		}
		if (announcedAlertKeysRef.current.has(alertKey)) return;
		announcedAlertKeysRef.current.add(alertKey);
		setAlertAnnouncement({ key: alertKey, text: alertText });
	}, [alertKey, alertText]);

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
		const activeKey =
			target.kind === "shell"
				? target.handleId
				: target.kind === "reviewer"
					? `reviewer:${target.handleId}`
					: undefined;
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
	}, [orderedAuxiliaryTerminals, target]);

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
						"flex min-w-0 shrink items-center pr-3",
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
							className="flex h-full min-w-flex-min flex-1 items-center"
							onKeyDown={handleTerminalTabListKeyDown}
							role="tablist"
						>
							{/* The owning session is permanent and never participates in overflow or reordering. */}
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
							<div
								ref={tabsOverflowRef}
								className="scrollbar-none flex min-w-flex-min flex-1 self-stretch items-center overflow-x-auto"
							>
								<Reorder.Group
									as="div"
									axis="x"
									className="flex self-stretch"
									onReorder={reorderAuxiliaryTerminals}
									values={orderedAuxiliaryTerminals.map((terminal) => terminal.key)}
								>
									{orderedAuxiliaryTerminals.map((terminal) => (
										<DraggableTerminalTab key={terminal.key} value={terminal.key}>
											{terminal.kind === "reviewer" ? (
												<SessionPaneTab
													appearance="connected"
													icon={
														<AgentAvatar
															provider={terminal.terminal.harness}
															className="size-terminal-agent-icon"
															decorative
														/>
													}
													isActive={target.kind === "reviewer"}
													label={t("terminal.reviewer")}
													onSelect={() => onSelectReviewerTerminal?.(terminal.terminal)}
													title={terminal.terminal.harness}
												/>
											) : (
												<ShellTerminalTab
													appearance="connected"
													isActive={target.kind === "shell" && target.handleId === terminal.terminal.handleId}
													onClose={() => onCloseShellTerminal?.(terminal.terminal.handleId)}
													onRename={
														onRenameShellTerminal
															? (title) => onRenameShellTerminal(terminal.terminal.handleId, title)
															: undefined
													}
													onSelect={() => onSelectShellTerminal?.(terminal.terminal.handleId)}
													shell={terminal.terminal}
												/>
											)}
										</DraggableTerminalTab>
									))}
								</Reorder.Group>
							</div>
					</div>
				</div>
				{isFullscreen ? null : (
					<div
						className="ml-auto flex shrink-0 items-center px-3"
			data-testid="session-action-region"
		>
			{session ? (
				<TerminalSwitchAgentButton
					key={session.id}
					container={switchSelectorContainer}
					onOpenChange={setSwitchSelectorOpen}
					open={switchSelectorOpen}
					presentation={presentation}
					session={session}
					switchError={switchMutation.error}
				/>
			) : null}
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
				ref={setSwitchSelectorContainer}
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
						focusRequested={
							target.kind === "worker" &&
							(Boolean(presentation?.allowSourceInput) || transientSuccessSwitchId === agentSwitch?.id)
						}
						isFullscreen={isFullscreen}
						inputDisabled={workerInputDisabled}
						onChangeFontSize={updateFontSize}
						onToggleFullscreen={toggleFullscreen}
						session={session}
						terminalTarget={target}
						theme={theme}
					/>
				</div>
				{switchSelectorOpen ? null : shownPresentation && agentSwitch && target.kind === "worker" ? (
					<AgentSwitchTerminalOverlay
						agentSwitch={agentSwitch}
						onDismiss={
							shownPresentation.outcome === "failure"
								? () => setDismissedFailureSwitchId(agentSwitch.id)
								: undefined
						}
						presentation={shownPresentation}
					/>
				) : shownPresentation && agentSwitch ? (
					<AgentSwitchTerminalStrip
						onSelectSessionTerminal={onSelectSessionTerminal}
						presentation={shownPresentation}
					/>
				) : null}
				{alertAnnouncement ? (
					<p key={alertAnnouncement.key} className="sr-only" role="alert">
						{alertAnnouncement.text}
					</p>
				) : null}
			</div>
		</div>
	);
}

type AgentSwitchTerminalOverlayProps = {
	agentSwitch: AgentSwitchSummary;
	onDismiss?: () => void;
	presentation: AgentSwitchPresentation;
};

function AgentSwitchTerminalOverlay({
	agentSwitch,
	onDismiss,
	presentation,
}: AgentSwitchTerminalOverlayProps) {
	const { t } = useTranslation();
	const overlayRef = useRef<HTMLDivElement | null>(null);
	const title = t(presentation.titleKey, presentation.values);
	const description = t(presentation.descriptionKey, presentation.values);
	const sourceInput = presentation.allowSourceInput;
	const staticWarning = presentation.outcome === "failure" || presentation.outcome === "recovery";
	const success = presentation.outcome === "success";
	const focusLockedStatus = presentation.lockAgentTerminal && !sourceInput && !success;
	useEffect(() => {
		if (focusLockedStatus) overlayRef.current?.focus({ preventScroll: true });
	}, [focusLockedStatus]);

	return (
		<div
			ref={overlayRef}
			aria-label={title}
			aria-atomic="true"
			aria-busy={!sourceInput && !staticWarning && !success && presentation.animate ? true : undefined}
			aria-live="polite"
			className={cn(
				"z-20 flex",
				sourceInput
					? "agent-switch-source-input-strip pointer-events-none absolute inset-x-3 top-3 justify-center"
					: "agent-switch-terminal-scrim absolute inset-0 items-center justify-center animate-overlay-in motion-reduce:animate-none",
				!presentation.lockAgentTerminal && "pointer-events-none",
				presentation.animate && !staticWarning && !sourceInput && "cursor-wait",
			)}
			data-testid="agent-switch-terminal-overlay"
			role="status"
			tabIndex={-1}
		>
			{sourceInput || staticWarning || success ? (
				<div className={cn(
					"agent-switch-attention-card pointer-events-auto relative flex max-w-md items-start gap-3 rounded-lg border bg-surface/95 px-4 py-3 text-left shadow-lg",
					onDismiss && "pr-11",
					success
						? "border-success/40"
						: presentation.tone === "danger"
							? "border-danger/40"
							: "border-warning/40",
				)}>
					{success ? (
						<CheckCircle2 aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-success" />
					) : (
						<TriangleAlert
							aria-hidden="true"
							className={cn(
								"mt-0.5 size-5 shrink-0",
								presentation.tone === "danger" ? "text-danger" : "text-warning",
							)}
						/>
					)}
					<div className="min-w-0">
						<p className="font-mono text-control font-medium text-foreground">{title}</p>
						<p className="mt-1 text-caption leading-4 text-muted-foreground">{description}</p>
					</div>
					{onDismiss ? (
						<button
							aria-label={t("common.close")}
							className="absolute right-2 top-2 grid size-7 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50"
							onClick={onDismiss}
							type="button"
						>
							<X aria-hidden="true" className="size-icon-sm" />
						</button>
					) : null}
				</div>
			) : (
				<div
					className="flex max-w-lg animate-modal-in flex-col items-center gap-5 rounded-xl border border-border-strong bg-surface/95 px-8 py-6 text-center shadow-xl shadow-black/20 motion-reduce:animate-none"
					data-testid="agent-switch-transition-card"
				>
					<div className="flex items-center gap-5 sm:gap-7">
						<SwitchingAgentMark harness={agentSwitch.fromHarness} />
						<div
							aria-hidden="true"
							className="relative h-4 w-20 shrink-0 text-accent sm:w-28"
							data-testid="agent-switch-transfer-arrow"
						>
							<ArrowRight
								className="absolute inset-0 size-full text-foreground/55"
								data-testid="agent-switch-transfer-arrow-icon"
								strokeWidth={1.5}
							/>
							<span
								className="absolute inset-y-[7px] left-0 right-3 overflow-hidden"
								data-testid="agent-switch-transfer-shaft"
							>
								<span className="agent-switch-transfer-pulse absolute inset-y-0 w-10 bg-gradient-to-r from-transparent via-accent to-transparent" />
							</span>
						</div>
						<SwitchingAgentMark harness={agentSwitch.targetHarness} />
					</div>
					<div className="flex w-full flex-col items-center" data-testid="agent-switch-status-group">
						<p className="font-mono text-control font-medium text-foreground">{title}</p>
						<p className="mt-2 text-caption leading-4 text-muted-foreground">{description}</p>
						<AgentSwitchProgressTrack stage={presentation.stage} />
					</div>
				</div>
			)}
		</div>
	);
}

function AgentSwitchTerminalStrip({
	onSelectSessionTerminal,
	presentation,
}: {
	onSelectSessionTerminal?: () => void;
	presentation: AgentSwitchPresentation;
}) {
	const { t } = useTranslation();
	return (
		<div
			aria-label={t(presentation.titleKey, presentation.values)}
			aria-atomic="true"
			aria-live="polite"
			className="agent-switch-shell-strip absolute inset-x-3 top-3 z-20 flex items-center justify-between gap-3 rounded-lg border border-border-strong bg-surface/95 px-3 py-2 shadow-lg"
			role="status"
		>
			<span className="min-w-0 truncate text-caption text-muted-foreground">
				{t(presentation.descriptionKey, presentation.values)}
			</span>
			<button
				className="shrink-0 rounded-md border border-border-strong bg-background px-2.5 py-1 text-caption font-medium text-foreground transition-colors hover:bg-interactive-hover focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent/50"
				onClick={onSelectSessionTerminal}
				type="button"
			>
				{t("terminal.backToAgent")}
			</button>
		</div>
	);
}

const agentSwitchProgressSteps = [
	{ key: "preparing", labelKey: "switchAgent.state.preparingHandoff" },
	{ key: "stopping_source", labelKey: "switchAgent.state.stoppingSource" },
	{ key: "starting_target", labelKey: "switchAgent.state.startingTarget" },
	{ key: "confirming_takeover", labelKey: "switchAgent.state.deliveringContext" },
] as const;

function AgentSwitchProgressTrack({ stage }: { stage: AgentSwitchPresentation["stage"] }) {
	const { t } = useTranslation();
	const activeIndex = agentSwitchProgressSteps.findIndex((step) => step.key === stage);
	return (
		<ol className="agent-switch-progress-track mt-2.5 flex w-full items-start" aria-label={t("switchAgent.switching")}>
			{agentSwitchProgressSteps.map((step, index) => (
				<li
					key={step.key}
					aria-current={index === activeIndex ? "step" : undefined}
					className={cn(
						"agent-switch-progress-step relative flex min-w-0 flex-1 flex-col items-center gap-1.5 px-0.5 text-[10px] leading-3 text-passive",
						index < activeIndex && "is-complete text-muted-foreground",
						index === activeIndex && "is-current text-foreground",
					)}
				>
					<span aria-hidden="true" className="agent-switch-progress-dot relative z-10 size-2 rounded-full border border-border-strong bg-surface" />
					<span className="min-h-6 max-w-16 break-words whitespace-normal text-balance text-center">
						{t(step.labelKey)}
					</span>
				</li>
			))}
		</ol>
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
	appearance?: "primary" | "connected";
	onSelect?: () => void;
	session?: WorkspaceSession;
	icon?: ReactNode;
	title?: string;
};

// Shared tab chrome: the open tab is highlighted with the same rounded
// background as the inspector rail tabs (Summary · Reviews · Browser), and
// the full label only becomes the hover tooltip when the tab strip is
// crowded enough to truncate it.
function SessionPaneTab({
	label,
	isActive,
	appearance = "primary",
	onSelect,
	session,
	icon,
	title,
}: SessionPaneTabProps) {
	const { t } = useTranslation();
	const { ref, isTruncated } = useTruncatedText<HTMLButtonElement>(label);
	const activity = session ? getAgentActivityView(session.activity, t) : undefined;
	const activityLabel = activity?.label;
	const activityTone = activity?.tone;
	const activityBreathe = activity?.breathe;
	const tabIcon = session ? <AgentAvatar className="size-terminal-agent-icon" decorative provider={session.provider} /> : icon;
	const connected = appearance === "connected";
	return (
		<span
			data-terminal-role={connected ? undefined : "primary"}
			className={cn(
				"group relative inline-flex min-w-shell-tab-min shrink-0 self-stretch items-center gap-1.5 transition-colors",
				connected
					? "w-shell-tab-connected border-x border-transparent px-2"
					: "border-r border-border bg-surface px-3 text-foreground",
				connected
					? isActive
						? "border-border-strong bg-overlay text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground/80"
						: "text-passive hover:bg-interactive-hover/60 hover:text-foreground"
					: isActive
						? "bg-overlay text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground/80"
						: "text-muted-foreground hover:bg-raised hover:text-foreground",
			)}
		>
			<button
				ref={ref}
				aria-current={isActive}
				aria-label={activityLabel ? `${label} · ${activityLabel}` : label}
				aria-selected={isActive}
				className={cn(
					"inline-flex items-center gap-1.5 truncate text-control leading-none transition-colors",
					connected
						? "min-w-0 w-full text-left font-normal"
						: "min-w-flex-min max-w-shell-tab-max font-medium",
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
				{activityTone ? (
					<span
						aria-hidden="true"
						className="inline-flex shrink-0 self-center items-center"
						style={{ color: activityTone }}
						title={activityLabel}
					>
						<span
							className={cn("size-1.5 rounded-full", activityBreathe && "animate-status-pulse")}
							style={{ background: activityTone }}
						/>
					</span>
				) : null}
			</button>
		</span>
	);
}
