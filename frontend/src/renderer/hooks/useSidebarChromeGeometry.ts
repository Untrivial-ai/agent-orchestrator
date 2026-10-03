import { useLayoutEffect, type RefObject } from "react";
import { isMacPlatform } from "../lib/platform";

/** Share the sidebar's actual animated layout with the fixed native chrome. */
export function useSidebarChromeGeometry(ready: boolean, gapRef: RefObject<HTMLDivElement | null>, containerRef: RefObject<HTMLDivElement | null>): void {
	useLayoutEffect(() => {
		if (!ready || !isMacPlatform()) return;
		const gap = gapRef.current;
		const container = containerRef.current;
		if (!gap || !container) return;
		const style = document.documentElement.style;
		const names = ["--ao-sidebar-layout-width", "--ao-sidebar-collapse-progress"];
		const previous = names.map((name) => style.getPropertyValue(name));
		const update = () => {
			const width = gap.getBoundingClientRect().width;
			const expandedWidth = container.getBoundingClientRect().width;
			const progress = expandedWidth > 0 ? Math.max(0, Math.min(1, 1 - width / expandedWidth)) : 1;
			style.setProperty(names[0], `${width}px`);
			style.setProperty(names[1], String(progress));
		};
		update();
		const observer = new ResizeObserver(update);
		observer.observe(gap);
		observer.observe(container);
		return () => {
			observer.disconnect();
			names.forEach((name, i) => {
				if (previous[i]) style.setProperty(name, previous[i]);
				else style.removeProperty(name);
			});
		};
	}, [ready, gapRef, containerRef]);
}
