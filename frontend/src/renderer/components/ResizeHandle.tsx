import { useLayoutEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";

type ResizeHandleProps = React.HTMLAttributes<HTMLDivElement> & {
	side: "left" | "right";
};

/** Midpoint of an element's border stroke on the given side (viewport X). */
function borderCenterX(el: HTMLElement, edge: "left" | "right"): number {
	const rect = el.getBoundingClientRect();
	const style = getComputedStyle(el);
	if (edge === "left") {
		const width = Number.parseFloat(style.borderLeftWidth) || 1;
		return rect.left + width / 2;
	}
	const width = Number.parseFloat(style.borderRightWidth) || 1;
	return rect.right - width / 2;
}

/**
 * Shared column-resize control for the left sidebar and right inspector.
 *
 * Hit strip stays on the panel edge for dragging. The grip is a fixed pill at
 * the viewport vertical center, horizontally centered on the center pane's
 * visible border (left surface edge / inspector split), not inset into the rail.
 */
export function ResizeHandle({ className, side, ...props }: ResizeHandleProps) {
	const hitRef = useRef<HTMLDivElement>(null);
	const [edgeX, setEdgeX] = useState<number | null>(null);

	useLayoutEffect(() => {
		const hit = hitRef.current;
		if (!hit) return;

		const sync = () => {
			const hitRect = hit.getBoundingClientRect();
			if (hitRect.width < 1 || hitRect.height < 1 || getComputedStyle(hit).display === "none") {
				setEdgeX(null);
				return;
			}

			// Left-sidebar handle (side=right): sit on the center pane's left border.
			// Inspector handle (side=left): sit on the inspector's left border (the
			// split rule inside the center pane).
			if (side === "right") {
				const surface = document.querySelector<HTMLElement>(".center-panel-surface");
				if (!surface) {
					setEdgeX(null);
					return;
				}
				setEdgeX(borderCenterX(surface, "left"));
				return;
			}

			const inspector = hit.closest<HTMLElement>("[data-slot='inspector-container']");
			if (!inspector) {
				setEdgeX(null);
				return;
			}
			setEdgeX(borderCenterX(inspector, "left"));
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
		mo.observe(hit, { attributes: true, attributeFilter: ["class", "hidden", "style"] });
		const sidebar = hit.closest("[data-slot='sidebar']");
		if (sidebar) mo.observe(sidebar, { attributes: true, attributeFilter: ["data-state", "data-collapsible"] });
		if (inspector) mo.observe(inspector, { attributes: true, attributeFilter: ["data-state", "hidden", "class"] });

		window.addEventListener("resize", sync);

		let raf = 0;
		const onPointerMove = () => {
			if (!document.body.classList.contains("is-resizing-x")) return;
			cancelAnimationFrame(raf);
			raf = requestAnimationFrame(sync);
		};
		window.addEventListener("pointermove", onPointerMove);

		return () => {
			ro.disconnect();
			mo.disconnect();
			window.removeEventListener("resize", sync);
			window.removeEventListener("pointermove", onPointerMove);
			cancelAnimationFrame(raf);
		};
	}, [side]);

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
					aria-hidden="true"
					data-resize-grip=""
					className="pointer-events-none fixed z-[6] h-[80vh] w-0.5 rounded-full bg-foreground/20 opacity-0 transition-opacity duration-fast group-hover/resize:opacity-100 group-active/resize:opacity-100 motion-reduce:transition-none"
					style={{
						top: "50%",
						left: edgeX,
						transform: "translate(-50%, -50%)",
					}}
				/>
			) : null}
		</div>
	);
}
