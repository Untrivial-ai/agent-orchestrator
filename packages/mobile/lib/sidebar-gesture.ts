const EDGE_ACTIVATION_WIDTH = 28;
const DIRECTION_LOCK_DISTANCE = 8;
const OPEN_PROGRESS_THRESHOLD = 0.5;
const FLICK_VELOCITY = 0.55;

type SidebarGesture = {
	open: boolean;
	startX: number;
	dx: number;
	dy: number;
	edgeWidth?: number;
};

export function shouldCaptureSidebarGesture({ open, startX, dx, dy, edgeWidth = EDGE_ACTIVATION_WIDTH }: SidebarGesture): boolean {
	if (Math.abs(dx) < DIRECTION_LOCK_DISTANCE || Math.abs(dx) <= Math.abs(dy)) return false;
	if (open) return dx < 0;
	return startX <= edgeWidth && dx > 0;
}

export function sidebarGestureTarget({
	open,
	dx,
	velocityX,
	drawerWidth,
}: {
	open: boolean;
	dx: number;
	velocityX: number;
	drawerWidth: number;
}): boolean {
	if (velocityX >= FLICK_VELOCITY) return true;
	if (velocityX <= -FLICK_VELOCITY) return false;
	const progress = Math.max(0, Math.min(1, (open ? drawerWidth + dx : dx) / drawerWidth));
	return progress >= OPEN_PROGRESS_THRESHOLD;
}
