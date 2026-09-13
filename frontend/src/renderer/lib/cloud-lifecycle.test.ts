import { describe, expect, it } from "vitest";
import type { WorkspaceSession } from "../types/workspace";
import { cloudLifecycleStage } from "./cloud-lifecycle";

function session(
	desiredState: string,
	observedState: string,
	runtimeConnected = false,
): WorkspaceSession {
	return {
		id: "session-1",
		workspaceId: "project-1",
		workspaceName: "cloud-project",
		title: "Cloud worker",
		provider: "claude-code",
		status: "working",
		updatedAt: "2026-09-01T00:00:00Z",
		prs: [],
		runtimeConnected,
		cloud: { orgId: "org-1", sandboxProvider: "coder", desiredState, observedState },
	};
}

describe("cloudLifecycleStage", () => {
	it.each([
		["paused", "stopped", false, "paused_by_coder"],
		["running", "stopped", false, "resuming_workspace"],
		["running", "restoring", false, "resuming_workspace"],
		["running", "provisioning", false, "waiting_for_coder_agent"],
		["running", "bootstrapping", false, "starting_ao_worker"],
		["running", "bootstrapping", true, "restoring_agent"],
		["running", "running", false, "restoring_agent"],
		["running", "running", true, "connected"],
	] as const)(
		"maps %s/%s (connected=%s) to %s",
		(desired, observed, connected, expected) => {
			expect(cloudLifecycleStage(session(desired, observed, connected))).toBe(expected);
		},
	);

	it("does not invent lifecycle state for local sessions", () => {
		const local = session("running", "running", true);
		delete local.cloud;
		expect(cloudLifecycleStage(local)).toBeUndefined();
	});
});
