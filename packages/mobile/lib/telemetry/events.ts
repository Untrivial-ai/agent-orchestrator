// The mobile app's event vocabulary and the property allowlist for each event.
//
// Split from any React Native import so the rules are unit-testable, the same
// split the codebase already uses for onboarding.ts vs onboardingStore.ts.
//
// The allowlist is the privacy contract. The phone can see session titles, PR
// titles, project names, terminal output, and the connection password, none of
// which may ever reach PostHog. sanitize.ts iterates THIS allowlist, never the
// caller's payload, so a property nobody registered here is dropped rather than
// forwarded. Adding a field is a deliberate edit to this file, reviewable in one
// place.

export const MOBILE_EVENTS = {
	// Unions with the desktop heartbeat of the same name; the `client` context
	// property separates mobile from desktop. One "active users" metric, split by
	// surface.
	active: "ao.v2.app.active",

	// A brand-new credential was validated by scanning a QR or manual entry. The
	// phone-side confirmation of the daemon's ao.mobile.device_connected.
	paired: "ao.v2.mobile_app.paired",
	// The stored config reconnected on launch (no user pairing action).
	connected: "ao.v2.mobile_app.connected",

	onboardingStarted: "ao.v2.mobile_app.onboarding_started",
	onboardingCompleted: "ao.v2.mobile_app.onboarding_completed",
	onboardingSkipped: "ao.v2.mobile_app.onboarding_skipped",

	// A push notification opened the app. The retention signal.
	notificationOpened: "ao.v2.mobile_app.notification_opened",

	// A core action was taken (spawn, merge, kill, ...). Drives "most-used
	// feature". Screen-view tracking was dropped by product decision: feature
	// events answer the usage question without a second per-navigation stream.
	featureUsed: "ao.v2.mobile_app.feature_used",

} as const;

export type MobileEventName = (typeof MOBILE_EVENTS)[keyof typeof MOBILE_EVENTS];

/**
 * Allowed property keys per event, with the closed vocabulary for enum fields.
 *
 * A key absent here is dropped. A key present with a `oneOf` is dropped unless
 * the value is in that set. A key present with `kind: "count"` keeps a finite
 * non-negative integer; `kind: "flag"` keeps a boolean. No `kind: "string"`
 * exists on purpose: there is no free-text field in the mobile vocabulary, so a
 * title or path can never ride along.
 */
export type PropRule =
	| { readonly oneOf: readonly string[] }
	| { readonly kind: "count" }
	| { readonly flag: true };

export const MOBILE_ALLOWLIST: Record<string, Readonly<Record<string, PropRule>>> = {
	[MOBILE_EVENTS.active]: {},
	[MOBILE_EVENTS.paired]: {
		method: { oneOf: ["qr", "manual"] },
		from_onboarding: { flag: true },
	},
	[MOBILE_EVENTS.connected]: {
		trigger: { oneOf: ["launch", "reconnect"] },
	},
	[MOBILE_EVENTS.onboardingStarted]: {},
	[MOBILE_EVENTS.onboardingCompleted]: {},
	[MOBILE_EVENTS.onboardingSkipped]: {},
	[MOBILE_EVENTS.notificationOpened]: {
		// notificationView already maps a push to a small set of destinations.
		target: { oneOf: ["session", "prs"] },
		cold_start: { flag: true },
	},
	[MOBILE_EVENTS.featureUsed]: {
		feature: {
			oneOf: ["spawn", "merge", "kill", "restore", "conductor", "send", "handoff"],
		},
		outcome: { oneOf: ["succeeded", "failed"] },
		// The chat/tui interface. Set on spawn and conductor (the mode the session
		// starts in) and on handoff (the mode it switches to). Absent on the other
		// features, which are mode-agnostic. Only the two enum values ever leave the
		// device; no titles or paths ride on this.
		mode: { oneOf: ["chat", "tui"] },
	},
};
