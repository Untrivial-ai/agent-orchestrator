import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { attentionZone, workerSessions, type WorkspaceSession, type WorkspaceSummary } from "../types/workspace";

const h = vi.hoisted(() => ({
	setAttentionState: vi.fn(),
	navigateToSession: vi.fn(),
	workspaces: [] as WorkspaceSummary[],
	listener: null as null | ((target: { projectId: string; sessionId: string }) => void),
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceTraySessions: () => ({
		data: h.workspaces.flatMap((workspace) =>
			workerSessions(workspace.sessions).flatMap((session) => {
				const zone = attentionZone(session);
				if ((zone === "merge" && session.status === "merged") || (zone !== "action" && zone !== "merge")) return [];
				return [{ projectId: workspace.id, projectName: workspace.name, sessionId: session.id, title: session.title, zone }];
			}),
		),
	}),
	workspaceQueryKey: ["workspaces"],
}));

vi.mock("../lib/navigate-to-session", () => ({
	useNavigateToSession: () => h.navigateToSession,
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		tray: {
			setAttentionState: h.setAttentionState,
			onOpenSession: (listener: (target: { projectId: string; sessionId: string }) => void) => {
				h.listener = listener;
				return () => {
					h.listener = null;
				};
			},
		},
	},
}));

import { TrayRuntime } from "./TrayRuntime";

function worker(overrides: Partial<WorkspaceSession> & { id: string }): WorkspaceSession {
	return {
		workspaceId: "proj-1",
		workspaceName: "note-tauri",
		title: overrides.id,
		provider: "codex",
		kind: "worker",
		branch: `feature/${overrides.id}`,
		status: "working",
		updatedAt: "2026-06-10T00:00:00Z",
		prs: [],
		...overrides,
	};
}

function workspaces(): WorkspaceSummary[] {
	return [
		{
			id: "proj-1",
			name: "note-tauri",
			path: "/repos/note",
			type: "main",
			sessions: [
				worker({ id: "s-need", title: "needs it", status: "needs_input" }),
				worker({ id: "s-work", title: "working", status: "working" }),
				worker({ id: "s-merge", title: "merge me", status: "mergeable" }),
				worker({ id: "s-merged", title: "already merged", status: "merged" }),
				worker({ id: "orch", title: "orchestrator", kind: "orchestrator", status: "needs_input" }),
			],
		},
	];
}

afterEach(() => {
	h.setAttentionState.mockReset();
	h.navigateToSession.mockReset();
	h.listener = null;
});

describe("TrayRuntime", () => {
	it("pushes only attention-worthy worker sessions to the tray", () => {
		h.workspaces = workspaces();
		render(<TrayRuntime />);
		expect(h.setAttentionState).toHaveBeenLastCalledWith({
			sessions: [
				{ projectId: "proj-1", projectName: "note-tauri", sessionId: "s-need", title: "needs it", zone: "action" },
				{ projectId: "proj-1", projectName: "note-tauri", sessionId: "s-merge", title: "merge me", zone: "merge" },
			],
		});
	});

	it("navigates when main reports a tray open-session", () => {
		h.workspaces = workspaces();
		render(<TrayRuntime />);
		act(() => h.listener?.({ projectId: "proj-1", sessionId: "s-need" }));
		expect(h.navigateToSession).toHaveBeenCalledWith("proj-1", "s-need");
	});

	it("excludes an already-merged session even though it shares the merge zone", () => {
		h.workspaces = workspaces();
		render(<TrayRuntime />);
		const pushed = h.setAttentionState.mock.calls.at(-1)?.[0]?.sessions ?? [];
		expect(pushed.some((entry: { sessionId: string }) => entry.sessionId === "s-merged")).toBe(false);
	});
});
