import type { components } from "../../api/schema";

export type EstimatedCost = components["schemas"]["EstimatedCostResponse"];

const NANOS_PER_CENT = 10_000_000;
const NANOS_PER_DOLLAR = 1_000_000_000;
const usd = new Intl.NumberFormat("en-US", {
	currency: "USD",
	maximumFractionDigits: 2,
	minimumFractionDigits: 2,
	style: "currency",
});

/** Format an integer nano-USD value without rounding a positive sub-cent value to zero. */
export function formatCostNanos(nanos: number | null | undefined): string | null {
	if (nanos == null || !Number.isFinite(nanos) || nanos < 0) return null;
	if (nanos === 0) return "$0.00";
	if (nanos >= NANOS_PER_CENT) return usd.format(nanos / NANOS_PER_DOLLAR);

	const fractionalNanos = Math.round(nanos).toString().padStart(9, "0").replace(/0+$/, "");
	return `$0.${fractionalNanos}`;
}

/**
 * Format an estimate for display, or return null when there is nothing to show.
 *
 * Coverage stays a backend fact used for aggregation and for the contextual
 * disclosure beside the heading. It is deliberately not turned into a `≈`/`≥`
 * prefix here: a mathematical qualifier reads as a pricing claim, and every
 * estimate carries the same caveat regardless of coverage.
 */
export function formatEstimatedCost(cost: EstimatedCost | null | undefined): string | null {
	if (!cost) return null;
	return formatCostNanos(cost.totalNanos);
}
