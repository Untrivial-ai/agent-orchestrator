import { useEffect, useRef, useState } from "react";
import type { UpdateStatus } from "../../main/update-settings";
import { aoBridge } from "../lib/bridge";

let current: UpdateStatus = { state: "idle" };
const listeners = new Set<(status: UpdateStatus) => void>();
let reconcileUsers = 0;
let revision = 0;
let stop: (() => void) | undefined;
let downloadPending = false;

function receive(status: UpdateStatus) {
	current = status;
	revision += 1;
	for (const listener of listeners) listener(status);
}

function connect() {
	let live = true;
	let pending = false;
	// Subscribe before reading. A late local snapshot must never replace a push.
	const off = aoBridge.updates.onStatus(receive);
	const refresh = async () => {
		if (pending) return;
		pending = true;
		const requestedRevision = revision;
		try {
			const next = await aoBridge.updates.getStatus();
			if (live && requestedRevision === revision) receive(next);
		} catch {
			// Retain the last known status and retry the local read next time.
		} finally {
			pending = false;
		}
	};
	void refresh();
	const timer = setInterval(() => {
		if (reconcileUsers > 0 || ["available", "downloading", "preparing", "downloaded", "error"].includes(current.state)) void refresh();
	}, 3_000);
	return () => {
		live = false;
		clearInterval(timer);
		off?.();
	};
}

/** One local status stream shared by the sidebar, Settings and confirmation. */
export function useUpdateStatus(onStatusEvent?: (status: UpdateStatus) => void, reconcile = false): UpdateStatus {
	const [status, setStatus] = useState(current);
	const callback = useRef(onStatusEvent);
	callback.current = onStatusEvent;
	useEffect(() => {
		const listener = (next: UpdateStatus) => {
			callback.current?.(next);
			setStatus(next);
		};
		listeners.add(listener);
		if (reconcile) reconcileUsers += 1;
		listener(current);
		if (!stop) stop = connect();
		return () => {
			listeners.delete(listener);
			if (reconcile) reconcileUsers -= 1;
			if (listeners.size === 0) {
				stop?.();
				stop = undefined;
				current = { state: "idle" };
			}
		};
	}, [reconcile]);
	return status;
}

/** Show acknowledgment in every surface before the main process receives IPC. */
export async function requestUpdateDownload(requestId?: string): Promise<void> {
	if (downloadPending || current.state === "downloading" || current.state === "preparing") return;
	downloadPending = true;
	receive({ ...current, state: "downloading", percent: undefined, transferred: undefined, total: undefined, requestId });
	try {
		await aoBridge.updates.download(requestId);
	} catch (error) {
		if (["downloading"].includes(current.state)) {
			receive({ ...current, state: "error", message: error instanceof Error ? error.message : "Download failed. Try again." });
		}
	} finally {
		downloadPending = false;
	}
}
