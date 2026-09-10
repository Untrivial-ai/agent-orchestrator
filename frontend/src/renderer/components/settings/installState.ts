import type { components } from "../../../api/schema";

export type InstallJob = components["schemas"]["InstallJob"];

/** What the panel should show about installing the connector. */
export type InstallView =
	| { kind: "offer" }
	| { kind: "running" }
	| { kind: "manual"; command: string; reason: string }
	| { kind: "failed"; reason: string }
	| { kind: "done" };

/**
 * Turns an install job into what the user should see.
 *
 * Kept separate from the component because the interesting cases are not
 * visual: Linux resolves as unsupported *with* the exact command, because AO
 * never asks for an administrator password — so "unsupported" there means
 * "here is what to run", not "this cannot work". Presenting that as a failure
 * would be wrong, and it is the case least likely to be exercised by hand.
 */
export function installView(job: InstallJob | undefined, starting: boolean): InstallView {
	if (starting) return { kind: "running" };
	if (!job) return { kind: "offer" };
	switch (job.status) {
		case "running":
			return { kind: "running" };
		case "succeeded":
			return { kind: "done" };
		case "unsupported":
			return job.command
				? { kind: "manual", command: job.command, reason: job.error ?? "" }
				: { kind: "failed", reason: job.error ?? "" };
		case "failed":
			return { kind: "failed", reason: job.error ?? "" };
		default:
			return { kind: "offer" };
	}
}
