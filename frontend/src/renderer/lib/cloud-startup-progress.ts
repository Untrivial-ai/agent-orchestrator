import { useEffect, useMemo, useRef, useState } from "react";
import { useCloudCp } from "../hooks/useCloudCp";
import type { WorkspaceSession } from "../types/workspace";
import { subscribeSessionEventsBridged } from "./cloud-cp/stream-bridge";
import type { CloudCpClientEvent } from "./cloud-cp/types";
import type { CloudPendingSession } from "./cloud-pending-session";

export type CloudStartupPhaseId =
	| "saving_session"
	| "allocating_workspace"
	| "starting_workspace"
	| "preparing_repository"
	| "starting_agent"
	| "ready"
	| "failed";

export type CloudStartupFailure = {
	code: string;
	message: string;
	phase: Exclude<CloudStartupPhaseId, "failed" | "ready">;
};

export type CloudStartupProjection = {
	failure?: CloudStartupFailure;
	latestMilestone?: string;
	latestSequence: number;
	workerEpoch?: number;
};

export type CloudStartupProgress = {
	failure?: CloudStartupFailure;
	phase: CloudStartupPhaseId;
	streamError?: string;
};

const initialProjection: CloudStartupProjection = { latestSequence: 0 };
const milestonePhases: Record<string, CloudStartupPhaseId> = {
	"sandbox.provisioning": "starting_workspace",
	"worker.connected": "preparing_repository",
	"worker.ready": "preparing_repository",
	"checkout.started": "preparing_repository",
	"checkout.completed": "preparing_repository",
	"restore.started": "preparing_repository",
	"restore.completed": "preparing_repository",
	"workspace.ready": "starting_agent",
	"agent.launch_started": "starting_agent",
	"agent.ready": "starting_agent",
	"terminal.first_frame": "ready",
};
const failurePhases = new Set<CloudStartupFailure["phase"]>([
	"saving_session",
	"allocating_workspace",
	"starting_workspace",
	"preparing_repository",
	"starting_agent",
]);

function objectPayload(payload: unknown): Record<string, unknown> | undefined {
	return payload !== null && typeof payload === "object" && !Array.isArray(payload)
		? (payload as Record<string, unknown>)
		: undefined;
}

function bounded(value: unknown, fallback: string, limit: number): string {
	return typeof value === "string" && value.trim() !== ""
		? value.trim().slice(0, limit)
		: fallback;
}

export function reduceCloudStartupEvent(
	projection: CloudStartupProjection,
	event: CloudCpClientEvent,
): CloudStartupProjection {
	if (event.sequence <= projection.latestSequence) return projection;
	const payload = objectPayload(event.payload);
	const rawEpoch = payload?.epoch;
	const eventEpoch = typeof rawEpoch === "number" && Number.isSafeInteger(rawEpoch) && rawEpoch > 0
		? rawEpoch
		: undefined;
	const newWorkerEpoch = event.type === "worker.connected" && eventEpoch !== undefined &&
		(projection.workerEpoch === undefined || eventEpoch > projection.workerEpoch);
	const next: CloudStartupProjection = newWorkerEpoch
		? { latestSequence: event.sequence, workerEpoch: eventEpoch }
		: { ...projection, latestSequence: event.sequence };
	if (eventEpoch !== undefined && next.workerEpoch !== undefined && eventEpoch < next.workerEpoch) {
		return next;
	}
	if (eventEpoch !== undefined) next.workerEpoch = Math.max(next.workerEpoch ?? 0, eventEpoch);
	if (event.type === "startup.failed") {
		const phase = payload?.phase;
		if (typeof phase === "string" && failurePhases.has(phase as CloudStartupFailure["phase"])) {
			next.failure = {
				phase: phase as CloudStartupFailure["phase"],
				code: bounded(payload?.code, "STARTUP_FAILED", 80),
				message: bounded(payload?.message, "Cloud startup failed.", 240),
			};
		}
		return next;
	}
	if (event.type in milestonePhases) {
		next.latestMilestone = event.type;
		// Parallel checkout progress does not recover failed harness startup.
		if (event.type === "agent.ready") next.failure = undefined;
	}
	return next;
}

export function deriveCloudStartupProgress(
	attempt: CloudPendingSession,
	session: WorkspaceSession | undefined,
	projection: CloudStartupProjection,
): CloudStartupProgress {
	if (attempt.createState === "failed") {
		return {
			phase: "failed",
			failure: {
				phase: "saving_session",
				code: "SESSION_CREATE_FAILED",
				message: attempt.createError ?? "The Cloud session could not be saved.",
			},
		};
	}
	if (projection.failure) return { phase: "failed", failure: projection.failure };
	if (attempt.createState === "ready") return { phase: "ready" };
	if (attempt.createState === "saving" || !attempt.durableSessionId) {
		return { phase: "saving_session" };
	}
	const milestonePhase = projection.latestMilestone
		? milestonePhases[projection.latestMilestone]
		: undefined;
	if (milestonePhase) return { phase: milestonePhase };
	const observed = session?.cloud?.observedState;
	if (observed === "failed" || observed === "terminated") {
		return {
			phase: "failed",
			failure: {
				phase: session?.runtimeConnected ? "preparing_repository" : "starting_workspace",
				code: "WORKSPACE_START_FAILED",
				message: "The workspace did not start. Retry with the saved session and messages.",
			},
		};
	}
	if (session?.runtimeConnected) return { phase: "preparing_repository" };
	if (observed === "provisioning" || observed === "bootstrapping" || observed === "restoring") {
		return { phase: "starting_workspace" };
	}
	return { phase: "allocating_workspace" };
}

function waitForRetry(signal: AbortSignal): Promise<void> {
	return new Promise((resolve) => {
		if (signal.aborted) {
			resolve();
			return;
		}
		const timer = window.setTimeout(done, 1_000);
		function done() {
			window.clearTimeout(timer);
			signal.removeEventListener("abort", done);
			resolve();
		}
		signal.addEventListener("abort", done, { once: true });
	});
}

export function useCloudStartupProgress(
	attempt: CloudPendingSession,
	session?: WorkspaceSession,
	retryToken = 0,
): CloudStartupProgress {
	const { baseUrl } = useCloudCp();
	const [projection, setProjection] = useState<CloudStartupProjection>(initialProjection);
	const [streamError, setStreamError] = useState<string>();
	const latestSequence = useRef(0);
	const sessionId = attempt.durableSessionId;

	useEffect(() => {
		setProjection(initialProjection);
		setStreamError(undefined);
		latestSequence.current = 0;
		if (!sessionId || baseUrl === "") return;
		const controller = new AbortController();
		const subscribe = async () => {
			while (!controller.signal.aborted) {
				await subscribeSessionEventsBridged({
					baseUrl,
					orgId: attempt.orgId,
					sessionId,
					after: latestSequence.current || undefined,
					signal: controller.signal,
					onEvent: (event) => {
						latestSequence.current = Math.max(latestSequence.current, event.sequence);
						setProjection((current) => reduceCloudStartupEvent(current, event));
						setStreamError(undefined);
					},
					onError: (error) => {
						setStreamError(error.message.slice(0, 240));
					},
				});
				if (!controller.signal.aborted) await waitForRetry(controller.signal);
			}
		};
		void subscribe();
		return () => controller.abort();
	}, [attempt.orgId, baseUrl, retryToken, sessionId]);

	return useMemo(
		() => ({ ...deriveCloudStartupProgress(attempt, session, projection), streamError }),
		[attempt, projection, session, streamError],
	);
}
