import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import {
	ArrowLeft,
	ArrowRight,
	Bug,
	Check,
	Globe2,
	Layers3,
	Maximize2,
	Minimize2,
	Monitor,
	MousePointer2,
	Plus,
	RefreshCw,
	Smartphone,
	Tablet,
	X,
} from "lucide-react";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { useBrowserView, type BrowserViewModel } from "../hooks/useBrowserView";
import { formatBrowserAnnotationMessage, type BrowserAnnotationSubmitPayload } from "../../shared/browser-annotations";
import { MAX_BROWSER_TABS } from "../../shared/browser-tabs";
import type { WorkspaceSession } from "../types/workspace";
import { Button } from "./ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./ui/dropdown-menu";
import { Input } from "./ui/input";
import { BrowserTabsRail, type BrowserTabsRailHandle } from "./BrowserTabsRail";
import { cn } from "../lib/utils";
import { appI18n, type MessageKey } from "../i18n";

// One-click viewport width presets for responsive testing — height is shown
// for reference but not enforced (only width drives CSS breakpoints, and
// this is a docked panel of limited, variable height, not a device
// emulator). No "Desktop" entry: the panel is already viewed on desktop, so
// that preset was always a no-op. "Custom" covers anything these named
// devices don't — you're never stuck with only this list.
//
// Matches Chrome DevTools' own "Standard" device list (front_end/models/
// emulation/EmulatedDevices.ts) so anyone already familiar with that list
// finds the same names here. Dimensions are each device's portrait/vertical
// mode from that source; Nest Hub/Max are fixed-landscape smart displays, so
// their one orientation is used directly. iPad Air and Nest Hub have since
// been dropped from Chrome's own current list but are kept here since
// they're still common, well-known breakpoints worth testing against.
const DEVICE_PRESETS: { id: string; label: string; width: number; height: number; category: "phone" | "tablet" }[] = [
	{ id: "iphone-se", label: "iPhone SE", width: 375, height: 667, category: "phone" },
	{ id: "iphone-xr", label: "iPhone XR", width: 414, height: 896, category: "phone" },
	{ id: "iphone-12-pro", label: "iPhone 12 Pro", width: 390, height: 844, category: "phone" },
	{ id: "iphone-14-pro-max", label: "iPhone 14 Pro Max", width: 430, height: 932, category: "phone" },
	{ id: "iphone-15-pro-max", label: "iPhone 15 Pro Max", width: 430, height: 932, category: "phone" },
	{ id: "iphone-16-pro-max", label: "iPhone 16 Pro Max", width: 440, height: 956, category: "phone" },
	{ id: "pixel-7", label: "Pixel 7", width: 412, height: 915, category: "phone" },
	{ id: "pixel-8", label: "Pixel 8", width: 412, height: 915, category: "phone" },
	{ id: "pixel-9", label: "Pixel 9", width: 412, height: 924, category: "phone" },
	{ id: "pixel-10", label: "Pixel 10", width: 412, height: 924, category: "phone" },
	{ id: "galaxy-s8-plus", label: "Samsung Galaxy S8+", width: 360, height: 740, category: "phone" },
	{ id: "galaxy-s20-ultra", label: "Samsung Galaxy S20 Ultra", width: 412, height: 915, category: "phone" },
	{ id: "galaxy-a51-71", label: "Samsung Galaxy A51/71", width: 412, height: 914, category: "phone" },
	{ id: "ipad-mini", label: "iPad Mini", width: 768, height: 1024, category: "tablet" },
	{ id: "ipad-air", label: "iPad Air", width: 820, height: 1180, category: "tablet" },
	{ id: "ipad-pro", label: "iPad Pro", width: 1032, height: 1376, category: "tablet" },
	{ id: "surface-pro-7", label: "Surface Pro 7", width: 912, height: 1368, category: "tablet" },
	{ id: "surface-duo", label: "Surface Duo", width: 540, height: 720, category: "phone" },
	{ id: "galaxy-z-fold-5", label: "Galaxy Z Fold 5", width: 344, height: 882, category: "phone" },
	{ id: "zenbook-fold", label: "Asus Zenbook Fold", width: 853, height: 1280, category: "tablet" },
	{ id: "nest-hub", label: "Nest Hub", width: 1024, height: 600, category: "tablet" },
	{ id: "nest-hub-max", label: "Nest Hub Max", width: 1280, height: 800, category: "tablet" },
];
const CUSTOM_DEVICE_PRESET_ID = "custom";
const MIN_DEVICE_FRAME_WIDTH = 240;
const MAX_DEVICE_FRAME_WIDTH = 2560;

