import { contextReadout, type Severity } from "./conversationChrome";
import type { ConversationUsage } from "./types";

/**
 * The context-budget meter shown in the composer's meta row.
 *
 * Deliberately absent below 70%. A permanent token gauge is chrome nobody
 * reads; one that appears as the window fills is information, and it arrives
 * before a compaction surprises you mid-thread.
 */
export type ContextMeterModel = {
	/** Whole percent of the context window used. */
	percent: number;
	/** Bar width as a percent — floored so a thin sliver stays visible. */
	fillPercent: number;
	severity: Exclude<Severity, "normal">;
	/** Short enough to sit beside the turn-settings control. */
	label: string;
};

/**
 * `quotaActive` is the account-quota banner above the timeline. When that is up
 * the user already has a resource warning on screen, and two at once read as
 * noise rather than urgency — so the meter yields to it.
 */
export function contextMeterModel(usage: ConversationUsage | undefined, quotaActive: boolean): ContextMeterModel | null {
	if (quotaActive) return null;

	const readout = contextReadout(usage);
	// `percent` is absent when the harness reports no context window, and there
	// is no budget to show without one.
	if (!readout || readout.percent === undefined || readout.fillPercent === undefined) return null;
	if (readout.severity === "normal") return null;

	return {
		percent: readout.percent,
		fillPercent: readout.fillPercent,
		severity: readout.severity,
		label: `${readout.percent}% context`,
	};
}
