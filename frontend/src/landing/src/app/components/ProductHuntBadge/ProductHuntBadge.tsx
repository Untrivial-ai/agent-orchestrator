"use client";

import type { ReactNode } from "react";

import { track } from "@/lib/analytics";
import { LAUNCH_EVENTS } from "@/lib/analytics/launch/events";
import { PRODUCT_HUNT_URL } from "@/lib/analytics/launch/utm";

/** Which Product Hunt CTA this is; selects the event fired on click. */
export type ProductHuntIntent = keyof typeof INTENT_EVENT;

const INTENT_EVENT = {
	/** The drop-in badge itself ("find us on PH"). */
	badge: LAUNCH_EVENTS.phBadgeClick,
	/** A CTA sending the visitor back to upvote. */
	upvote: LAUNCH_EVENTS.phUpvoteCtaClick,
} as const;

const INTENT_LABEL: Record<ProductHuntIntent, string> = {
	badge: "Find Agent Orchestrator on Product Hunt",
	upvote: "Upvote us on Product Hunt",
};

type ProductHuntBadgeProps = {
	/**
	 * The badge visual. Pass Product Hunt's official embed `<img>` here so we do
	 * not hardcode an asset (the embed URL depends on the launch/post id). When
	 * omitted, a plain text label is rendered so the CTA still works.
	 */
	children?: ReactNode;
	className?: string;
	/** Which CTA this instance is; defaults to the plain badge. */
	intent?: ProductHuntIntent;
};

/**
 * A drop-in Product Hunt CTA for launch day. It links to our Product Hunt page
 * and fires the event matching `intent` on click (`ph_badge_click` or
 * `ph_upvote_cta_click`). The header mounts the `upvote` variant for launch
 * day; remove it after. It intentionally does not carry UTM back to Product
 * Hunt (the destination is Product Hunt, not our site).
 */
export function ProductHuntBadge({
	children,
	className,
	intent = "badge",
}: ProductHuntBadgeProps) {
	return (
		<a
			href={PRODUCT_HUNT_URL}
			target="_blank"
			rel="noopener noreferrer"
			className={className}
			aria-label="Agent Orchestrator on Product Hunt"
			onClick={() => track(INTENT_EVENT[intent])}
		>
			{children ?? INTENT_LABEL[intent]}
		</a>
	);
}