function clampDeviceFrameWidth(width: number): number | undefined {
	if (!Number.isFinite(width)) return undefined;
	return Math.min(MAX_DEVICE_FRAME_WIDTH, Math.max(MIN_DEVICE_FRAME_WIDTH, Math.round(width)));
}

type BrowserPanelProps = {
	session: WorkspaceSession;
	active: boolean;
	poppedOut: boolean;
	onTogglePopOut: (next: boolean) => void;
};

type AnnotationStatus = "idle" | "picking" | "queued" | "sending" | "sent" | "error";

// Docked rail visibility: collapsed (0px, tab access via the toolbar trigger) is
// the default; pinning restores an always-visible icon rail. Persisted so it's a
// one-time choice, not a state.
const RAIL_PINNED_STORAGE_KEY = "ao.browserTabs.railPinned";

export type BrowserAnnotationQueueModel = {
	status: AnnotationStatus;
	error: string;
	queuedCount: number;
	beginPicking: () => void;
	cancelPicking: () => void;
	enqueue: (payload: BrowserAnnotationSubmitPayload) => void;
	failPicking: (message: string) => void;
	retryQueued: () => void;
};

export function useBrowserAnnotationQueue({
	sessionId,
	navUrl,
}: {
	sessionId?: string;
	navUrl?: string;
}): BrowserAnnotationQueueModel {
	const [state, setState] = useState<{ status: AnnotationStatus; error: string; queuedCount: number }>({
		status: "idle",
		error: "",
		queuedCount: 0,
	});
	const annotationQueueRef = useRef<BrowserAnnotationSubmitPayload[]>([]);
	const annotationSendingRef = useRef(false);
	const sessionIdRef = useRef(sessionId ?? "");
	const generationRef = useRef(0);
	const sentTimerRef = useRef<number | null>(null);

	const resetQueue = useCallback(() => {
		generationRef.current += 1;
		if (sentTimerRef.current !== null) window.clearTimeout(sentTimerRef.current);
		sentTimerRef.current = null;
		annotationQueueRef.current = [];
		annotationSendingRef.current = false;
		setState({ status: "idle", error: "", queuedCount: 0 });
	}, []);

	const drainAnnotationQueue = useCallback(() => {
		if (annotationSendingRef.current || !sessionIdRef.current) {
			return;
		}

		const payload = annotationQueueRef.current.shift();
		setState((current) => ({ ...current, queuedCount: annotationQueueRef.current.length }));
		if (!payload) return;

		annotationSendingRef.current = true;
		const sendGeneration = generationRef.current;
		const sendSessionId = sessionIdRef.current;
		setState({ status: "sending", error: "", queuedCount: annotationQueueRef.current.length });

		void (async () => {
			let sent = false;
			let failureMessage = appI18n.t("browser.unableSendAnnotation");
			try {
				const message = formatBrowserAnnotationMessage(payload);
				const { error } = await apiClient.POST("/api/v1/sessions/{sessionId}/send", {
					params: { path: { sessionId: sendSessionId } },
					body: { message, attachment: payload.snapshot },
				});
				if (error) {
					failureMessage = apiErrorMessage(error, appI18n.t("browser.unableSendAnnotation"));
					return;
				}
				sent = true;
			} catch (error) {
				failureMessage = apiErrorMessage(error, appI18n.t("browser.unableSendAnnotation"));
			} finally {
				if (sendGeneration !== generationRef.current || sendSessionId !== sessionIdRef.current) return;
				annotationSendingRef.current = false;
				if (!sent) {
					annotationQueueRef.current.unshift(payload);
					setState({
						status: "error",
						error: failureMessage,
						queuedCount: annotationQueueRef.current.length,
					});
					return;
				}

				const queuedCount = annotationQueueRef.current.length;
				setState({ status: queuedCount > 0 ? "queued" : "sent", error: "", queuedCount });
				if (queuedCount > 0) {
					drainAnnotationQueue();
				} else {
					if (sentTimerRef.current !== null) window.clearTimeout(sentTimerRef.current);
					sentTimerRef.current = window.setTimeout(() => {
						sentTimerRef.current = null;
						setState((current) =>
							current.status === "sent" ? { status: "idle", error: "", queuedCount: 0 } : current,
						);
					}, 2_000);
				}
			}
		})();
	}, []);

	useEffect(() => {
		sessionIdRef.current = sessionId ?? "";
		resetQueue();
	}, [resetQueue, sessionId]);

	useEffect(() => {
		if (navUrl) return;
		resetQueue();
	}, [navUrl, resetQueue]);

	useEffect(
		() => () => {
			if (sentTimerRef.current !== null) window.clearTimeout(sentTimerRef.current);
		},
		[],
	);

	const beginPicking = useCallback(() => {
		setState((current) => ({ ...current, status: "picking", error: "" }));
	}, []);

	const cancelPicking = useCallback(() => {
		setState((current) => ({
			status: annotationQueueRef.current.length > 0 ? "queued" : current.status === "sending" ? "sending" : "idle",
			error: "",
			queuedCount: annotationQueueRef.current.length,
		}));
	}, []);

	const failPicking = useCallback((message: string) => {
		setState({ status: "error", error: message, queuedCount: annotationQueueRef.current.length });
	}, []);

	const enqueue = useCallback(
		(payload: BrowserAnnotationSubmitPayload) => {
			annotationQueueRef.current.push(payload);
			setState({ status: "queued", error: "", queuedCount: annotationQueueRef.current.length });
			drainAnnotationQueue();
		},
		[drainAnnotationQueue],
	);

	const retryQueued = useCallback(() => {
		if (annotationQueueRef.current.length === 0) return;
		setState({ status: "queued", error: "", queuedCount: annotationQueueRef.current.length });
		drainAnnotationQueue();
	}, [drainAnnotationQueue]);

	return {
		status: state.status,
		error: state.error,
		queuedCount: state.queuedCount,
		beginPicking,
		cancelPicking,
		enqueue,
		failPicking,
		retryQueued,
	};
}

