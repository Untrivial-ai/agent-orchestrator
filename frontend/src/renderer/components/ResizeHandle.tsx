import { useLayoutEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";

type WidthConstraint = number | (() => number);

type ResizeHandleProps = React.HTMLAttributes<HTMLDivElement> & {
	side: "left" | "right";
	minWidth?: WidthConstraint;
	maxWidth?: WidthConstraint;
};

type DragState = {
	pointerId: number;
	startClientX: number;
	startWidth: number;
	/** Stable panel edge during drag: right for inspector, left for sidebar. */
	anchor: number;
	borderHalf: number;
	minW: number;
	maxW: number;
	/** +1 sidebar (wider → edge moves right), -1 inspector (wider → edge moves left). */
	widthSign: number;
};

function resolveWidth(value: WidthConstraint | undefined): number | null {
	if (value === undefined) return null;
	const resolved = typeof value === "function" ? value() : value;
	return Number.isFinite(resolved) ? resolved : null;
}

function borderCenterX(el: HTMLElement, edge: "left" | "right"): number {
	const rect = el.getBoundingClientRect();
	const style = getComputedStyle(el);
	if (edge === "left") {
		return rect.left + (Number.parseFloat(style.borderLeftWidth) || 1) / 2;
	}
	return rect.right - (Number.parseFloat(style.borderRightWidth) || 1) / 2;
}

/** Prefer the live CSS max-width when it is tighter than the prop (inspector). */
function effectiveMaxWidth(panel: HTMLElement, propMax: number): number {
	const computed = Number.parseFloat(getComputedStyle(panel).maxWidth);
	if (Number.isFinite(computed) && computed > 0) return Math.min(propMax, computed);
	return propMax;
}

/** Hit strip for sidebar/inspector resize; hover grip sits on the center-pane border. */
export function ResizeHandle({ className, side, minWidth, maxWidth, ...props }: ResizeHandleProps) {
	const hitRef = useRef<HTMLDivElement>(null);
	const gripRef = useRef<HTMLSpanElement>(null);
	const dragRef = useRef<DragState | null>(null);
	const [edgeX, setEdgeX] = useState<number | null>(null);
	const widthSign = side === "right" ? 1 : -1;

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
			if (dragRef.current) return;
			const hitRect = hit.getBoundingClientRect();
			if (hitRect.width < 1 || hitRect.height < 1 || getComputedStyle(hit).display === "none") {
				place(null);
				return;
			}
			const el = borderEl();
			place(el ? borderCenterX(el, "left") : null);
		};

		const beginDrag = (event: PointerEvent) => {
			const panel = panelEl();
			const propMin = resolveWidth(minWidth);
			const propMax = resolveWidth(maxWidth);
			if (!panel || propMin === null || propMax === null) {
				dragRef.current = null;
				return;
			}
			const rect = panel.getBoundingClientRect();
			const style = getComputedStyle(panel);
			const maxW = effectiveMaxWidth(panel, propMax);
			const minW = Math.min(propMin, maxW);
			const startWidth = Math.min(maxW, Math.max(minW, rect.width));
			const borderHalf =
				(side === "left"
					? Number.parseFloat(style.borderLeftWidth)
					: Number.parseFloat(style.borderRightWidth) || Number.parseFloat(style.borderLeftWidth) || 1) / 2;

			dragRef.current = {
				pointerId: event.pointerId,
				startClientX: event.clientX,
				startWidth,
				// Inspector is right-docked (right edge fixed); sidebar is left-docked.
				anchor: side === "left" ? rect.right : rect.left,
				borderHalf: Number.isFinite(borderHalf) ? borderHalf : 0.5,
				minW,
				maxW,
				widthSign,
			};
		};

		const moveDrag = (event: PointerEvent) => {
			const drag = dragRef.current;
			if (!drag || event.pointerId !== drag.pointerId) return;
			if (!document.body.classList.contains("is-resizing-x")) return;

			const rawWidth = drag.startWidth + drag.widthSign * (event.clientX - drag.startClientX);
			const width = Math.min(drag.maxW, Math.max(drag.minW, rawWidth));
			// Place from the docked edge so CSS max-width cannot desync travel distance.
			const x =
				drag.widthSign > 0
					? drag.anchor + width - drag.borderHalf
					: drag.anchor - width + drag.borderHalf;
			if (gripRef.current) gripRef.current.style.left = `${x}px`;
			else setEdgeX(x);
		};

		const endDrag = (event: PointerEvent) => {
			if (dragRef.current && event.pointerId !== dragRef.current.pointerId) return;
			dragRef.current = null;
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

		hit.addEventListener("pointerdown", beginDrag);
		window.addEventListener("resize", sync);
		window.addEventListener("pointermove", moveDrag);
		window.addEventListener("pointerup", endDrag);
		window.addEventListener("pointercancel", endDrag);
		return () => {
			ro.disconnect();
			mo.disconnect();
			hit.removeEventListener("pointerdown", beginDrag);
			window.removeEventListener("resize", sync);
			window.removeEventListener("pointermove", moveDrag);
			window.removeEventListener("pointerup", endDrag);
			window.removeEventListener("pointercancel", endDrag);
		};
	}, [side, minWidth, maxWidth, widthSign]);

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
