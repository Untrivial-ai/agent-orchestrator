/**
 * Product Hunt launch event names.
 *
 * Fired through the shared `track()` wrapper (from `@/lib/analytics`), so they
 * inherit the campaign attribution and the best-effort/no-throw behavior, and
 * they are broken down by the normalized launch super-properties registered in
 * `instrumentation-client.ts`.
 *
 * Deliberately NOT re-declared here (already tracked elsewhere): download,
 * waitlist signup, install-command copy, section viewed, video progress, and
 * generic outbound link clicks. And deliberately NOT tracked on the marketing
 * site at all: signup/login/workspace/agent/workflow/orchestration events —
 * those happen inside the product and are emitted by the app/daemon.
 */

export const LAUNCH_EVENTS = {
	/** First event of a visit that came from Product Hunt. */
	phReferralVisit: "ph_referral_visit",
	/** Click on an embedded Product Hunt badge on our site. */
	phBadgeClick: "ph_badge_click",
	/** Click on a CTA that sends the visitor back to Product Hunt to upvote. */
	phUpvoteCtaClick: "ph_upvote_cta_click",
	/** A visitor we have seen before in this browser. */
	returnVisit: "return_visit",
} as const;
