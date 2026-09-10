import { useCallback, useLayoutEffect, useRef } from "react";

type ResizableConstraint = number | (() => number);

interface UseResizableOptions {
	/** CSS custom property to drive (set on :root), e.g. "--ao-sidebar-w". */
	cssVar: string;
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
 * on :root (so the consuming layout reads it with `width: var(--cssVar, default)`),
 * avoiding any inline `style=`.
 */
export function useResizable({
	cssVar,
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
	const minValue = useCallback(() => (typeof min === "function" ? min() : min), [min]);
	const maxValue = useCallback(() => (typeof max === "function" ? max() : max), [max]);

	const apply = useCallback(
		(next: number) => {
			const clamped = Math.min(maxValue(), Math.max(minValue(), next));
			widthRef.current = clamped;
			document.documentElement.style.setProperty(cssVar, `${clamped}px`);
		},
		[cssVar, maxValue, minValue],
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
			if (frameRef.current !== null) window.cancelAnimationFrame(frameRef.current);
			document.documentElement.style.removeProperty(cssVar);
		};
	}, [apply, cssVar, defaultWidth, restoreMin, storageKey]);

	const onPointerDown = useCallback(
		(event: React.PointerEvent<HTMLElement>) => {
			event.preventDefault();
			const startX = event.clientX;
			const startWidth = Math.min(maxValue(), Math.max(minValue(), widthRef.current));
			const sign = edge === "right" ? 1 : -1;
			document.body.classList.add("is-resizing-x");

			const onUp = () => {
				window.removeEventListener("pointermove", onMove);
				window.removeEventListener("pointerup", onUp);
				flushPending();
				document.body.classList.remove("is-resizing-x");
				window.localStorage.setItem(storageKey, String(widthRef.current));
			};
			// Dragging never collapses the panel: `apply` clamps at `min`, so the
			// drag simply stops at the floor. Collapse stays on explicit controls.
			const onMove = (e: PointerEvent) => {
				applyOnFrame(startWidth + sign * (e.clientX - startX));
			};
			window.addEventListener("pointermove", onMove);
			window.addEventListener("pointerup", onUp);
		},
		[applyOnFrame, edge, flushPending, maxValue, minValue, storageKey],
	);

	const onCollapsedPointerDown = useCallback(
		(event: React.PointerEvent<HTMLElement>) => {
			const startX = event.clientX;
			const sign = edge === "right" ? 1 : -1;
			let expanded = false;
			document.body.classList.add("is-resizing-x");

			const onUp = () => {
				window.removeEventListener("pointermove", onMove);
				window.removeEventListener("pointerup", onUp);
				flushPending();
				document.body.classList.remove("is-resizing-x");
				if (expanded) window.localStorage.setItem(storageKey, String(widthRef.current));
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
			window.addEventListener("pointerup", onUp);
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
