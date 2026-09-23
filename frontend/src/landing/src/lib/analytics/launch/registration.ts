/**
 * Turning a launch context into PostHog super-property operations.
 *
 * Extracted so the register/unregister decision is unit-testable: it is easy to
 * get subtly wrong in ways that silently misattribute every event (registering
 * a campaign the visit never carried, or letting a stale one persist).
 */

import type { LaunchContext } from "./context";

export type LaunchRegistration = {
	/** Properties to `register` as super-properties. */
	register: Record<string, unknown>;
	/** Keys to `unregister` first; emptied of values the visit does not carry. */
	unregister: string[];
};

/**
 * Super-properties for the visit's context.
 *
 * `campaign` is registered only when the visit actually carried one, and is
 * explicitly unregistered otherwise: registered super-properties persist in the
 * PostHog cookie, so without the unregister a campaign from an earlier visit
 * would keep riding along on later untagged visits.
 */
export function launchSuperProperties(
	context: LaunchContext,
): LaunchRegistration {
	const base = {
		source: context.source,
		user_type: context.user_type,
		device: context.device,
	};
	if (context.campaign) {
		return {
			register: { ...base, campaign: context.campaign },
			unregister: [],
		};
	}
	return { register: base, unregister: ["campaign"] };
}
