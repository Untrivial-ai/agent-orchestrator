import type { UpdateOutcome } from "../../shared/update-telemetry";
import { aoBridge } from "./bridge";
import { captureRendererEvent } from "./telemetry";

/**
 * Forwards update outcomes decided by the main process.
 *
 * Subscribed exactly once from main.tsx, not from a hook. `useUpdateStatus` is
 * mounted by both the sidebar row and the settings section, so a hook-based
 * observer would report every outcome twice; the per-name reservation in
 * captureRendererEvent rate-limits but does not deduplicate, so the duplicate
 * would survive.
 *
 * The payload is already reduced to enum-like fields by the main process, which
 * is also the only place that knows whether an operation was automatic and what
 * version it was fetching.
 */
export function startUpdateTelemetry(): () => void {
	const off = aoBridge.updates.onTelemetry?.((outcome: UpdateOutcome) => {
		void captureRendererEvent(outcome.event, {
			phase: outcome.phase,
			trigger: outcome.trigger,
			error_category: outcome.error_category,
			to_version: outcome.to_version,
			differential_eligible: outcome.differential_eligible,
			transfer_mode: outcome.transfer_mode,
			fallback: outcome.fallback,
			transferred_bytes: outcome.transferred_bytes,
			target_bytes: outcome.target_bytes,
		});
	});
	return () => off?.();
}
