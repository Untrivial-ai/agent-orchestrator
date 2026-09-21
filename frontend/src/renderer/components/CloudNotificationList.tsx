import { Check } from "lucide-react";
import { useCloudNotifications } from "../hooks/useCloudNotifications";
import { formatTimeCompact } from "../lib/format-time";

export function CloudNotificationList() {
	const { data, isLoading, markAllRead } = useCloudNotifications("all");
	const items = data?.items ?? [];
	if (isLoading || items.length === 0) return null;
	return (
		<div className="border-t border-border py-1.5" data-testid="cloud-notification-list">
			<div className="flex items-center justify-between px-4 py-1.5">
				<span className="text-caption font-medium text-muted-foreground">Cloud</span>
				<button className="text-caption text-muted-foreground hover:text-foreground" onClick={() => void markAllRead()} type="button">Mark cloud read</button>
			</div>
			{items.map((notification) => (
				<div className="flex items-start gap-3 px-4 py-2.5" data-notification-source="cloud" key={notification.id} role="listitem">
					<Check className="mt-0.5 size-4 shrink-0 text-blue-400" aria-hidden="true" />
					<div className="min-w-0 flex-1">
						<div className="flex items-center gap-2"><span className="truncate text-control text-foreground">{notification.title}</span><span className="rounded-full border border-blue-400/25 bg-blue-400/10 px-1.5 py-0.5 text-[9px] font-medium text-blue-300">Cloud</span></div>
						<p className="mt-0.5 text-caption text-muted-foreground">{notification.body}</p>
					</div>
					<time className="font-mono text-[9px] text-passive" dateTime={notification.createdAt}>{formatTimeCompact(notification.createdAt)}</time>
				</div>
			))}
		</div>
	);
}
