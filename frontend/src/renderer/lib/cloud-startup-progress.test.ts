import { describe, expect, it } from "vitest";
import type { WorkspaceSession } from "../types/workspace";
import type { CloudCpClientEvent } from "./cloud-cp/types";
import type { CloudPendingSession } from "./cloud-pending-session";
import {
	deriveCloudStartupProgress,
	reduceCloudStartupEvent,
	type CloudStartupProjection,
} from "./cloud-startup-progress";

const attempt: CloudPendingSession = {
	attemptId: "attempt-1",
	createIdempotencyKey: "attempt-1",
	createState: "accepted",
	durableSessionId: "session-1",
	initialPrompt: "Initial task",
	messages: [],
	orgId: "org-1",
	projectId: "project-1",
	routeSessionId: "pending-cloud-attempt-1",
	startedAtMs: 100,
};

function event(sequence: number, type: string, payload: unknown = {}): CloudCpClientEvent {
	return { sessionId: "session-1", sequence, type, payload, createdAt: "2026-09-22T00:00:00Z" };
}

function session(observedState: string, runtimeConnected = false): WorkspaceSession {
	return {
		id: "session-1",
		workspaceId: "project-1",
		workspaceName: "Cloud project",
		title: "Worker",
		provider: "codex",
		status: "working",
		updatedAt: "2026-09-22T00:00:00Z",
		prs: [],
		runtimeConnected,
		cloud: { orgId: "org-1", sandboxProvider: "docker", desiredState: "running", observedState },
	};
}

describe("cloud startup progress", () => {
	it("projects each stable startup phase from replayed milestones", () => {
		let projection: CloudStartupProjection = { latestSequence: 0 };
		expect(deriveCloudStartupProgress(attempt, session("requested"), projection).phase).toBe("allocating_workspace");
		projection = reduceCloudStartupEvent(projection, event(1, "sandbox.provisioning"));
		expect(deriveCloudStartupProgress(attempt, session("provisioning"), projection).phase).toBe("starting_workspace");
		projection = reduceCloudStartupEvent(projection, event(2, "worker.connected", { epoch: 1 }));
		expect(deriveCloudStartupProgress(attempt, session("running", true), projection).phase).toBe("preparing_repository");
		projection = reduceCloudStartupEvent(projection, event(3, "workspace.ready", { epoch: 1 }));
		expect(deriveCloudStartupProgress(attempt, session("running", true), projection).phase).toBe("starting_agent");
		expect(deriveCloudStartupProgress({ ...attempt, createState: "ready" }, session("running", true), projection).phase).toBe("ready");
	});

	it("ignores duplicates and resets worker milestones for a newer epoch", () => {
		let projection = reduceCloudStartupEvent({ latestSequence: 0 }, event(1, "workspace.ready", { epoch: 1 }));
		const duplicate = reduceCloudStartupEvent(projection, event(1, "sandbox.provisioning"));
		expect(duplicate).toBe(projection);
		projection = reduceCloudStartupEvent(projection, event(2, "worker.connected", { epoch: 2 }));
		expect(projection).toMatchObject({ latestSequence: 2, workerEpoch: 2 });
		expect(projection.latestMilestone).toBe("worker.connected");
	});

	it("keeps a bounded owning phase for startup failures", () => {
		const projection = reduceCloudStartupEvent(
			{ latestSequence: 0 },
			event(1, "startup.failed", {
				phase: "preparing_repository",
				code: "CHECKOUT_FAILED",
				message: "repository grant denied",
			}),
		);
		expect(deriveCloudStartupProgress(attempt, session("running", true), projection)).toEqual({
			phase: "failed",
			failure: {
				phase: "preparing_repository",
				code: "CHECKOUT_FAILED",
				message: "repository grant denied",
			},
		});
	});
});
