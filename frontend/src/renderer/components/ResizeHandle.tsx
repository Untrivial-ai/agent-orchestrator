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
	...props
}: ResizeHandleProps) {
	const hitRef = useRef<HTMLDivElement>(null);
	const gripRef = useRef<HTMLSpanElement>(null);
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
			const el = getBorderElement();
			place(el ? borderCenterX(el, borderEdge) : null);
		};

		// While useResizable is dragging, follow the painted border every move
		// (no local clamp math — the hook owns width).
		let dragMoveAttached = false;
		const onDragMove = () => {
			if (!document.body.classList.contains("is-resizing-x")) return;
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
		const onBodyClass = () => {
			if (document.body.classList.contains("is-resizing-x")) attachDragMove();
			else detachDragMove();
		};

		sync();

		const ro = new ResizeObserver(sync);
		ro.observe(hit);
		if (hit.parentElement) ro.observe(hit.parentElement);
		const refreshObserved = () => {
			for (const el of getObserveElements?.() ?? []) {
				if (el) ro.observe(el);
			}
			const border = getBorderElement();
			if (border) ro.observe(border);
		};
		refreshObserved();

		const mo = new MutationObserver(sync);
		mo.observe(hit, { attributes: true, attributeFilter: ["class", "hidden"] });
		const border = getBorderElement();
		if (border) {
			mo.observe(border, { attributes: true, attributeFilter: ["data-state", "hidden", "class", "style"] });
		}
		const bodyMo = new MutationObserver(onBodyClass);
		bodyMo.observe(document.body, { attributes: true, attributeFilter: ["class"] });
		onBodyClass();

		window.addEventListener("resize", sync);
		return () => {
			ro.disconnect();
			mo.disconnect();
			bodyMo.disconnect();
			detachDragMove();
			window.removeEventListener("resize", sync);
		};
	}, [borderEdge, getBorderElement, getObserveElements]);

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
