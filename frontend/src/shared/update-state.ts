/**
 * Update state vocabulary shared by the main process and the renderer.
 *
 * Here, not in `main/update-settings`, because that module imports node:fs. A
 * renderer VALUE import from there puts Node built-ins in the browser bundle and
 * the renderer fails to boot; type imports were safe only because they erase.
 */

// Streamed to the renderer so Settings and the sidebar can reflect progress.
// "retry-scheduled" is calm and non-error: AO will fetch and prepare again.
export type UpdateState =
	"idle" | "checking" | "available" | "not-available" | "downloading" | "preparing" | "downloaded" | "retry-scheduled" | "error" | "unsupported";

/**
 * Total Record on purpose: guards that spelled out their own subset silently
 * excluded `retry-scheduled` when it was added, because a missing name reads as
 * a legitimate false. This way a new state fails the build until classified.
 */
export const UPDATE_STATE_KIND = {
	idle: "resting",
	checking: "progress",
	available: "resting",
	"not-available": "resting",
	downloading: "progress",
	preparing: "progress",
	downloaded: "resting",
	"retry-scheduled": "failure",
	error: "failure",
	unsupported: "resting",
} as const satisfies Record<UpdateState, "progress" | "resting" | "failure">;

/** A failure already on screen. A later generic broadcast must not replace it. */
export function isReportedFailure(state: UpdateState): boolean {
	return UPDATE_STATE_KIND[state] === "failure";
}

/** An updater operation is still running and will report again. */
export function isUpdateInProgress(state: UpdateState): boolean {
	return UPDATE_STATE_KIND[state] === "progress";
}

/** Separate axis from the kind: `available` is settled but still worth re-reading. */
export const UPDATE_STATE_RECONCILES = {
	idle: false,
	checking: true,
	available: true,
	"not-available": false,
	downloading: true,
	preparing: true,
	downloaded: true,
	"retry-scheduled": true,
	error: true,
	unsupported: false,
} as const satisfies Record<UpdateState, boolean>;

export function shouldReconcileUpdateStatus(state: UpdateState): boolean {
	return UPDATE_STATE_RECONCILES[state];
}

/**
 * Not the same as "not in progress": `available` and `downloaded` mean the switch
 * found something, so the "update and restart to switch" prompt has to stay up.
 */
export const UPDATE_STATE_RESOLVES_CHANNEL_SWITCH = {
	idle: false,
	checking: false,
	available: false,
	"not-available": true,
	downloading: false,
	preparing: false,
	downloaded: false,
	"retry-scheduled": true,
	error: true,
	unsupported: true,
} as const satisfies Record<UpdateState, boolean>;

export function resolvesChannelSwitch(state: UpdateState): boolean {
	return UPDATE_STATE_RESOLVES_CHANNEL_SWITCH[state];
}

/**
 * Shared: the renderer watchdogs the same call, and when its ceiling was the
 * shorter of the two it failed a check that then succeeded, with no way to
 * retract. Any renderer deadline derives from this, never picked independently.
 */
export const UPDATE_CHECK_TIMEOUT_MS = 180_000;
