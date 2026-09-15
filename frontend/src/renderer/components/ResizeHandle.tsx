import { useLayoutEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";

type WidthConstraint = number | (() => number);

type ResizeHandleProps = React.HTMLAttributes<HTMLDivElement> & {
	side: "left" | "right";
	/** Panel width floor — grip stops here while dragging. */
	minWidth?: WidthConstraint;
	/** Panel width ceiling — grip stops here while dragging. */
	maxWidth?: WidthConstraint;
};

function resolveWidth(value: WidthConstraint | undefined): number | null {
	if (value === undefined) return null;
	return typeof value === "function" ? value() : value;
}

function borderCenterX(el: HTMLElement, edge: "left" | "right"): number {
	const rect = el.getBoundingClientRect();
	const style = getComputedStyle(el);
	if (edge === "left") {
		return rect.left + (Number.parseFloat(style.borderLeftWidth) || 1) / 2;
	}
	return rect.right - (Number.parseFloat(style.borderRightWidth) || 1) / 2;
}

/** Hit strip for sidebar/inspector resize; hover grip sits on the center-pane border. */
export function ResizeHandle({ className, side, minWidth, maxWidth, ...props }: ResizeHandleProps) {
	const hitRef = useRef<HTMLDivElement>(null);
	const gripRef = useRef<HTMLSpanElement>(null);
	const dragClampRef = useRef<{ min: number; max: number } | null>(null);
	const [edgeX, setEdgeX] = useState<number | null>(null);

	useLayoutEffect(() => {
		const hit = hitRef.current;
		if (!hit) return;

		const place = (x: number | null) => {
			setEdgeX(x);
			if (gripRef.current && x !== null) gripRef.current.style.left = `${x}px`;
		};

		const borderEl = (): HTMLElement | null => {
			if (side === "right") return document.querySelector<HTMLElement>(".center-panel-surface");
			return hit.closest<HTMLElement>("[data-slot='inspector-container']");
		};

		const panelEl = (): HTMLElement | null => {
			if (side === "right") return document.querySelector<HTMLElement>("[data-slot='sidebar-container']");
			return hit.closest<HTMLElement>("[data-slot='inspector-container']");
		};

		const sync = () => {
			const hitRect = hit.getBoundingClientRect();
			if (hitRect.width < 1 || hitRect.height < 1 || getComputedStyle(hit).display === "none") {
				place(null);
				return;
			}
			const el = borderEl();
			place(el ? borderCenterX(el, "left") : null);
		};

		const beginDragClamp = () => {
			const el = borderEl();
			const panel = panelEl();
			const minW = resolveWidth(minWidth);
			const maxW = resolveWidth(maxWidth);
			if (!el || !panel || minW === null || maxW === null) {
				dragClampRef.current = null;
				return;
			}
			const startEdge = borderCenterX(el, "left");
			const startWidth = panel.getBoundingClientRect().width;
			// Wider sidebar moves the center-pane left border right; wider inspector
			// moves its left border left.
			const sign = side === "right" ? 1 : -1;
			const atMin = startEdge + sign * (minW - startWidth);
			const atMax = startEdge + sign * (maxW - startWidth);
			dragClampRef.current = { min: Math.min(atMin, atMax), max: Math.max(atMin, atMax) };
		};

		const endDragClamp = () => {
			dragClampRef.current = null;
			sync();
		};

		sync();

		const ro = new ResizeObserver(sync);
		ro.observe(hit);
		if (hit.parentElement) ro.observe(hit.parentElement);
		const surface = document.querySelector(".center-panel-surface");
		if (surface) ro.observe(surface);
		const inspector = hit.closest("[data-slot='inspector-container']");
		if (inspector) ro.observe(inspector);

		const mo = new MutationObserver(sync);
		mo.observe(hit, { attributes: true, attributeFilter: ["class", "hidden"] });
		const sidebar = hit.closest("[data-slot='sidebar']");
		if (sidebar) mo.observe(sidebar, { attributes: true, attributeFilter: ["data-state", "data-collapsible"] });
		if (inspector) mo.observe(inspector, { attributes: true, attributeFilter: ["data-state", "hidden", "class"] });

		const onPointerDown = () => beginDragClamp();
		const onPointerMove = (event: PointerEvent) => {
			if (!document.body.classList.contains("is-resizing-x")) return;
			const clamp = dragClampRef.current;
			const x = clamp ? Math.min(clamp.max, Math.max(clamp.min, event.clientX)) : event.clientX;
			if (gripRef.current) gripRef.current.style.left = `${x}px`;
			else setEdgeX(x);
		};

		hit.addEventListener("pointerdown", onPointerDown);
		window.addEventListener("resize", sync);
		window.addEventListener("pointermove", onPointerMove);
		window.addEventListener("pointerup", endDragClamp);
		window.addEventListener("pointercancel", endDragClamp);
		return () => {
			ro.disconnect();
			mo.disconnect();
			hit.removeEventListener("pointerdown", onPointerDown);
			window.removeEventListener("resize", sync);
			window.removeEventListener("pointermove", onPointerMove);
			window.removeEventListener("pointerup", endDragClamp);
			window.removeEventListener("pointercancel", endDragClamp);
		};
	}, [side, minWidth, maxWidth]);

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
					className="pointer-events-none fixed z-[6] h-[80vh] w-0.5 rounded-full bg-foreground/20 opacity-0 transition-opacity duration-fast group-hover/resize:opacity-100 group-active/resize:opacity-100 motion-reduce:transition-none"
					style={{ top: "50%", left: edgeX, transform: "translate(-50%, -50%)" }}
				/>
			) : null}
		</div>
	);
}
