import { useCallback, useEffect, useRef, type PointerEvent as ReactPointerEvent, type RefObject } from "react";

type PointerPosition = { clientX: number; clientY: number };

/**
 * Pierre rebuilds its line DOM when a live workspace snapshot refreshes. That
 * rebuild clears its internal hovered-line state even when the mouse has not
 * moved, which removes the slotted AO feedback button. Replay the last real
 * pointer position after a render so the original control stays attached to
 * the line still under the pointer.
 */
export function usePersistentGutterUtility(containerRef: RefObject<HTMLElement | null>) {
	const pointerRef = useRef<PointerPosition | null>(null);
	const frameRef = useRef<number | null>(null);

	const onPointerMove = useCallback((event: ReactPointerEvent<HTMLElement>) => {
		pointerRef.current = { clientX: event.clientX, clientY: event.clientY };
	}, []);
	const onPointerLeave = useCallback(() => {
		pointerRef.current = null;
		if (frameRef.current !== null) window.cancelAnimationFrame(frameRef.current);
		frameRef.current = null;
	}, []);
	const restoreAfterRender = useCallback(() => {
		if (frameRef.current !== null) window.cancelAnimationFrame(frameRef.current);
		frameRef.current = window.requestAnimationFrame(() => {
			frameRef.current = null;
			const pointer = pointerRef.current;
			const container = containerRef.current;
			if (!pointer || !container || typeof window.PointerEvent === "undefined") return;
			const surfaceTarget = document.elementFromPoint(pointer.clientX, pointer.clientY);
			if (!(surfaceTarget instanceof Element) || !container.contains(surfaceTarget)) return;
			let target = surfaceTarget;
			while (target.shadowRoot) {
				const shadowTarget = target.shadowRoot.elementFromPoint(pointer.clientX, pointer.clientY);
				if (!(shadowTarget instanceof Element) || shadowTarget === target) break;
				target = shadowTarget;
			}
			target.dispatchEvent(new window.PointerEvent("pointermove", {
				bubbles: true,
				clientX: pointer.clientX,
				clientY: pointer.clientY,
				composed: true,
				pointerType: "mouse",
			}));
		});
	}, [containerRef]);

	useEffect(() => () => {
		if (frameRef.current !== null) window.cancelAnimationFrame(frameRef.current);
	}, []);

	return { onPointerLeave, onPointerMove, restoreAfterRender };
}
