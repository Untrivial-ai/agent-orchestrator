import { useCallback, useRef, useState, type CSSProperties, type ReactNode, type RefObject } from "react";
import { motion, useReducedMotion } from "motion/react";
import { useResizable } from "../hooks/useResizable";
import { INSPECTOR_SEPARATOR_RESERVE_PX, inspectorMaxWidthPx } from "../lib/inspector-width";
import { SHELL_PANEL_SPRING } from "../lib/motion-spring";
import { isMacPlatform } from "../lib/platform";
import type { InspectorView } from "../stores/ui-store";
import { ResizeHandle } from "./ResizeHandle";

const inspectorWidthVar = "--ao-inspector-w";
const noDragStyle = isMacPlatform() ? ({ WebkitAppRegion: "no-drag" } as CSSProperties) : undefined;
export const INSPECTOR_SPRING_MS = 300;
export const INSPECTOR_SPRING_EASING = "linear(0, 0.333 12.5%, 0.642 25%, 0.813 37.5%, 0.902 50%, 0.949 62.5%, 0.974 75%, 0.986 87.5%, 1)";

export type InspectorSizing = {
	chatMinWidth: number;
	defaultWidth: number;
	minWidth: number;
	maxPercent: number;
	mode: "utility" | "browser" | "files";
	storageKey: string;
};

export function inspectorSizing(view: InspectorView): InspectorSizing {
	if (view === "browser") return {
		chatMinWidth: 440,
		defaultWidth: 900,
		minWidth: 460,
		maxPercent: 68,
		mode: "browser",
		storageKey: "ao.workspace.browser.canvasWidthPx",
	};
	return {
		chatMinWidth: 560,
		defaultWidth: 500,
		minWidth: view === "files" ? 460 : 340,
		maxPercent: 55,
		mode: view === "files" ? "files" : "utility",
		storageKey: "ao.inspector.widthPx",
	};
}

export function initialInspectorSize(sizing: InspectorSizing, availableWidth?: number): string {
	const raw = typeof window === "undefined" ? null : window.localStorage?.getItem(sizing.storageKey);
	const parsed = raw === null ? Number.NaN : Number(raw);
	const requestedWidth = Number.isFinite(parsed)
		? Math.max(sizing.minWidth, Math.round(parsed))
		: sizing.defaultWidth;
	const maxWidth = inspectorMaxWidthPx(availableWidth, sizing.maxPercent, sizing.chatMinWidth);
	return maxWidth === undefined ? `${requestedWidth}px` : `${Math.min(requestedWidth, maxWidth)}px`;
}

export function sizingGeometryEqual(a: InspectorSizing, b: InspectorSizing): boolean {
	return a.chatMinWidth === b.chatMinWidth && a.defaultWidth === b.defaultWidth &&
		a.minWidth === b.minWidth && a.maxPercent === b.maxPercent && a.storageKey === b.storageKey;
}

