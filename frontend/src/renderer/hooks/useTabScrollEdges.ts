import { useCallback, useEffect, useRef, useState } from "react";

type ScrollBehavior = ScrollToOptions["behavior"];

// Tracks whether a horizontal tab strip has hidden content on either edge.
// The small tolerance avoids flickering from fractional layout measurements.
export function useTabScrollEdges(dependencies: readonly unknown[] = []) {
	const scrollRef = useRef<HTMLDivElement | null>(null);
	const [edges, setEdges] = useState({ left: false, right: false });
	const [scrollingToEnd, setScrollingToEnd] = useState(false);
	const updateEdges = useCallback(() => {
		const element = scrollRef.current;
		if (!element) return;
		const overflow = element.scrollWidth - element.clientWidth;
		const next = {
			left: overflow > 1 && element.scrollLeft > 1,
			right: overflow > 1 && element.scrollLeft < overflow - 1,
		};
		setEdges((current) => (current.left === next.left && current.right === next.right ? current : next));
		if (!next.right) setScrollingToEnd(false);
	}, []);
	const scrollToEnd = useCallback((behavior: ScrollBehavior = "smooth") => {
		const element = scrollRef.current;
		if (!element) return;
		setScrollingToEnd(true);
		element.scrollTo({ left: element.scrollWidth, behavior });
		window.requestAnimationFrame(updateEdges);
	}, [updateEdges]);

	useEffect(() => {
		const element = scrollRef.current;
		if (!element) return;
		updateEdges();
		element.addEventListener("scroll", updateEdges, { passive: true });
		const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(updateEdges);
		observer?.observe(element);
		return () => {
			element.removeEventListener("scroll", updateEdges);
			observer?.disconnect();
		};
	}, [updateEdges, ...dependencies]);

	return {
		scrollRef,
		scrollToEnd,
		showLeftFade: edges.left,
		showRightFade: edges.right && !scrollingToEnd,
		updateEdges,
	};
}
