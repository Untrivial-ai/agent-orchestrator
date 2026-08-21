import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import {
	ArrowLeft,
	ArrowRight,
	Bug,
	Globe2,
	Layers3,
	Maximize2,
	Minimize2,
	MousePointer2,
	Plus,
	RefreshCw,
	X,
} from "lucide-react";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { useBrowserView, type BrowserViewModel } from "../hooks/useBrowserView";
import { formatBrowserAnnotationMessage, type BrowserAnnotationSubmitPayload } from "../../shared/browser-annotations";
import { MAX_BROWSER_TABS } from "../../shared/browser-tabs";
import { isAgentActivityWorking } from "../lib/session-presentation";
import type { WorkspaceSession } from "../types/workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { BrowserTabsRail, type BrowserTabsRailHandle } from "./BrowserTabsRail";
import { cn } from "../lib/utils";
import { appI18n, type MessageKey } from "../i18n";

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

// A queued annotation waits for the agent to leave "active" before it's
// delivered, so it doesn't collide with a turn the agent is still mid-way
// through. This is a bounded safety valve, not the primary wakeup signal (the
// agentWorking prop flipping false is) — it guarantees delivery even if the
// activity signal never arrives, since an earlier version of this gate got
// permanently stuck on stale status data (#2606).
const ANNOTATION_BUSY_RETRY_MS = 20_000;

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
	agentWorking,
}: {
	sessionId?: string;
	navUrl?: string;
	agentWorking?: boolean;
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
	const agentWorkingRef = useRef(Boolean(agentWorking));
	const busyRetryTimerRef = useRef<number | null>(null);

	const clearBusyRetryTimer = useCallback(() => {
		if (busyRetryTimerRef.current !== null) window.clearTimeout(busyRetryTimerRef.current);
		busyRetryTimerRef.current = null;
	}, []);

	const resetQueue = useCallback(() => {
		generationRef.current += 1;
		if (sentTimerRef.current !== null) window.clearTimeout(sentTimerRef.current);
		sentTimerRef.current = null;
		clearBusyRetryTimer();
		annotationQueueRef.current = [];
		annotationSendingRef.current = false;
		setState({ status: "idle", error: "", queuedCount: 0 });
	}, [clearBusyRetryTimer]);

	const drainAnnotationQueue = useCallback(
		(bypassBusyGate = false) => {
			if (annotationSendingRef.current || !sessionIdRef.current) {
				return;
			}

			const payload = annotationQueueRef.current[0];
			if (!payload) return;

			if (agentWorkingRef.current && !bypassBusyGate) {
				setState((current) => ({ ...current, status: "queued", queuedCount: annotationQueueRef.current.length }));
				if (busyRetryTimerRef.current === null) {
					busyRetryTimerRef.current = window.setTimeout(() => {
						busyRetryTimerRef.current = null;
						drainAnnotationQueue(true);
					}, ANNOTATION_BUSY_RETRY_MS);
				}
				return;
			}
			clearBusyRetryTimer();

			annotationQueueRef.current.shift();
			setState((current) => ({ ...current, queuedCount: annotationQueueRef.current.length }));

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
		},
		[clearBusyRetryTimer],
	);

	useEffect(() => {
		sessionIdRef.current = sessionId ?? "";
		resetQueue();
	}, [resetQueue, sessionId]);

	useEffect(() => {
		if (navUrl) return;
		resetQueue();
	}, [navUrl, resetQueue]);

	useEffect(() => {
		agentWorkingRef.current = Boolean(agentWorking);
		if (!agentWorking) drainAnnotationQueue();
	}, [agentWorking, drainAnnotationQueue]);

	useEffect(
		() => () => {
			if (sentTimerRef.current !== null) window.clearTimeout(sentTimerRef.current);
			clearBusyRetryTimer();
		},
		[clearBusyRetryTimer],
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
		agentWorking: isAgentActivityWorking(session.activity),
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
					<div className="browser-panel__slot absolute inset-0 min-h-px min-w-px" ref={slotRef} />
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
