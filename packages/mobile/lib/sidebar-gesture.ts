const EDGE_ACTIVATION_WIDTH = 28;
const DIRECTION_LOCK_DISTANCE = 8;
const OPEN_PROGRESS_THRESHOLD = 0.5;
const FLICK_VELOCITY = 0.55;

type SidebarGesture = {
	open: boolean;
	settling?: boolean;
	startX: number;
	dx: number;
	dy: number;
	edgeWidth?: number;
};

export function shouldCaptureSidebarGesture({ open, settling = false, startX, dx, dy, edgeWidth = EDGE_ACTIVATION_WIDTH }: SidebarGesture): boolean {
	if (Math.abs(dx) < DIRECTION_LOCK_DISTANCE || Math.abs(dx) <= Math.abs(dy)) return false;
	if (settling) return true;
	if (open) return dx < 0;
	return startX <= edgeWidth && dx > 0;
}

export function sidebarGestureProgress({
	startProgress,
	dx,
	drawerWidth,
}: {
	startProgress: number;
	dx: number;
	drawerWidth: number;
}): number {
	return Math.max(0, Math.min(1, startProgress + dx / drawerWidth));
}

export function sidebarGestureTarget({
	open,
	startProgress,
	dx,
	velocityX,
	drawerWidth,
}: {
	open: boolean;
	startProgress?: number;
	dx: number;
	velocityX: number;
	drawerWidth: number;
}): boolean {
	if (velocityX >= FLICK_VELOCITY) return true;
	if (velocityX <= -FLICK_VELOCITY) return false;
	const progress = sidebarGestureProgress({
		startProgress: startProgress ?? (open ? 1 : 0),
		dx,
		drawerWidth,
	});
	return progress >= OPEN_PROGRESS_THRESHOLD;
}
