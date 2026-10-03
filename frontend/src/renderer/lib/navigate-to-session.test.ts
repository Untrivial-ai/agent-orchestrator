import { describe, expect, it } from "vitest";
import { projectNavigateTarget, sessionNavigateTarget } from "./navigate-to-session";

describe("sessionNavigateTarget", () => {
	it("keeps local URLs and identifies the remote host in a session URL", () => {
		expect(sessionNavigateTarget("project-1", "session-1")).toEqual({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "project-1", sessionId: "session-1" },
		});
		expect(sessionNavigateTarget("project-1", "session-1", "machine-a")).toEqual({
			to: "/host/$hostId/project/$projectId/session/$sessionId",
			params: { hostId: "machine-a", projectId: "project-1", sessionId: "session-1" },
		});
	});
});

it("routes a project to its owning host without changing the local URL", () => {
	expect(projectNavigateTarget("project-1")).toEqual({ to: "/projects/$projectId", params: { projectId: "project-1" } });
	expect(projectNavigateTarget("project-1", "machine-a")).toEqual({
		to: "/host/$hostId/project/$projectId", params: { hostId: "machine-a", projectId: "project-1" },
	});
});