export function BrowserPanel({ session, active, poppedOut, onTogglePopOut }: BrowserPanelProps) {
	const browserView = useBrowserView({
		sessionId: session.id,
		active,
		poppedOut,
		previewUrl: session.previewUrl,
		previewRevision: session.previewRevision,
	});
	const annotationQueue = useBrowserAnnotationQueue({
		sessionId: session.id,
		navUrl: browserView.navState.url,
	});
	return (
		<BrowserPanelView
			active={active}
			annotationQueue={annotationQueue}
			browserView={browserView}
			onTogglePopOut={onTogglePopOut}
			poppedOut={poppedOut}
			session={session}
		/>
	);
}

export function BrowserPanelView({
	poppedOut,
	onTogglePopOut,
	browserView,
	annotationQueue,
}: BrowserPanelProps & { annotationQueue: BrowserAnnotationQueueModel; browserView: BrowserViewModel }) {
	const { t } = useTranslation();
	const {
		viewId,
		navState,
		slotRef,
		navigate,
		goBack,
		goForward,
		reload,
		stop,
		tabs,
		activeTabId,
		tabNotice,
		selectTab,
		closeTab,
		openTab,
		reorderTabs,
		closedTabs,
		reopenClosedTab,
		agentBrowserActive,
		agentBrowserActivity,
		devtoolsState = { viewId: "", open: false, activeTabId: "" },
		openDevTools = async () => undefined,
		closeDevTools = async () => undefined,
		annotationMode,
		setAnnotationMode,
	} = browserView;
	const [urlInput, setUrlInput] = useState(navState.url);
	const { beginPicking, cancelPicking, enqueue, error, failPicking, queuedCount, retryQueued, status } =
		annotationQueue;
	const hasNativeBrowser = Boolean(window.ao?.browser);
	const showStaticPreview = !hasNativeBrowser && navState.url !== "";
	const canAnnotate = Boolean(window.ao?.browser && viewId && navState.url);
	const canRetryAnnotation = status === "error" && queuedCount > 0;
	const canOpenTab = tabs.length < MAX_BROWSER_TABS;
	const [devicePreset, setDevicePreset] = useState<string | null>(null);
	const [customDeviceWidth, setCustomDeviceWidth] = useState("390");
	const deviceFrameWidth =
		devicePreset === CUSTOM_DEVICE_PRESET_ID
			? clampDeviceFrameWidth(Number(customDeviceWidth))
			: DEVICE_PRESETS.find((preset) => preset.id === devicePreset)?.width;
	const railRef = useRef<BrowserTabsRailHandle>(null);
	const urlInputRef = useRef<HTMLInputElement>(null);
	const [pinned, setPinned] = useState(() => window.localStorage.getItem(RAIL_PINNED_STORAGE_KEY) === "1");
	const showTabsTrigger = !poppedOut && !pinned && tabs.length >= 2;

	const handlePinnedChange = useCallback((next: boolean) => {
		setPinned(next);
		window.localStorage.setItem(RAIL_PINNED_STORAGE_KEY, next ? "1" : "0");
	}, []);

	// Docked DevTools belongs to the native page view, which is intentionally
	// hidden while the active target is blank. Keep close available for any
	// in-flight state update, but do not offer an open action with no page.
	const canUseDevTools = hasNativeBrowser && Boolean(viewId) && Boolean(navState.url || devtoolsState.open);

	useEffect(() => {
		setUrlInput(navState.url);
		// A prior submit (typed, or pasted, then Enter) leaves the caret at the
		// end of the old value; the browser keeps that same horizontal scroll
		// position for the new value, scrolling the scheme/host off the left
		// edge (e.g. showing "://example.com" instead of "https://example.com").
		// Reset it once the DOM has the new value committed, so the address is
		// readable from the start like a real address bar after navigating.
		const frame = window.requestAnimationFrame(() => {
			if (urlInputRef.current) urlInputRef.current.scrollLeft = 0;
		});
		return () => window.cancelAnimationFrame(frame);
	}, [navState.url]);

	useEffect(() => {
		const offSubmit = window.ao?.browser.onAnnotationSubmit((payload) => {
			if (payload.viewId !== viewId) return;
			enqueue(payload);
		});
		const offCancel = window.ao?.browser.onAnnotationCancel((payload) => {
			if (payload.viewId !== viewId) return;
			cancelPicking();
		});
		return () => {
			offSubmit?.();
			offCancel?.();
		};
	}, [cancelPicking, enqueue, viewId]);

	const submit = (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		const nextURL = urlInput.trim();
		if (nextURL) void navigate(nextURL);
	};

	const toggleAnnotationMode = async () => {
		if (!canAnnotate || status === "sending") return;
		if (canRetryAnnotation) {
			retryQueued();
			return;
		}
		const next = !(annotationMode || status === "picking");
		try {
			await setAnnotationMode(next);
			if (next) {
				beginPicking();
			} else {
				cancelPicking();
			}
		} catch (error) {
			failPicking(error instanceof Error ? error.message : appI18n.t("browser.unableStartAnnotation"));
		}
	};

	// The button lives in the toolbar, not inside the rail, so a fast
	// hover-rail-then-click-here still needs to force the flyout closed first —
	// same reason rows inside the rail do it (see BrowserTabsRail.tsx). A blank
	// new tab has nowhere to go on its own, so send focus straight to the URL
	// bar afterward instead of leaving the user to click into it themselves.
	const handleOpenTab = useCallback(async () => {
		railRef.current?.closeFlyout(true);
		await openTab();
		urlInputRef.current?.focus();
		urlInputRef.current?.select();
	}, [openTab]);

	const handleSelectTab = useCallback(
		async (tabId: string) => {
			try {
				await selectTab(tabId);
			} catch {
				// The existing tab remains active.
			}
		},
		[selectTab],
	);

	const annotationStatusLabel =
		status === "picking"
			? t("browser.pickElement")
			: status === "queued"
				? queuedCount > 1
					? t("browser.queuedCount", { count: queuedCount })
					: t("browser.queued")
				: status === "sending"
					? t("browser.sending")
					: status === "sent"
						? t("browser.sent")
						: status === "error"
							? error
							: "";
	const agentStatusLabel = agentActivityLabel(agentBrowserActivity, agentBrowserActive);
	return (
		<div
			className={cn(
				"browser-panel flex h-full min-h-browser-min flex-col overflow-hidden rounded-lg border border-border bg-background",
				poppedOut && "browser-panel--popped-out",
				agentStatusLabel && "browser-panel--agent-active",
			)}
			data-browser-native-page={navState.url ? "live" : "empty"}
			data-testid="browser-panel"
			role="tabpanel"
		>
			<form
				className="browser-panel__toolbar flex shrink-0 min-w-0 items-center gap-1 border-b border-border bg-surface"
				data-testid="browser-toolbar"
				onSubmit={submit}
			>
				<Button
					aria-label={t("browser.back")}
					disabled={!navState.canGoBack}
					onClick={() => void goBack()}
					size="icon-sm"
					type="button"
					variant="ghost"
				>
					<ArrowLeft aria-hidden="true" className="size-icon-base" />
				</Button>
				<Button
					aria-label={t("browser.forward")}
					disabled={!navState.canGoForward}
					onClick={() => void goForward()}
					size="icon-sm"
					type="button"
					variant="ghost"
				>
					<ArrowRight aria-hidden="true" className="size-icon-base" />
				</Button>
				<Button
					aria-label={navState.isLoading ? t("browser.stop") : t("browser.reload")}
					onClick={() => void (navState.isLoading ? stop() : reload())}
					size="icon-sm"
					type="button"
					variant="ghost"
				>
					{navState.isLoading ? (
						<X aria-hidden="true" className="size-icon-base" />
					) : (
						<RefreshCw aria-hidden="true" className="size-icon-base" />
					)}
				</Button>
				<Button
					aria-label={
						canRetryAnnotation
							? t("browser.retryAnnotation")
							: annotationMode || status === "picking"
								? t("browser.cancelAnnotation")
								: t("browser.annotate")
					}
					aria-pressed={annotationMode || status === "picking"}
					className="browser-panel__annotate-btn relative"
					disabled={!canAnnotate || status === "sending"}
					onClick={() => void toggleAnnotationMode()}
					size="icon-sm"
					// Status is available on hover/focus (native title tooltip on the same
					// button, plus the corner dot below) rather than permanently-visible
					// on-screen text — mirrors the design note on annotate-preload.ts's
					// on-page hint banner. Falls back to the button's own static label
					// when there's no live status to report.
					title={annotationStatusLabel || agentStatusLabel || (canRetryAnnotation ? t("browser.retryAnnotation") : t("browser.annotate"))}
					type="button"
					variant="ghost"
				>
					<MousePointer2 aria-hidden="true" className="h-4 w-4" />
					{annotationStatusLabel ? (
						<span
							aria-hidden="true"
							className={cn(
								"pointer-events-none absolute -right-0.5 -top-0.5 size-1.5 rounded-full",
								status === "error" ? "bg-destructive" : "bg-accent",
							)}
						/>
					) : agentStatusLabel ? (
						<span aria-hidden="true" className="pointer-events-none absolute -right-0.5 -top-0.5 size-1.5 rounded-full bg-accent" />
					) : null}
				</Button>
				{annotationStatusLabel ? (
					<span className="sr-only" role="status">
						{annotationStatusLabel}
					</span>
				) : agentStatusLabel ? (
					<span aria-live="polite" className="sr-only" role="status">
						{agentStatusLabel}
					</span>
				) : null}
					<div className="browser-panel__url-wrap relative min-w-0 flex-1">
						<Globe2
							aria-hidden="true"
							className="browser-panel__url-icon"
							data-testid="browser-url-icon"
						/>
					<Input
						aria-label={t("browser.url")}
						className="browser-panel__url-input h-browser-url pl-browser-url font-mono text-xs"
						onChange={(event) => setUrlInput(event.target.value)}
						placeholder={t("browser.urlPlaceholder")}
						ref={urlInputRef}
						value={urlInput}
					/>
				</div>
				{tabNotice ? (
					<span className="max-w-24 truncate text-caption text-accent" role="status">
						{tabNotice}
					</span>
				) : null}
				<DropdownMenu>
					<DropdownMenuTrigger asChild>
						<Button
							aria-label={t("browser.devicePreset")}
							aria-pressed={devicePreset !== null}
							className={cn(
								devicePreset !== null &&
									"bg-accent-strong text-accent-foreground hover:bg-accent-strong dark:hover:bg-accent-strong",
							)}
							size="icon-sm"
							title={t("browser.devicePreset")}
							type="button"
							variant="ghost"
						>
							{(() => {
								const active = DEVICE_PRESETS.find((preset) => preset.id === devicePreset);
								const ActiveIcon = active ? (active.category === "tablet" ? Tablet : Smartphone) : Monitor;
								return <ActiveIcon aria-hidden="true" className="size-icon-base" />;
							})()}
						</Button>
					</DropdownMenuTrigger>
					{/* Opens directly over the live page (the toolbar sits right above the
					    native browser view), so without this it renders behind the native
					    view — Electron always paints native view pixels above the
					    renderer. Marked as a browser overlay so useBrowserView.ts's
					    MutationObserver raises the transparent shell above the native view
					    for as long as this stays mounted+open. See the matching comment on
					    BrowserTabsRail's flyout for the full mechanism. */}
					<DropdownMenuContent align="end" className="w-64" data-browser-native-overlay="true">
						<DropdownMenuItem className="gap-1.5" onSelect={() => setDevicePreset(null)}>
							<span className="flex size-4 shrink-0 items-center justify-center">
								{devicePreset === null ? <Check aria-hidden="true" className="text-accent" /> : null}
							</span>
							{t("browser.deviceFit")}
						</DropdownMenuItem>
						<div className="my-1 h-px bg-border" role="separator" />
						<div className="flex max-h-72 flex-col gap-px overflow-y-auto">
							{DEVICE_PRESETS.map((preset) => {
								const PresetIcon = preset.category === "tablet" ? Tablet : Smartphone;
								return (
									<DropdownMenuItem
										className="gap-1.5"
										key={preset.id}
										onSelect={() => setDevicePreset(preset.id)}
									>
										<span className="flex size-4 shrink-0 items-center justify-center">
											{devicePreset === preset.id ? <Check aria-hidden="true" className="text-accent" /> : null}
										</span>
										<PresetIcon aria-hidden="true" className="size-3.5 shrink-0 text-passive" />
										<span className="flex-1 truncate">{preset.label}</span>
										<span className="shrink-0 font-mono text-caption text-passive">
											{preset.width}×{preset.height}
										</span>
									</DropdownMenuItem>
								);
							})}
						</div>
						<div className="my-1 h-px bg-border" role="separator" />
						<label className="flex items-center gap-1.5 px-2 py-1.5 text-body">
							<span className="flex size-4 shrink-0 items-center justify-center">
								{devicePreset === CUSTOM_DEVICE_PRESET_ID ? <Check aria-hidden="true" className="text-accent" /> : null}
							</span>
							<span className="flex-1">{t("browser.deviceCustomWidth")}</span>
							<Input
								className="h-6 w-16 shrink-0 px-1.5 text-right font-mono text-caption"
								inputMode="numeric"
								max={MAX_DEVICE_FRAME_WIDTH}
								min={MIN_DEVICE_FRAME_WIDTH}
								onChange={(event) => {
									setCustomDeviceWidth(event.target.value);
									setDevicePreset(CUSTOM_DEVICE_PRESET_ID);
								}}
								onClick={(event) => event.stopPropagation()}
								type="number"
								value={customDeviceWidth}
							/>
						</label>
					</DropdownMenuContent>
				</DropdownMenu>
				<Button
					aria-label={t(devtoolsState.open ? "browser.closeDevTools" : "browser.openDevTools")}
					aria-pressed={devtoolsState.open}
					className={cn(
						devtoolsState.open &&
							"bg-accent-strong text-accent-foreground hover:bg-accent-strong dark:hover:bg-accent-strong",
					)}
					disabled={!canUseDevTools}
					onClick={() => void (devtoolsState.open ? closeDevTools() : openDevTools())}
					size="icon-sm"
					title={t(devtoolsState.open ? "browser.closeDevTools" : "browser.openDevTools")}
					type="button"
					variant="ghost"
				>
					<Bug aria-hidden="true" className="size-icon-base" />
				</Button>
				<Button
					aria-label={poppedOut ? t("browser.returnToPanel") : t("browser.popOut")}
					onClick={() => onTogglePopOut(!poppedOut)}
					size="icon-sm"
					type="button"
					variant="ghost"
				>
					{poppedOut ? (
						<Minimize2 aria-hidden="true" className="size-icon-base" />
					) : (
						<Maximize2 aria-hidden="true" className="size-icon-base" />
					)}
				</Button>
				{/* Docked mode has no reserved rail column by default (see
				    BrowserTabsRail.tsx) — this trigger is the only way to reach the tab
				    list until the user pins the rail. Hidden at a single tab, same as
				    the rail's own hover trigger was before this existed. Hover/focus
				    drive the rail's flyout imperatively since the two live in separate
				    DOM subtrees (toolbar row vs. body row) — see BrowserTabsRail.tsx's
				    BrowserTabsRailHandle for why the close side stays debounced here. */}
				{showTabsTrigger ? (
					<div className="flex w-8 shrink-0 items-center justify-center self-stretch border-l border-border">
						<Button
							aria-label={t("browser.tabsTitle", { count: tabs.length })}
							className="relative"
							onBlur={() => railRef.current?.closeFlyout()}
							onFocus={() => railRef.current?.openFlyout(true)}
							onPointerEnter={() => railRef.current?.openFlyout()}
							onPointerLeave={() => railRef.current?.closeFlyout()}
							size="icon-sm"
							title={t("browser.tabsTitle", { count: tabs.length })}
							type="button"
							variant="ghost"
						>
							<Layers3 aria-hidden="true" className="size-icon-base" />
							<span
								aria-hidden="true"
								className="pointer-events-none absolute -right-0.5 -top-0.5 grid min-w-4 place-items-center rounded-full bg-foreground px-1 font-mono text-[9px] font-semibold leading-4 text-background shadow-sm"
							>
								{tabs.length}
							</span>
						</Button>
					</div>
				) : null}
				{/* Fixed at the rail's own width (w-8) and flush against the panel's
				    right edge (the form has no right padding) so this column lines up
				    with the docked rail directly below it. Popped-out has no icon rail
				    to align with, and gets its own "+" row inside BrowserTabsRail. */}
				{!poppedOut ? (
					<div className="flex w-8 shrink-0 items-center justify-center self-stretch border-l border-border">
						<Button
							aria-label={t("browser.openNewTab")}
							disabled={!canOpenTab}
							onClick={() => void handleOpenTab()}
							size="icon-sm"
							title={t("browser.openNewTab")}
							type="button"
							variant="ghost"
						>
							<Plus aria-hidden="true" className="size-icon-base" />
						</Button>
					</div>
				) : null}
			</form>
			<div className="browser-panel__body flex min-h-0 flex-1 overflow-hidden">
				<div
					className="browser-panel__viewport relative min-h-0 flex-1 overflow-hidden"
					// The live page paints as a separate native WebContentsView, not inside
					// this div. Opening any overlay (e.g. the tabs-rail flyout,
					// BrowserTabsRail.tsx's data-browser-native-overlay) briefly raises the
					// transparent shell above that native view so the overlay can paint on
					// top — if this div painted an opaque background here, it would blank
					// the live page for the duration. `.browser-panel__viewport` in
					// styles.css carries its own plain-CSS background (a decorative
					// gradient for the empty/no-bridge placeholder states) that is NOT a
					// Tailwind utility and so can't be toggled via className — Tailwind
					// utilities live in a lower-priority cascade layer and can never
					// override plain author CSS. Gate that CSS rule with this data
					// attribute instead, so there's exactly one place deciding opacity.
					data-placeholder={!hasNativeBrowser || navState.url === "" ? "true" : undefined}
					data-testid="browser-viewport"
				>
					{/* Only the native-view slot is width-constrained for a device
					    preset — the empty/error placeholders below stay full-width
					    overlays. maxWidth caps it to whatever room the panel actually
					    has instead of overflowing a narrow docked panel. */}
					<div
						className={cn("relative mx-auto h-full", deviceFrameWidth && "border-x border-border shadow-(--shadow-popover)")}
						style={deviceFrameWidth ? { maxWidth: "100%", width: deviceFrameWidth } : undefined}
					>
						<div
							className="browser-panel__slot absolute inset-0 min-h-px min-w-px"
							data-testid="browser-device-frame"
							ref={slotRef}
						/>
					</div>
					{showStaticPreview ? <StaticPreview url={navState.url} /> : null}
					{navState.url === "" ? (
						<div className="pointer-events-none absolute inset-0 grid place-items-center p-5 text-center font-mono text-xs text-passive">
							<p>{t("browser.emptyUrl")}</p>
						</div>
					) : null}
					{navState.error ? (
						<p
							className={cn(
								"absolute inset-x-2.5 bottom-2.5 m-0 border border-error/35 bg-error/8 px-2.5 py-2",
								"rounded-md text-xs text-destructive",
							)}
							data-testid="browser-preview-error"
						>
							{navState.error}
						</p>
					) : null}
				</div>
				{/* Both docked and popped-out keep the rail on the right of the
				    viewport (out of the way of the toolbar/address bar). */}
				<BrowserTabsRail
					activeTabId={activeTabId}
					closedTabs={closedTabs}
					onCloseTab={closeTab}
					onOpenTab={handleOpenTab}
					onPinnedChange={handlePinnedChange}
					onReopenClosedTab={reopenClosedTab}
					onReorderTabs={reorderTabs}
					onSelectTab={handleSelectTab}
					pinned={pinned}
					poppedOut={poppedOut}
					ref={railRef}
					tabs={tabs}
				/>
			</div>
		</div>
	);
}

