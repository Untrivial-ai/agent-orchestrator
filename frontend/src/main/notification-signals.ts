/**
 * Notification signal policy for the main process.
 *
 * The renderer forwards every `notification_created` event to `notifications:show`,
 * so the type reaching main is always one defined by
 * `backend/internal/domain/notification.go`. These helpers keep the decision of
 * "toast vs. active attention signal" in one typed, testable place rather than as
 * hardcoded string literals scattered through the IPC handler.
 */

/** The notification types defined by `backend/internal/domain/notification.go`. */
export type NotificationType = "needs_input" | "ready_to_merge" | "pr_merged" | "pr_closed_unmerged";

/**
 * Whether to fire the Windows/Linux taskbar flash. Restricted to the
 * actionable types: a merged or closed PR is worth a toast, but should not
 * demand attention as insistently as an agent blocked waiting on the user.
 *
 * The macOS dock bounce is deliberately NOT gated by this allowlist — every
 * notification bounces there, with urgency carried by {@link dockBounceType}.
 */
const ATTENTION_TYPES: ReadonlySet<string> = new Set<NotificationType>(["needs_input", "ready_to_merge"]);

/** Whether this notification type should flash the Windows/Linux taskbar. */
export function shouldSignalAttention(type: string | undefined): boolean {
	return type !== undefined && ATTENTION_TYPES.has(type);
}

/**
 * Whether a new macOS dock bounce should replace the pending one. A pending
 * "critical" bounce (agent blocked on the user) is never replaced: a later
 * informational notification must not downgrade it to a one-shot bounce, or
 * the blocked-agent signal would stop lasting until the user returns.
 */
export function shouldReplaceBounce(pending: { critical: boolean } | null): boolean {
	return pending === null || !pending.critical;
}

/**
 * Whether to fire an OS toast. Deliberately independent of the type list: every
 * backend notification type gets a toast, so adding a new type in
 * `notification.go` can never silently drop its toast (the bug this replaced).
 */
export function shouldToast(notification: { title?: string }, isSupported: boolean): boolean {
	return Boolean(notification.title) && isSupported;
}

/**
 * macOS dock bounce style. A blocked agent waiting on the user keeps bouncing
 * until the app is activated ("critical"); anything else bounces once
 * ("informational"). This is where urgency lives, so every notification can
 * signal without a merged PR nagging as insistently as a blocked agent.
 */
export function dockBounceType(type: string | undefined): "critical" | "informational" {
	return type === "needs_input" ? "critical" : "informational";
}