// One inspector layout for local, cloud, and remote sessions. The host only
// changes where the inspector's data comes from, not how its rail behaves.
export function SessionInspectorRail({
	sessionKey,
	showCollapsedHandle = true,
	children,
	isOpen,
	onExpand,
	onCloseAnimationComplete,
	restoreMinWidth,
	sizing,
	settledClosed,
	splitRef,
}: {
	sessionKey: string;
	showCollapsedHandle?: boolean;
	children: ReactNode;
	isOpen: boolean;
	onExpand: () => void;
	onCloseAnimationComplete?: () => void;
	restoreMinWidth?: number;
	sizing: InspectorSizing;
	settledClosed: boolean;
	splitRef: RefObject<HTMLDivElement | null>;
}) {
	const prefersReducedMotion = useReducedMotion();
	const gapRef = useRef<HTMLDivElement>(null);
	const panelRef = useRef<HTMLDivElement>(null);
	const minWidth = useCallback(() => {
		const split = splitRef.current;
		if (!split || split.clientWidth <= 0) return sizing.minWidth;
		const available = Math.max(0, split.clientWidth - INSPECTOR_SEPARATOR_RESERVE_PX);
		const max = inspectorMaxWidthPx(available, sizing.maxPercent, sizing.chatMinWidth) ?? sizing.defaultWidth;
		return Math.min(sizing.minWidth, max);
	}, [sizing.chatMinWidth, sizing.defaultWidth, sizing.maxPercent, sizing.minWidth, splitRef]);
	const maxWidth = useCallback(() => {
		const split = splitRef.current;
		if (!split || split.clientWidth <= 0) return Number.POSITIVE_INFINITY;
		const available = Math.max(0, split.clientWidth - INSPECTOR_SEPARATOR_RESERVE_PX);
		return inspectorMaxWidthPx(available, sizing.maxPercent, sizing.chatMinWidth) ?? sizing.defaultWidth;
	}, [sizing.chatMinWidth, sizing.defaultWidth, sizing.maxPercent, splitRef]);
	const getResizeTargets = useCallback(() => [gapRef.current, panelRef.current], []);
	const getBorderElement = useCallback(() => panelRef.current, []);
	const { onPointerDown, onCollapsedPointerDown, onDoubleClick } = useResizable({
		cssVar: inspectorWidthVar,
		getCssTargets: getResizeTargets,
		storageKey: sizing.storageKey,
		defaultWidth: sizing.defaultWidth,
		min: minWidth,
		max: maxWidth,
		edge: "left",
		onExpand,
		restoreMin: restoreMinWidth,
		// Restore the preferred width after a narrow window or zoom level widens again.
		reclampOnWindowResize: true,
	});
	// Restore a destination session's layout immediately. Only subsequent open/
	// close changes within that session should animate; keep the content mounted.
	const [motionState, setMotionState] = useState({ sessionKey, isOpen, animate: false });
	if (motionState.sessionKey !== sessionKey || motionState.isOpen !== isOpen) {
		setMotionState({ sessionKey, isOpen, animate: motionState.sessionKey === sessionKey });
	}
	const transition = prefersReducedMotion || !motionState.animate ? { duration: 0 } : SHELL_PANEL_SPRING;
	const hidden = !isOpen && settledClosed;
	const handleAnimationComplete = useCallback(() => {
		if (!isOpen) onCloseAnimationComplete?.();
	}, [isOpen, onCloseAnimationComplete]);

	return <>
		<motion.div aria-hidden="true" className="relative max-w-(--session-inspector-max-width) shrink-0" data-slot="inspector-gap" initial={false} ref={gapRef} animate={{ width: isOpen ? `var(${inspectorWidthVar}, ${sizing.defaultWidth}px)` : 0 }} transition={transition} />
		<motion.div
			aria-hidden={hidden}
			className="absolute inset-y-0 right-0 z-chrome flex h-full max-w-(--session-inspector-max-width) flex-col overflow-hidden border-l border-border-strong bg-background"
			data-panel=""
			data-settled={settledClosed ? "true" : "false"}
			data-slot="inspector-container"
			data-state={isOpen ? "expanded" : "collapsed"}
			data-workspace-mode={sizing.mode}
			data-testid="panel-inspector"
			hidden={hidden}
			id="inspector"
			inert={hidden}
			initial={false}
			animate={{ x: isOpen ? "0%" : "100%" }}
			onAnimationComplete={handleAnimationComplete}
			ref={panelRef}
			style={{ width: `var(${inspectorWidthVar}, ${sizing.defaultWidth}px)` }}
			transition={transition}
		>
			<ResizeHandle className={!isOpen ? "hidden" : undefined} data-testid="inspector-resize-handle" getBorderElement={getBorderElement} getObserveElements={getResizeTargets} onDoubleClick={onDoubleClick} onPointerDown={onPointerDown} side="left" style={noDragStyle} />
			<div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">{children}</div>
		</motion.div>
		{isOpen || !showCollapsedHandle ? null : <div className="absolute inset-y-0 right-0 z-chrome w-2 cursor-e-resize touch-none" data-slot="inspector-collapsed-rail" data-testid="inspector-collapsed-rail" onPointerDown={onCollapsedPointerDown} style={noDragStyle} />}
	</>;
}
