import { describe, expect, it } from "vitest";
import { sessionInterfaceTransition } from "../test/interface-transition-fixtures";
import type { WorkspaceSession } from "../types/workspace";
import { canResumeAgent } from "./useCanResumeAgent";

const exitedSession: WorkspaceSession = {
	id: "session-1",
	workspaceId: "project-1",
	workspaceName: "Project One",
	title: "Exited agent",
	provider: "codex",
	kind: "worker",
	branch: "ao/session-1",
	status: "exited",
	activity: { state: "exited", lastActivityAt: "2026-09-21T00:00:00Z" },
	updatedAt: "2026-09-21T00:00:00Z",
	prs: [],
};

describe("canResumeAgent", () => {
	it("blocks resume while an interface transition is active", () => {
		expect(canResumeAgent(exitedSession, sessionInterfaceTransition(exitedSession.id))).toBe(false);
	});

	it("allows resume after an interface transition completes", () => {
		expect(canResumeAgent(exitedSession, sessionInterfaceTransition(exitedSession.id, "completed"))).toBe(true);
	});
});
