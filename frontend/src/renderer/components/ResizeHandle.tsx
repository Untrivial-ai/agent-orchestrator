import { useLayoutEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";

type ResizeHandleProps = React.HTMLAttributes<HTMLDivElement> & {
	side: "left" | "right";
};

function borderCenterX(el: HTMLElement, edge: "left" | "right"): number {
	const rect = el.getBoundingClientRect();
	const style = getComputedStyle(el);
	if (edge === "left") {
		return rect.left + (Number.parseFloat(style.borderLeftWidth) || 1) / 2;
	}
	return rect.right - (Number.parseFloat(style.borderRightWidth) || 1) / 2;
}

/** Hit strip for sidebar/inspector resize; hover grip sits on the center-pane border. */
export function ResizeHandle({ className, side, ...props }: ResizeHandleProps) {
	const hitRef = useRef<HTMLDivElement>(null);
	const gripRef = useRef<HTMLSpanElement>(null);
	const [edgeX, setEdgeX] = useState<number | null>(null);

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

			if (side === "right") {
				const surface = document.querySelector<HTMLElement>(".center-panel-surface");
				place(surface ? borderCenterX(surface, "left") : null);
				return;
			}

			const inspector = hit.closest<HTMLElement>("[data-slot='inspector-container']");
			place(inspector ? borderCenterX(inspector, "left") : null);
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

		// Follow the pointer 1:1 while dragging (no rAF — width paint is already
		// rAF-batched in useResizable; an extra frame would desync the grip).
		const onPointerMove = (event: PointerEvent) => {
			if (!document.body.classList.contains("is-resizing-x")) return;
			if (gripRef.current) gripRef.current.style.left = `${event.clientX}px`;
			else setEdgeX(event.clientX);
		};
		const onPointerUp = () => sync();

		window.addEventListener("resize", sync);
		window.addEventListener("pointermove", onPointerMove);
		window.addEventListener("pointerup", onPointerUp);
		window.addEventListener("pointercancel", onPointerUp);
		return () => {
			ro.disconnect();
			mo.disconnect();
			window.removeEventListener("resize", sync);
			window.removeEventListener("pointermove", onPointerMove);
			window.removeEventListener("pointerup", onPointerUp);
			window.removeEventListener("pointercancel", onPointerUp);
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
