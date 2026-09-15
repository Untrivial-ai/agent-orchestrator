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

			if (side === "right") {
				const surface = document.querySelector<HTMLElement>(".center-panel-surface");
				setEdgeX(surface ? borderCenterX(surface, "left") : null);
				return;
			}

			const inspector = hit.closest<HTMLElement>("[data-slot='inspector-container']");
			setEdgeX(inspector ? borderCenterX(inspector, "left") : null);
		};

		sync();

		const ro = new ResizeObserver(sync);
		ro.observe(hit);
		hit.parentElement && ro.observe(hit.parentElement);
		const surface = document.querySelector(".center-panel-surface");
		surface && ro.observe(surface);
		const inspector = hit.closest("[data-slot='inspector-container']");
		inspector && ro.observe(inspector);

		const mo = new MutationObserver(sync);
		mo.observe(hit, { attributes: true, attributeFilter: ["class", "hidden"] });
		const sidebar = hit.closest("[data-slot='sidebar']");
		sidebar && mo.observe(sidebar, { attributes: true, attributeFilter: ["data-state", "data-collapsible"] });
		inspector && mo.observe(inspector, { attributes: true, attributeFilter: ["data-state", "hidden", "class"] });

		window.addEventListener("resize", sync);
		return () => {
			ro.disconnect();
			mo.disconnect();
			window.removeEventListener("resize", sync);
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
					style={{ top: "50%", left: edgeX, transform: "translate(-50%, -50%)" }}
				/>
			) : null}
		</div>
	);
}
