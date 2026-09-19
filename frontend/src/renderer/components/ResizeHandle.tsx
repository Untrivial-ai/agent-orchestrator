/**
 * Shared sidebar/inspector resize hit-strip + grip.
 *
 * DO NOT regress these design contracts (home-page-ui / shell polish):
 * - Grip is a fixed, hover/active-only pill on the CENTER-PANE border (sidebar:
 *   sidebar-container right edge ≈ center surface left; inspector: panel
 *   `border-l`). Not inset into the panel, not a CSS `::after`, not always-visible.
 * - Height is 80vh, vertically centered — not full inset-y / not titlebar-tall.
 * - useResizable is the ONLY width integrator. This handle only paints the grip
 *   on the live border — never a second drag/clamp state machine.
 * - Callers MUST pass `getBorderElement` (scoped to their panel), not global
 *   document.querySelector, so nested/preview shells cannot pick the wrong node.
 * - Ancestor refs attach after descendant layout effects — getters live in refs and
 *   we re-bind observers once the border node appears (inspector close/reopen).
 * - Drag follow is scoped to *this* handle (not every `is-resizing-x` peer).
 */
import { useLayoutEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";

type ResizeHandleProps = React.HTMLAttributes<HTMLDivElement> & {
	side: "left" | "right";
	/** Panel/surface whose border the grip sits on. */
	getBorderElement: () => HTMLElement | null;
	/** Extra nodes to ResizeObserver (gap, container, etc.). */
	getObserveElements?: () => ReadonlyArray<HTMLElement | null>;
};

function borderCenterX(el: HTMLElement, edge: "left" | "right"): number {
	const rect = el.getBoundingClientRect();
	const style = getComputedStyle(el);
	if (edge === "left") {
		return rect.left + (Number.parseFloat(style.borderLeftWidth) || 1) / 2;
	}
	return rect.right - (Number.parseFloat(style.borderRightWidth) || 1) / 2;
}

export function ResizeHandle({
	className,
	side,
	getBorderElement,
	getObserveElements,
	onPointerDown,
	...props
}: ResizeHandleProps) {
	const hitRef = useRef<HTMLDivElement>(null);
	const gripRef = useRef<HTMLSpanElement>(null);
	const getBorderElementRef = useRef(getBorderElement);
	const getObserveElementsRef = useRef(getObserveElements);
	const draggingRef = useRef(false);
	getBorderElementRef.current = getBorderElement;
	getObserveElementsRef.current = getObserveElements;
	const [edgeX, setEdgeX] = useState<number | null>(null);
	// Sidebar handle paints on the panel's right edge; inspector on its left.
	const borderEdge: "left" | "right" = side === "right" ? "right" : "left";

	useLayoutEffect(() => {
		const hit = hitRef.current;
		if (!hit) return;

		const place = (x: number | null) => {
			setEdgeX(x);
			if (gripRef.current && x !== null) gripRef.current.style.left = `${x}px`;
		};

		const sync = () => {
			const hitRect = hit.getBoundingClientRect();
			if (hitRect.width < 1 || hitRect.height < 1 || getComputedStyle(hit).display === "none") {
				place(null);
				return;
			}
			const el = getBorderElementRef.current();
			place(el ? borderCenterX(el, borderEdge) : null);
		};

		// Only this handle follows during its own drag — not peer `is-resizing-x`.
		let dragMoveAttached = false;
		const onDragMove = () => {
			if (!draggingRef.current) return;
			sync();
		};
		const attachDragMove = () => {
			if (dragMoveAttached) return;
			dragMoveAttached = true;
			window.addEventListener("pointermove", onDragMove);
		};
		const detachDragMove = () => {
			if (!dragMoveAttached) return;
			dragMoveAttached = false;
			window.removeEventListener("pointermove", onDragMove);
			sync();
		};
		const endLocalDrag = () => {
			draggingRef.current = false;
			detachDragMove();
		};
		const onBodyClass = () => {
			if (!document.body.classList.contains("is-resizing-x")) endLocalDrag();
			else if (draggingRef.current) attachDragMove();
		};

		const observed = new Set<Element>();
		const ro = new ResizeObserver(sync);
		const observe = (el: Element | null | undefined) => {
			if (!el || observed.has(el)) return;
			observed.add(el);
			ro.observe(el);
		};
		const refreshObserved = () => {
			observe(hit);
			if (hit.parentElement) observe(hit.parentElement);
			for (const el of getObserveElementsRef.current?.() ?? []) observe(el);
			observe(getBorderElementRef.current());
		};

		// Idle-only style MO — during drag, pointermove owns sync so the width
		// write does not force a second layout read per move.
		let borderMoTarget: HTMLElement | null = null;
		const mo = new MutationObserver(() => {
			if (draggingRef.current) return;
			refreshObserved();
			const border = getBorderElementRef.current();
			if (border !== borderMoTarget) bindBorderMo();
			sync();
		});
		const bindBorderMo = () => {
			const border = getBorderElementRef.current();
			mo.disconnect();
			borderMoTarget = border;
			mo.observe(hit, { attributes: true, attributeFilter: ["class", "hidden"] });
			if (border) {
				mo.observe(border, {
					attributes: true,
					attributeFilter: ["data-state", "hidden", "class", "style"],
				});
			}
		};

		refreshObserved();
		bindBorderMo();
		sync();

		// Ancestor refs attach after this descendant layout effect — retry until
		// the border node exists (inspector mount / close+reopen).
		let borderPollRaf = 0;
		let borderPollAttempts = 0;
		const pollForBorder = () => {
			borderPollRaf = 0;
			refreshObserved();
			bindBorderMo();
			sync();
			if (!getBorderElementRef.current() && borderPollAttempts++ < 120) {
				borderPollRaf = requestAnimationFrame(pollForBorder);
			}
		};
		if (!getBorderElementRef.current()) {
			borderPollRaf = requestAnimationFrame(pollForBorder);
		}

		const bodyMo = new MutationObserver(onBodyClass);
		bodyMo.observe(document.body, { attributes: true, attributeFilter: ["class"] });
		onBodyClass();

		window.addEventListener("resize", sync);
		return () => {
			if (borderPollRaf) cancelAnimationFrame(borderPollRaf);
			ro.disconnect();
			mo.disconnect();
			bodyMo.disconnect();
			endLocalDrag();
			window.removeEventListener("resize", sync);
		};
	}, [borderEdge]);

	return (
		<div
			ref={hitRef}
			data-side={side}
			data-slot="resize-handle"
			data-testid="resize-handle"
			className={cn(
				"group/resize absolute inset-y-0 z-[5] w-[length:var(--size-resize-handle)] cursor-col-resize touch-none",
				side === "right" && "right-[calc(-1*var(--size-resize-handle-offset))]",
				side === "left" && "left-[calc(-1*var(--size-resize-handle-offset))]",
				className,
			)}
			onPointerDown={(event) => {
				// Mark local drag before useResizable adds `is-resizing-x`.
				draggingRef.current = true;
				onPointerDown?.(event);
			}}
			{...props}
		>
			{edgeX !== null ? (
				<span
					ref={gripRef}
					aria-hidden="true"
					data-resize-grip=""
					// Fixed + 80vh + hover opacity only. Do not restore always-on / inset-y /
					// ::after grips — those were rejected for this shell polish.
					className="pointer-events-none fixed z-[6] h-[80vh] w-0.5 rounded-full bg-foreground/20 opacity-0 transition-opacity duration-fast group-hover/resize:opacity-100 group-active/resize:opacity-100 motion-reduce:transition-none"
					style={{ top: "50%", left: edgeX, transform: "translate(-50%, -50%)" }}
				/>
			) : null}
		</div>
	);
}
