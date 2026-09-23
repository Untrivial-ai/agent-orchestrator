type Schedule = (callback: () => void) => () => void;

const nextFrame: Schedule = (callback) => {
	const frame = requestAnimationFrame(callback);
	return () => cancelAnimationFrame(frame);
};

/**
 * Native-stack keeps the same navigation item when a route swaps its React
 * renderer. Clear that item for one frame so iOS measures the replacement's
 * intrinsic width instead of clipping it to the previous renderer's width.
 */
export function resetHeaderRightForSwap(
	clearHeaderRight: () => void,
	onReady: () => void,
	schedule: Schedule = nextFrame,
): () => void {
	let active = true;
	clearHeaderRight();
	const cancel = schedule(() => {
		if (active) onReady();
	});
	return () => {
		active = false;
		cancel();
		clearHeaderRight();
	};
}

export function deferRouteContent(
	onReady: () => void,
	schedule: Schedule,
): () => void {
	let active = true;
	const cancel = schedule(() => {
		if (active) onReady();
	});
	return () => {
		active = false;
		cancel();
	};
}