function agentActivityLabel(activity: BrowserViewModel["agentBrowserActivity"], active: boolean): string {
	if (!active && !activity?.active) return "";
	const action = activity?.active ? activity.action : "";
	if (!action) return appI18n.t("browser.agentUsing");
	return appI18n.t("browser.agentAction", { verb: browserActionVerb(action) });
}

function browserActionVerb(action: string): string {
	const key = ((): MessageKey => {
		switch (action) {
			case "click":
				return "browser.verb.click";
			case "fill":
			case "type":
				return "browser.verb.type";
			case "press":
				return "browser.verb.press";
			case "hover":
				return "browser.verb.hover";
			case "scroll":
				return "browser.verb.scroll";
			case "open":
				return "browser.verb.open";
			case "wait":
				return "browser.verb.wait";
			case "snapshot":
				return "browser.verb.read";
			case "highlight":
				return "browser.verb.highlight";
			case "unhighlight":
				return "browser.verb.clearHighlight";
			case "tab-new":
				return "browser.verb.openTab";
			case "tab-select":
				return "browser.verb.switchTab";
			case "tab-close":
				return "browser.verb.closeTab";
			case "tabs":
				return "browser.verb.checkTabs";
			default:
				return "browser.verb.using";
		}
	})();
	return appI18n.t(key);
}

