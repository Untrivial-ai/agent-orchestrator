import { useCallback, useLayoutEffect, useRef } from "react";

type ResizableConstraint = number | (() => number);

interface UseResizableOptions {
	/** CSS custom property to drive, e.g. "--ao-sidebar-w". */
	cssVar: string;
	/**
	 * Limits custom-property invalidation to the elements that consume the
	 * width. Defaults to :root for callers that do not provide local targets.
	 */
	getCssTargets?: () => ReadonlyArray<HTMLElement | null>;
	/** localStorage key to persist the width. */
	storageKey: string;
	defaultWidth: number;
	min: ResizableConstraint;
	max: ResizableConstraint;
	/**
	 * Which edge the drag handle sits on relative to the panel it resizes.
	 * "right" (sidebar handle) grows with rightward drag; "left" (inspector
	 * handle) grows with leftward drag.
	 */
	edge: "left" | "right";
	/** Called once when a collapsed rail drag should reopen the owner. */
	onExpand?: () => void;
	/** Optional one-time floor when restoring a saved width for a new panel profile. */
	restoreMin?: number;
	/** Pointer movement needed before a collapsed rail drag expands. */
	expandDragThreshold?: number;
}

/**
 * Pointer-driven panel resize, cloned from agent-orchestrator's useResizable.
 * Persists the width to localStorage and applies it via a CSS custom property
 * to the nearest consuming layout elements. Keeping a high-frequency custom
 * property off :root avoids invalidating unrelated renderer subtrees.
 *
 * Inspector clamp contracts (do not regress):
 * - `apply` MUST also respect each target's computed CSS `max-width` (e.g.
 *   `--session-inspector-max-width`). Prop/`rangeRef` max alone is not enough —
 *   the var can grow past the painted panel and desync ResizeHandle.
 * - On pointerdown, seed `widthRef` from the painted box when it disagrees with
 *   the custom property (same CSS max-width desync).
 * - Dragging never auto-collapses: clamp at `min`; collapse stays on explicit UI.
 */
