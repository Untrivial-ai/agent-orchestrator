import { useCloudNotifications } from "../hooks/useCloudNotifications";

// Kept beside the local runtime in the shell. This deliberately has no local
// daemon subscriptions: its hook is gated by cloud auth + organization and
// owns only cloud-qualified cache entries.
export function CloudNotificationRuntime() {
	useCloudNotifications("all");
	return null;
}
