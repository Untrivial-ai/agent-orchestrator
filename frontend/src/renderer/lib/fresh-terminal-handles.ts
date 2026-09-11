// Only PTYs created by this renderer may answer DSRs in their initial output.
// Consuming the handle on the first connection keeps remounts and reconnects
// conservative; handles discovered after an app restart are replay by default.
const freshTerminalHandles = new Set<string>();

export function markTerminalHandleFresh(handleId: string): void {
	freshTerminalHandles.add(handleId);
}

export function consumeFreshTerminalHandle(handleId: string): boolean {
	return freshTerminalHandles.delete(handleId);
}
