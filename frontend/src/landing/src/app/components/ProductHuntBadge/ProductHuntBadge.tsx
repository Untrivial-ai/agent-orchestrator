"use client";

import type { ReactNode } from "react";

/** The live Product Hunt page (outbound target for upvote / comment CTAs). */
export const PRODUCT_HUNT_URL =
	"https://www.producthunt.com/products/agent-orchestrator?launch=agent-orchestrator";

/** Which Product Hunt CTA this is; selects the label shown. */
const INTENT_LABEL = {
	badge: "Find Agent Orchestrator on Product Hunt",
	upvote: "Upvote us on Product Hunt",
} as const;

export type ProductHuntIntent = keyof typeof INTENT_LABEL;

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
 * A drop-in Product Hunt CTA that links to our Product Hunt page. The header
 * mounts the `upvote` variant for launch day; remove it after. It intentionally
 * does not carry UTM back to Product Hunt (the destination is Product Hunt,
 * not our site).
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
		>
			{children ?? INTENT_LABEL[intent]}
		</a>
	);
}