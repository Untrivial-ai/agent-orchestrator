import { useLayoutEffect, useRef, useState, type ReactNode } from "react";

export function SelectionActionToolbar({ anchorX, top, children }: {
	anchorX: number;
	top: number;
	children: ReactNode;
}) {
	const toolbar = useRef<HTMLDivElement>(null);
	const [left, setLeft] = useState(8);

	useLayoutEffect(() => {
		const element = toolbar.current;
		const pane = element?.parentElement;
		if (!element || !pane) return;
		const position = () => {
			const width = element.getBoundingClientRect().width;
			const paneWidth = pane.getBoundingClientRect().width;
			setLeft(Math.max(8, Math.min(anchorX - width / 2, paneWidth - width - 8)));
		};
		position();
		if (typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(position);
		observer.observe(element);
		observer.observe(pane);
		return () => observer.disconnect();
	}, [anchorX]);

	return <div
		ref={toolbar}
		data-testid="selection-action-toolbar"
		style={{ left, top }}
		onMouseDown={(event) => event.preventDefault()}
		className="absolute z-50 inline-flex w-max max-w-[calc(100%-16px)] flex-nowrap overflow-x-auto whitespace-nowrap rounded-md border border-border-strong bg-popover text-xs font-medium text-popover-foreground shadow-lg"
	>{children}</div>;
}
