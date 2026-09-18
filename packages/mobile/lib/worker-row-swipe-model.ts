export const WORKER_ACTION_REVEAL_WIDTH = 128;

const OPEN_VELOCITY = -500;

/** Keeps the live row translation directly under the user's finger. */
export function boundWorkerActionTranslation(translationX: number): number {
	return Math.max(-WORKER_ACTION_REVEAL_WIDTH, Math.min(0, translationX));
}

export function resolveWorkerActionRail({
	translationX,
	velocityX,
}: {
	translationX: number;
	velocityX: number;
}) {
	"worklet";
	const crossedOpenThreshold = translationX <= -WORKER_ACTION_REVEAL_WIDTH / 2;
	const flickedOpen = velocityX <= OPEN_VELOCITY;
	const flickedClosed = velocityX > 0;
	return (crossedOpenThreshold && !flickedClosed) || flickedOpen ? -WORKER_ACTION_REVEAL_WIDTH : 0;
}