function StaticPreview({ url }: { url: string }) {
	return (
		<div className="absolute inset-0 overflow-auto bg-background text-foreground">
			<div className="border-b border-border bg-surface px-4 py-3">
				<div className="text-caption font-semibold uppercase tracking-wide-md text-muted-foreground">AO Preview</div>
				<div className="mt-1 truncate font-mono text-xs text-accent">{url}</div>
			</div>
			<div className="mx-auto max-w-preview-max px-5 py-6">
				<div className="rounded-lg border border-border bg-card p-5 shadow-sm">
					<div className="flex items-center justify-between gap-3">
						<div>
							<h1 className="text-heading-lg font-semibold leading-tight tracking-normal text-foreground">
								Demo app preview
							</h1>
							<p className="mt-1 text-control leading-row text-muted-foreground">
								The worker exposed a local Vite app with <span className="font-mono">ao preview</span>.
							</p>
						</div>
						<span className="rounded-md bg-success/15 px-2.5 py-1 text-caption font-semibold text-success">
							Loaded
						</span>
					</div>
					<div className="mt-5 grid grid-cols-3 gap-3">
						{[
							["Routes", "12 passing"],
							["Build", "ready"],
							["Latency", "42 ms"],
						].map(([label, value]) => (
							<div key={label} className="rounded-md border border-border bg-raised p-3">
								<div className="text-caption font-medium uppercase tracking-wide text-muted-foreground">{label}</div>
								<div className="mt-1 text-subtitle font-semibold text-foreground">{value}</div>
							</div>
						))}
					</div>
					<div className="mt-5 rounded-md border border-border bg-terminal p-3 font-mono text-xs leading-row text-terminal-dim">
						<div>$ npm run dev -- --host 127.0.0.1</div>
						<div className="text-success-bright">ready in 418 ms</div>
						<div>Local: http://localhost:5173/</div>
					</div>
				</div>
			</div>
		</div>
	);
}
