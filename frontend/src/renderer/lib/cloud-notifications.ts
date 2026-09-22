import type { CloudCpNotification, CloudCpNotificationEvent } from "./cloud-cp";
import type { CloudNotificationHint } from "./cloud-notification-hints";

export const cloudNotificationsQueryKey = (baseUrl: string, orgId: string, status: "all" | "unread" | "read" = "all") =>
	["cloud-notifications", baseUrl.replace(/\/+$/, ""), orgId, status] as const;

export interface CloudNotificationState {
	sequence: number;
	pending: CloudNotificationHint[];
	durable: CloudCpNotification[];
}

export const emptyCloudNotificationState = (): CloudNotificationState => ({ sequence: 0, pending: [], durable: [] });

// Hints are UX-only. Durable facts, keyed independently from their transport
// event ID, replace them as soon as the control plane confirms processing.
export function addCloudNotificationHint(state: CloudNotificationState, hint: CloudNotificationHint): CloudNotificationState {
	if (state.durable.some((item) => item.eventId === hint.eventId) || state.pending.some((item) => item.eventId === hint.eventId)) return state;
	return { ...state, pending: [...state.pending, hint] };
}

export function applyCloudNotificationEvent(state: CloudNotificationState, event: CloudCpNotificationEvent): CloudNotificationState {
	if (event.sequence <= state.sequence) return state;
	const durable = state.durable.filter((item) => item.id !== event.notification.id);
	durable.unshift(event.notification);
	return {
		sequence: event.sequence,
		durable,
		pending: state.pending.filter((hint) => hint.eventId !== event.notification.eventId),
	};
}
