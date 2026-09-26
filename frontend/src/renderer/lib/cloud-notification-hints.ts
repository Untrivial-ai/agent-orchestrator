// Ephemeral hints arrive over the existing cloud terminal WebSocket before the
// control plane's durable notification processor has committed its inbox row.
// They are intentionally process-local: REST/SSE remains the recovery source
// after a reconnect, and consumers must dedupe by eventId when that row lands.
export interface CloudNotificationHint {
	source: "cloud";
	eventId: string;
	type: string;
	occurredAt: string;
	payload: Record<string, unknown>;
}

type Listener = (hint: CloudNotificationHint) => void;

const listeners = new Set<Listener>();

export function publishCloudNotificationHint(hint: CloudNotificationHint): void {
	listeners.forEach((listener) => listener(hint));
}

export function subscribeCloudNotificationHints(listener: Listener): () => void {
	listeners.add(listener);
	return () => listeners.delete(listener);
}
