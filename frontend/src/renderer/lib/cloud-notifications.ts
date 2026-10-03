// The cloud notification list is server-authoritative (paginated items, unread
// count, and event sequence all computed by the control plane), so the renderer
// keys a single react-query cache off the notification endpoint and refreshes it
// from durable events and pre-durable hints rather than reconstructing that state
// client-side. The base URL is normalized here so a trailing slash can never make
// an invalidation key miss the query key.
export const cloudNotificationsQueryKey = (baseUrl: string, orgId: string, userId: string, status: "all" | "unread" | "read" = "all") =>
	["cloud-notifications", baseUrl.replace(/\/+$/, ""), orgId, userId, status] as const;
