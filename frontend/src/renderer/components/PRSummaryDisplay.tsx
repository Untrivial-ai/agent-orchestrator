import {
	PRCardStatusSummary as ProductPRCardStatusSummary,
	PRSummaryMeta as ProductPRSummaryMeta,
	PRSummaryParts as ProductPRSummaryParts,
	type CountNounLabel,
	type PRSummaryPartKey,
} from "@aoagents/product-ui";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { SessionPRSummary } from "../hooks/useSessionScmSummary";
import { prCardPresentation, prNounKeys, prSummaryParts } from "../lib/pr-display";
import { ProductExternalLink } from "./ProductExternalLink";

function useCountNounLabel(): CountNounLabel {
	const { t } = useTranslation();
	return (count, noun) => `${count} ${t(prNounKeys[noun], { count })}`;
}

export function PRSummaryMeta({
	className,
	leading,
	pr,
}: {
	className?: string;
	leading?: string;
	pr: SessionPRSummary;
}) {
	return (
		<ProductPRSummaryMeta
			className={className}
			countNounLabel={useCountNounLabel()}
			externalLink={ProductExternalLink}
			leading={leading}
			pr={pr}
		/>
	);
}

export function PRCardStatusSummary({
	action,
	className,
	omit,
	pr,
}: {
	action?: ReactNode;
	className?: string;
	omit?: PRSummaryPartKey[];
	pr: SessionPRSummary;
}) {
	return (
		<ProductPRCardStatusSummary
			action={action}
			className={className}
			externalLink={ProductExternalLink}
			omit={omit}
			presentation={prCardPresentation(pr)}
		/>
	);
}

export function PRSummaryParts({
	className,
	omit,
	interactiveLinks = true,
	maxLinks = 3,
	pr,
	variant = "compact",
}: {
	className?: string;
	omit?: PRSummaryPartKey[];
	interactiveLinks?: boolean;
	maxLinks?: number;
	pr: SessionPRSummary;
	variant?: "compact" | "stacked";
}) {
	return (
		<ProductPRSummaryParts
			className={className}
			countNounLabel={useCountNounLabel()}
			externalLink={ProductExternalLink}
			interactiveLinks={interactiveLinks}
			maxLinks={maxLinks}
			omit={omit}
			parts={prSummaryParts(pr)}
			variant={variant}
		/>
	);
}