export function useResizable({
	cssVar,
	getCssTargets,
	storageKey,
	defaultWidth,
	min,
	max,
	edge,
	onExpand,
	restoreMin,
	expandDragThreshold = 8,
}: UseResizableOptions) {
	const widthRef = useRef(defaultWidth);
	const frameRef = useRef<number | null>(null);
	const pendingWidthRef = useRef<number | null>(null);
	const appliedTargetsRef = useRef(new Set<HTMLElement>());
	const activeDragCleanupRef = useRef<(() => void) | null>(null);
	const minValue = useCallback(() => (typeof min === "function" ? min() : min), [min]);
	const maxValue = useCallback(() => (typeof max === "function" ? max() : max), [max]);
	const cssTargets = useCallback(
		() =>
			getCssTargets
				? getCssTargets().filter((target): target is HTMLElement => target !== null)
				: [document.documentElement],
		[getCssTargets],
	);

	const apply = useCallback(
		(next: number) => {
			// Prop max ∩ computed max-width — required for inspector CSS cap.
			let max = maxValue();
			for (const target of cssTargets()) {
				const computedMax = Number.parseFloat(getComputedStyle(target).maxWidth);
				if (Number.isFinite(computedMax) && computedMax > 0) {
					max = Math.min(max, computedMax);
				}
			}
			const clamped = Math.min(max, Math.max(minValue(), next));
			widthRef.current = clamped;
			for (const target of cssTargets()) {
				target.style.setProperty(cssVar, `${clamped}px`);
				appliedTargetsRef.current.add(target);
			}
		},
		[cssTargets, cssVar, maxValue, minValue],
	);

	const applyOnFrame = useCallback(
		(next: number) => {
			pendingWidthRef.current = next;
			if (frameRef.current !== null) return;
			frameRef.current = window.requestAnimationFrame(() => {
				frameRef.current = null;
				const pending = pendingWidthRef.current;
				pendingWidthRef.current = null;
				if (pending !== null) apply(pending);
			});
		},
		[apply],
	);

	const flushPending = useCallback(() => {
		if (frameRef.current !== null) {
			window.cancelAnimationFrame(frameRef.current);
			frameRef.current = null;
		}
		const pending = pendingWidthRef.current;
		pendingWidthRef.current = null;
		if (pending !== null) apply(pending);
	}, [apply]);

	// Restore persisted width before first paint so the sidebar does not appear
	// at the default width on reload and then jump/animate to the stored width.
	useLayoutEffect(() => {
		const saved = Number(window.localStorage.getItem(storageKey));
		const restored = Number.isFinite(saved) && saved > 0 ? saved : defaultWidth;
		apply(restoreMin === undefined ? restored : Math.max(restoreMin, restored));
		return () => {
			activeDragCleanupRef.current?.();
			if (frameRef.current !== null) window.cancelAnimationFrame(frameRef.current);
			for (const target of appliedTargetsRef.current) target.style.removeProperty(cssVar);
			appliedTargetsRef.current.clear();
		};
	}, [apply, cssVar, defaultWidth, restoreMin, storageKey]);

	const onPointerDown = useCallback(
		(event: React.PointerEvent<HTMLElement>) => {
			activeDragCleanupRef.current?.();
			event.preventDefault();
			const pointerId = event.pointerId;
			const captureTarget = event.currentTarget;
			captureTarget.setPointerCapture?.(pointerId);
			const startX = event.clientX;
			// Seed from the painted box when CSS max-width holds width below the var.
			const visualWidth = cssTargets()
				.map((target) => target.getBoundingClientRect().width)
				.find((width) => width > 0);
			if (visualWidth !== undefined && Math.abs(visualWidth - widthRef.current) > 0.5) {
				apply(visualWidth);
			}
			const startWidth = Math.min(maxValue(), Math.max(minValue(), widthRef.current));
			const sign = edge === "right" ? 1 : -1;
			document.body.classList.add("is-resizing-x");

			const finish = () => {
				window.removeEventListener("pointermove", onMove);
				window.removeEventListener("pointerup", onEnd);
				window.removeEventListener("pointercancel", onEnd);
				window.removeEventListener("blur", finish);
				flushPending();
				document.body.classList.remove("is-resizing-x");
				if (captureTarget.hasPointerCapture?.(pointerId)) captureTarget.releasePointerCapture(pointerId);
				window.localStorage.setItem(storageKey, String(widthRef.current));
				if (activeDragCleanupRef.current === finish) activeDragCleanupRef.current = null;
			};
			const onEnd = (e: PointerEvent) => {
				if (e.pointerId === pointerId) finish();
			};
			// Dragging never collapses the panel: `apply` clamps at `min`, so the
			// drag simply stops at the floor. Collapse stays on explicit controls.
			const onMove = (e: PointerEvent) => {
				applyOnFrame(startWidth + sign * (e.clientX - startX));
			};
			window.addEventListener("pointermove", onMove);
			window.addEventListener("pointerup", onEnd);
			window.addEventListener("pointercancel", onEnd);
			window.addEventListener("blur", finish);
			activeDragCleanupRef.current = finish;
		},
		[apply, applyOnFrame, cssTargets, edge, flushPending, maxValue, minValue, storageKey],
	);

	const onCollapsedPointerDown = useCallback(
		(event: React.PointerEvent<HTMLElement>) => {
			activeDragCleanupRef.current?.();
			const pointerId = event.pointerId;
			const captureTarget = event.currentTarget;
			captureTarget.setPointerCapture?.(pointerId);
			const startX = event.clientX;
			const sign = edge === "right" ? 1 : -1;
			let expanded = false;
			document.body.classList.add("is-resizing-x");

			const finish = () => {
				window.removeEventListener("pointermove", onMove);
				window.removeEventListener("pointerup", onEnd);
				window.removeEventListener("pointercancel", onEnd);
				window.removeEventListener("blur", finish);
				flushPending();
				document.body.classList.remove("is-resizing-x");
				if (captureTarget.hasPointerCapture?.(pointerId)) captureTarget.releasePointerCapture(pointerId);
				if (expanded) window.localStorage.setItem(storageKey, String(widthRef.current));
				if (activeDragCleanupRef.current === finish) activeDragCleanupRef.current = null;
			};
			const onEnd = (e: PointerEvent) => {
				if (e.pointerId === pointerId) finish();
			};
			const onMove = (e: PointerEvent) => {
				const delta = sign * (e.clientX - startX);
				if (delta < expandDragThreshold) return;
				if (!expanded) {
					expanded = true;
					onExpand?.();
				}
				applyOnFrame(minValue() + delta);
			};
			window.addEventListener("pointermove", onMove);
			window.addEventListener("pointerup", onEnd);
			window.addEventListener("pointercancel", onEnd);
			window.addEventListener("blur", finish);
			activeDragCleanupRef.current = finish;
		},
		[applyOnFrame, edge, expandDragThreshold, flushPending, minValue, onExpand, storageKey],
	);

	/** Double-click the handle to reset to the default width. */
	const onDoubleClick = useCallback(() => {
		apply(defaultWidth);
		window.localStorage.setItem(storageKey, String(defaultWidth));
	}, [apply, defaultWidth, storageKey]);

	return { onPointerDown, onCollapsedPointerDown, onDoubleClick };
}
