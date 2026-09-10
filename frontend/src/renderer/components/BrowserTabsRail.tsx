import {
	forwardRef,
	useCallback,
	useEffect,
	useImperativeHandle,
	useRef,
	useState,
	type CSSProperties,
	type HTMLAttributes,
} from "react";
import { useTranslation } from "react-i18next";
import {
	DndContext,
	KeyboardSensor,
	PointerSensor,
	closestCenter,
	useSensor,
	useSensors,
	type DragEndEvent,
} from "@dnd-kit/core";
import {
	SortableContext,
	sortableKeyboardCoordinates,
	useSortable,
	verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { Globe2, History, Pin, PinOff, Plus, X } from "lucide-react";
import type { BrowserTabState } from "../../main/browser-view-host";
import type { ClosedBrowserTab } from "../hooks/useBrowserView";
import { browserTabLabel } from "../lib/browser-tab-label";
import { reorderBrowserTabs } from "../lib/browser-tab-order";
import { useResizable } from "../hooks/useResizable";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./ui/tooltip";
import { cn } from "../lib/utils";

const RAIL_DEFAULT_WIDTH = 220;
const RAIL_MIN_WIDTH = 180;
const RAIL_MAX_WIDTH = 320;
const HOVER_OPEN_DELAY_MS = 150;
const HOVER_CLOSE_DELAY_MS = 140;

export type BrowserTabsRailHandle = {
	// Lets the toolbar's tab-count trigger (outside the rail) drive the same
	// hover-intent flyout the rail itself uses once docked mode is collapsed to
	// 0px — see the wiring note on the trigger button in BrowserPanel.tsx.
	openFlyout: (immediate?: boolean) => void;
	// Lets the toolbar's "+" button and tab-count trigger (both outside the rail)
	// force the hover flyout closed, the same safeguard rows inside the rail
	// already get — see the comment above handleSelectTab/handleCloseTab.
	closeFlyout: (immediate?: boolean) => void;
};

type BrowserTabsRailProps = {
	tabs: BrowserTabState[];
	activeTabId: string;
	poppedOut: boolean;
	pinned: boolean;
	onPinnedChange: (pinned: boolean) => void;
	onSelectTab: (tabId: string) => Promise<void>;
	onCloseTab: (tabId: string) => Promise<void>;
	onOpenTab: () => Promise<void>;
	onReorderTabs: (orderedIds: string[]) => void;
	closedTabs: ClosedBrowserTab[];
	onReopenClosedTab: (tabId: string) => Promise<void>;
};

export const BrowserTabsRail = forwardRef<BrowserTabsRailHandle, BrowserTabsRailProps>(function BrowserTabsRail(
	{
		tabs,
		activeTabId,
		poppedOut,
		pinned,
		onPinnedChange,
		onSelectTab,
		onCloseTab,
		onOpenTab,
		onReorderTabs,
		closedTabs,
		onReopenClosedTab,
	},
	ref,
) {
	const { t } = useTranslation();
	const [flyoutOpen, setFlyoutOpen] = useState(false);
	const openTimerRef = useRef<number | null>(null);
	const closeTimerRef = useRef<number | null>(null);
	// Popped-out/fullscreen (which has room to spare) is permanently expanded.
	// Docked defaults to collapsed (0px, no reserved column) with tab access via
	// the toolbar's hover trigger; `pinned` restores an always-visible icon rail
	// for users who want one. Both docked and popped-out keep the rail on the
	// right of the viewport (out of the way of the toolbar/address bar).
	const expanded = poppedOut;
	const collapsed = !expanded && !pinned;

	const { onPointerDown: onResizePointerDown, onDoubleClick: onResizeDoubleClick } = useResizable({
		cssVar: "--ao-browser-tabs-w",
		storageKey: "ao-browser-tabs-w",
		defaultWidth: RAIL_DEFAULT_WIDTH,
		min: RAIL_MIN_WIDTH,
		max: RAIL_MAX_WIDTH,
		// The resize handle only ever renders in popped-out mode, which keeps
		// the rail on the right (same side as docked) with the handle on its
		// own left edge — grows when dragged leftward, into the viewport.
		edge: "left",
	});

	const clearOpenTimer = useCallback(() => {
		if (openTimerRef.current === null) return;
		window.clearTimeout(openTimerRef.current);
		openTimerRef.current = null;
	}, []);

	const clearCloseTimer = useCallback(() => {
		if (closeTimerRef.current === null) return;
		window.clearTimeout(closeTimerRef.current);
		closeTimerRef.current = null;
	}, []);

	const openFlyout = useCallback(
		(immediate = false) => {
			if (expanded) return;
			clearCloseTimer();
			if (flyoutOpen || openTimerRef.current !== null) return;
			const apply = () => {
				openTimerRef.current = null;
				setFlyoutOpen(true);
			};
			if (immediate) {
				apply();
				return;
			}
			openTimerRef.current = window.setTimeout(apply, HOVER_OPEN_DELAY_MS);
		},
		[clearCloseTimer, expanded, flyoutOpen],
	);

	const closeFlyout = useCallback(
		(immediate = false) => {
			clearOpenTimer();
			if (!flyoutOpen) return;
			clearCloseTimer();
			const apply = () => {
				closeTimerRef.current = null;
				setFlyoutOpen(false);
			};
			if (immediate) {
				apply();
				return;
			}
			closeTimerRef.current = window.setTimeout(apply, HOVER_CLOSE_DELAY_MS);
		},
		[clearCloseTimer, clearOpenTimer, flyoutOpen],
	);

	useImperativeHandle(
		ref,
		() => ({
			openFlyout: (immediate?: boolean) => openFlyout(immediate),
			closeFlyout: (immediate?: boolean) => closeFlyout(immediate),
		}),
		[closeFlyout, openFlyout],
	);

	// Popping out mid-hover would otherwise leave the flyout mounted open on top
	// of the now-expanded persistent list.
	useEffect(() => {
		if (expanded && flyoutOpen) closeFlyout(true);
	}, [closeFlyout, expanded, flyoutOpen]);

	useEffect(() => () => {
		clearOpenTimer();
		clearCloseTimer();
	}, [clearCloseTimer, clearOpenTimer]);

	// Selecting a tab closes the flyout immediately rather than waiting for the
	// hover-close delay, so the menu doesn't linger open over the newly active
	// tab. The toolbar's new-tab button gets the same treatment via the
	// closeFlyout handle exposed above.
	const handleSelectTab = useCallback(
		(tabId: string) => {
			closeFlyout(true);
			void onSelectTab(tabId);
		},
		[closeFlyout, onSelectTab],
	);

	// Closing a tab does NOT force the flyout shut — the cursor is still over
	// it (that's how the close button got clicked), so the normal
	// onPointerLeave-driven hover-close already handles dismissal once the
	// cursor actually leaves. Force-closing here made the flyout vanish out
	// from under the cursor on every close, active tab or not.
	const handleCloseTab = useCallback(
		(tabId: string) => {
			void onCloseTab(tabId);
		},
		[onCloseTab],
	);

	const handleReopenClosedTab = useCallback(
		(tabId: string) => {
			closeFlyout(true);
			void onReopenClosedTab(tabId);
		},
		[closeFlyout, onReopenClosedTab],
	);

	const sensors = useSensors(
		useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
		useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
	);

	const handleDragEnd = useCallback(
		(event: DragEndEvent) => {
			const { active, over } = event;
			if (!over) return;
			const orderedIds = reorderBrowserTabs(
				tabs.map((tab) => tab.id),
				String(active.id),
				String(over.id),
			);
			if (orderedIds) onReorderTabs(orderedIds);
		},
		[onReorderTabs, tabs],
	);

	const onlyTab = tabs.length === 1;
	const tabIds = tabs.map((tab) => tab.id);
	const closeTitle = t("browser.onlyTab");

	return (
		<div
			className={cn(
				"browser-tabs-rail relative flex h-full shrink-0 flex-col border-border bg-surface",
				expanded || pinned ? "border-l" : "",
				expanded ? "w-(--ao-browser-tabs-w)" : pinned ? "w-8" : "w-0",
			)}
			data-testid="browser-tabs-rail"
			// Only the collapsed (0px) rail opens the flyout on hover. Pinned already
			// renders every tab as a favicon row, so opening the flyout there covered
			// the live page with a duplicate of the list the user is already looking
			// at; each favicon exposes its own adjacent close action instead. The
			// toolbar trigger is likewise hidden while pinned, except when only one
			// tab remains so recently closed tabs stay reachable.
			onBlur={
				collapsed
					? (event) => {
							if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
							closeFlyout();
						}
					: undefined
			}
			onFocus={collapsed ? () => openFlyout(true) : undefined}
			onPointerEnter={collapsed ? () => openFlyout() : undefined}
			onPointerLeave={collapsed ? () => closeFlyout() : undefined}
		>
			{/* Docked keeps "+" in the toolbar (BrowserPanel.tsx) — putting it here
			    too would add a header row this rail's nav doesn't have, breaking the
			    icon-rail/flyout alignment above. Popped-out has no flyout to keep in
			    sync, so it's free to live here instead, full-width like a tab row. */}
			{expanded ? (
				<button
					aria-label={t("browser.openNewTab")}
					className={cn(
						"flex h-8 w-full shrink-0 items-center gap-1.5 border-b border-border p-1.5 text-left text-sm transition-colors",
						"text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
						"disabled:pointer-events-none disabled:opacity-50",
					)}
					onClick={() => void onOpenTab()}
					type="button"
				>
					<Plus aria-hidden="true" className="size-icon-base shrink-0" />
					<span className="truncate">{t("browser.newTab")}</span>
				</button>
			) : pinned ? (
				<Tooltip>
					<TooltipTrigger asChild>
						<button
							aria-label={t("browser.unpinTabs")}
							className={cn(
								"flex h-8 w-full shrink-0 items-center justify-center border-b border-border p-1.5 transition-colors",
								"text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
							)}
							onClick={() => onPinnedChange(false)}
							type="button"
						>
							<PinOff aria-hidden="true" className="size-icon-base" />
						</button>
					</TooltipTrigger>
					<TooltipContent data-browser-native-overlay="true" side="bottom">{t("browser.unpinTabs")}</TooltipContent>
				</Tooltip>
			) : null}
			<nav
				aria-label={t("browser.tabsAria", { count: tabs.length })}
				className={cn(
					"min-h-0 flex-1 overflow-y-auto",
					pinned && !expanded && "w-15 -translate-x-7",
				)}
			>
				{collapsed ? null : (
					<DndContext collisionDetection={closestCenter} onDragEnd={handleDragEnd} sensors={sensors}>
						<SortableContext items={tabIds} strategy={verticalListSortingStrategy}>
							{/* No app-wide TooltipProvider exists (each site mounts its own),
							    so the pinned rail's favicon tooltips need one here. */}
							<TooltipProvider>
							<div className="flex flex-col">
								{tabs.map((tab) =>
									expanded ? (
										<SortableExpandedTabRow
											active={tab.id === activeTabId}
											closeTitle={closeTitle}
											key={tab.id}
											onClose={() => handleCloseTab(tab.id)}
											onSelect={() => handleSelectTab(tab.id)}
											onlyTab={onlyTab}
											tab={tab}
										/>
									) : (
										<SortableIconTabRow
											active={tab.id === activeTabId}
											closeTitle={closeTitle}
											key={tab.id}
											onClose={() => handleCloseTab(tab.id)}
											onSelect={() => handleSelectTab(tab.id)}
											onlyTab={onlyTab}
											tab={tab}
										/>
									),
								)}
							</div>
							</TooltipProvider>
						</SortableContext>
					</DndContext>
				)}
				{expanded ? <ClosedTabsSection closedTabs={closedTabs} onReopen={handleReopenClosedTab} /> : null}
			</nav>
			{expanded ? (
				<div
					className="absolute inset-y-0 left-0 w-1.5 cursor-col-resize touch-none"
					data-testid="browser-tabs-resize-handle"
					onDoubleClick={onResizeDoubleClick}
					onPointerDown={onResizePointerDown}
				/>
			) : null}
			{/* Kept mounted (not conditionally rendered) so `data-state` alone drives
			    visibility: the OPEN_BROWSER_OVERLAY_SELECTOR MutationObserver in
			    useBrowserView.ts watches `[data-browser-native-overlay="true"]
			    [data-state="open"]` anywhere in the document, so toggling this
			    attribute reorders the native browser view behind this flyout with no
			    changes needed there. Positioned relative to this same rail root that
			    `<nav>` fills top-to-bottom, so the two row lists start at the exact
			    same y with no separate spacer. */}
			{!expanded ? (
				<div
					className={cn(
						"absolute inset-y-0 right-full z-chrome w-56 overflow-hidden border border-border bg-card shadow-(--shadow-popover)",
						"transition-[opacity,transform] duration-[220ms] ease-[cubic-bezier(0.16,1,0.3,1)]",
						"data-[state=closed]:pointer-events-none data-[state=closed]:opacity-0 data-[state=closed]:-translate-x-3",
						"data-[state=open]:opacity-100 data-[state=open]:translate-x-0",
					)}
					data-browser-native-overlay="true"
					data-state={flyoutOpen ? "open" : "closed"}
					data-testid="browser-tabs-flyout"
					onPointerEnter={pinned ? clearCloseTimer : undefined}
					onPointerLeave={pinned ? () => closeFlyout() : undefined}
					role="menu"
				>
					<div className="flex h-full flex-col overflow-y-auto">
						{/* Always rendered, in both states, so this list has the exact same
						    row count as the rail it sits beside when pinned (which always
						    carries its own header row — the unpin button above `<nav>`).
						    Rendering it only while unpinned left the flyout one row
						    shorter than the pinned rail, so every row below it landed a
						    row height out of sync between the two lists. */}
						<button
							className={cn(
								"flex h-8 w-full shrink-0 items-center gap-1.5 border-b border-border p-1.5 text-left text-sm transition-colors",
								"text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
							)}
							onClick={() => {
								if (pinned) {
									onPinnedChange(false);
								} else {
									onPinnedChange(true);
									closeFlyout(true);
								}
							}}
							type="button"
						>
							{pinned ? (
								<PinOff aria-hidden="true" className="size-icon-base shrink-0" />
							) : (
								<Pin aria-hidden="true" className="size-icon-base shrink-0" />
							)}
							<span className="truncate">{pinned ? t("browser.tabsPinned") : t("browser.pinTabs")}</span>
						</button>
						{tabs.map((tab) => (
							<ExpandedTabRow
								active={tab.id === activeTabId}
								closeTitle={closeTitle}
								key={tab.id}
								onClose={() => handleCloseTab(tab.id)}
								onSelect={() => handleSelectTab(tab.id)}
								onlyTab={onlyTab}
								tab={tab}
							/>
						))}
						<ClosedTabsSection closedTabs={closedTabs} onReopen={handleReopenClosedTab} />
					</div>
				</div>
			) : null}
		</div>
	);
});

type RowChrome = {
	setNodeRef?: (node: HTMLElement | null) => void;
	style?: CSSProperties;
	dragProps?: HTMLAttributes<HTMLElement>;
};

type TabRowProps = {
	tab: BrowserTabState;
	active: boolean;
	onlyTab: boolean;
	onSelect: () => void;
	onClose: () => void;
	closeTitle: string;
	chrome?: RowChrome;
};

function TabFavicon({ className, tab }: { className: string; tab: BrowserTabState }) {
	// object-cover guarantees containment regardless of the source image's own
	// aspect ratio (some sites' favicons aren't perfectly square), so it can
	// never spill past its box into the row next to it.
	if (tab.favicon) return <img alt="" className={cn("shrink-0 object-cover", className)} src={tab.favicon} />;
	return <Globe2 aria-hidden="true" className={cn("shrink-0 text-passive", className)} />;
}

// Only rendered where there's room for a label (expanded rail, docked flyout)
// — the icon-only pinned rail has no space for a second, distinct row style,
// and closed tabs are inherently distinct from "click to switch to this open
// tab," so they don't belong crammed into the icon list.
function ClosedTabsSection({
	closedTabs,
	onReopen,
}: {
	closedTabs: ClosedBrowserTab[];
	onReopen: (tabId: string) => void;
}) {
	const { t } = useTranslation();
	if (closedTabs.length === 0) return null;
	return (
		<div className="border-t border-border">
			<div className="flex h-6 shrink-0 items-center px-1.5 font-mono text-[10px] font-semibold uppercase tracking-wide text-passive">
				{t("browser.recentlyClosed")}
			</div>
			{closedTabs.map((tab) => {
				const label = browserTabLabel(tab.title, tab.url);
				const reopenLabel = t("browser.reopenTab", { title: label.title });
				return (
					<button
						aria-label={reopenLabel}
						className={cn(
							"flex h-8 w-full items-center gap-1.5 p-1.5 text-left text-sm text-muted-foreground opacity-70 transition-[opacity,background-color,color]",
							"hover:bg-interactive-hover hover:text-foreground hover:opacity-100",
							"disabled:pointer-events-none disabled:opacity-40",
						)}
						key={tab.id}
						onClick={() => onReopen(tab.id)}
						title={reopenLabel}
						type="button"
					>
						{tab.favicon ? (
							<img alt="" className="size-icon-base shrink-0 object-cover" src={tab.favicon} />
						) : (
							<History aria-hidden="true" className="size-icon-base shrink-0" />
						)}
						<span className="min-w-0 flex-1 truncate">{label.title}</span>
					</button>
				);
			})}
		</div>
	);
}

function IconTabRow({ active, chrome, closeTitle, onClose, onSelect, onlyTab, tab }: TabRowProps) {
	const { t } = useTranslation();
	const [hovered, setHovered] = useState(false);
	const [focused, setFocused] = useState(false);
	const label = browserTabLabel(tab.title, tab.url);
	const closeLabel = t("browser.closeTab", { title: label.title });
	const closeVisible = hovered || focused;
	return (
		<div
			className="group/tab-icon flex h-8 w-full items-center"
			onBlurCapture={(event) => {
				if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
				setFocused(false);
			}}
			onFocusCapture={() => setFocused(true)}
			onPointerEnter={() => setHovered(true)}
			onPointerLeave={() => setHovered(false)}
			ref={chrome?.setNodeRef}
			style={chrome?.style}
		>
			<button
				aria-current={active ? "true" : undefined}
				aria-label={`${label.title} — ${label.subtitle}`}
				className={cn(
					"order-2 flex h-full w-8 shrink-0 items-center justify-center p-1.5 transition-colors",
					"hover:bg-interactive-hover",
					active && "bg-interactive-active",
				)}
				onClick={onSelect}
				type="button"
				{...chrome?.dragProps}
			>
				<TabFavicon className="size-icon-base" tab={tab} />
			</button>
			<button
				aria-label={closeLabel}
				className={cn(
					"pointer-events-none order-1 grid size-7 shrink-0 scale-90 place-items-center rounded-md",
					"bg-interactive-hover text-muted-foreground opacity-0",
					"transition-[opacity,transform,background-color,color] duration-150 ease-out",
					"hover:bg-interactive-active hover:text-foreground active:scale-95",
					"group-hover/tab-icon:pointer-events-auto group-hover/tab-icon:scale-100 group-hover/tab-icon:opacity-100",
					"group-focus-within/tab-icon:pointer-events-auto group-focus-within/tab-icon:scale-100 group-focus-within/tab-icon:opacity-100",
					"focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-accent/60",
					"disabled:pointer-events-none",
				)}
				data-browser-native-overlay="true"
				data-state={closeVisible ? "open" : "closed"}
				disabled={onlyTab}
				onClick={(event) => {
					event.stopPropagation();
					onClose();
				}}
				title={onlyTab ? closeTitle : closeLabel}
				type="button"
			>
				<X aria-hidden="true" className="size-icon-sm" />
			</button>
		</div>
	);
}

function ExpandedTabRow({ active, chrome, closeTitle, onClose, onSelect, onlyTab, tab }: TabRowProps) {
	const { t } = useTranslation();
	const label = browserTabLabel(tab.title, tab.url);
	const closeLabel = t("browser.closeTab", { title: label.title });
	return (
		<div
			className={cn(
				"group/tab-row flex h-8 w-full items-center overflow-hidden transition-colors",
				"hover:bg-interactive-hover",
				active && "bg-interactive-active",
			)}
			ref={chrome?.setNodeRef}
			style={chrome?.style}
			{...chrome?.dragProps}
		>
			<button
				className="flex min-w-0 flex-1 items-center gap-1.5 p-1.5 text-left outline-hidden"
				onClick={onSelect}
				type="button"
			>
				<TabFavicon className="size-icon-base shrink-0" tab={tab} />
				<span
					className={cn(
						"min-w-0 flex-1 truncate text-sm",
						active ? "text-foreground" : "text-muted-foreground group-hover/tab-row:text-foreground",
					)}
				>
					{label.title}
				</span>
			</button>
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="inline-flex">
						<button
							aria-label={closeLabel}
							className={cn(
								"mr-1.5 grid size-control-sm shrink-0 place-items-center overflow-hidden rounded-sm text-passive opacity-0",
								"transition-[opacity,color] hover:text-foreground",
								"group-hover/tab-row:opacity-100 group-focus-within/tab-row:opacity-100",
								"disabled:pointer-events-none",
							)}
							disabled={onlyTab}
							onClick={(event) => {
								event.stopPropagation();
								onClose();
							}}
							type="button"
						>
							<X aria-hidden="true" className="size-icon-sm" />
						</button>
					</span>
				</TooltipTrigger>
				{/* Expanded rows render in the shell-owned rail beside the native page,
				    so their styled tooltip does not need to restack the shell. */}
				<TooltipContent side="bottom">{onlyTab ? closeTitle : closeLabel}</TooltipContent>
			</Tooltip>
		</div>
	);
}

function SortableIconTabRow(props: TabRowProps) {
	const { attributes, listeners, setNodeRef, transform, transition } = useSortable({ id: props.tab.id });
	const style: CSSProperties = { transform: CSS.Transform.toString(transform), transition };
	return <IconTabRow {...props} chrome={{ dragProps: { ...attributes, ...listeners }, setNodeRef, style }} />;
}

function SortableExpandedTabRow(props: TabRowProps) {
	const { attributes, listeners, setNodeRef, transform, transition } = useSortable({ id: props.tab.id });
	const style: CSSProperties = { transform: CSS.Transform.toString(transform), transition };
	return <ExpandedTabRow {...props} chrome={{ dragProps: { ...attributes, ...listeners }, setNodeRef, style }} />;
}
