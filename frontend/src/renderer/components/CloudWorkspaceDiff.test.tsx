// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";
import type { FileAnnotationModel } from "./WorkspaceDiffView";
import { CloudFileContentPane, CloudWorkspaceDiff } from "./CloudWorkspaceDiff";

const { explorerProps, contentProps, client } = vi.hoisted(() => ({ explorerProps: vi.fn(), contentProps: vi.fn(), client: {} }));
vi.mock("../hooks/useCloudCp", () => ({ useCloudCp: () => ({ baseUrl: "https://cloud.test", client, ready: true }) }));
vi.mock("./cloud-workspace/CloudWorkspaceExplorer", () => ({ CloudWorkspaceExplorer: (props: { onOpenFile?: (path: string, options: unknown) => void }) => { explorerProps(props); return <button onClick={() => props.onOpenFile?.("src/App.tsx", { scope: "committed", commitSha: "abc", mode: "diff", editing: true })}>open cloud file</button>; } }));
vi.mock("./cloud-workspace/CloudFileContentPane", () => ({ CloudFileContentPane: (props: unknown) => { contentProps(props); return <div>cloud content</div>; } }));

const session: WorkspaceSession = { branch: "ao/cloud", cloud: { orgId: "org-1", sandboxProvider: "coder" }, id: "session-1", prs: [], provider: "codex", status: "working", title: "Cloud", updatedAt: "2026-09-20T00:00:00Z", workspaceId: "workspace-1", workspaceName: "Cloud workspace" };
const annotation: FileAnnotationModel = { target: null, draft: "", status: "idle", error: "", begin: vi.fn(), setDraft: vi.fn(), cancel: vi.fn(), submit: vi.fn() };

describe("Cloud workspace compatibility exports", () => {
	it("delegates review and preserves file-open options", async () => {
		const onOpenFile = vi.fn();
		render(<CloudWorkspaceDiff annotation={annotation} onOpenFile={onOpenFile} session={session} split />);
		await userEvent.click(screen.getByRole("button", { name: "open cloud file" }));
		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx", { scope: "committed", commitSha: "abc", mode: "diff", editing: true });
		expect(explorerProps).toHaveBeenCalledWith(expect.objectContaining({ annotation, session, split: true }));
	});

	it("passes scoped view and edit state to the Cloud content pane", () => {
		const { container } = render(<CloudFileContentPane annotation={annotation} commitSha="abc" initialEditing initialMode="rendered" initialRequestKey={4} path="README.md" scope="committed" session={session} split />);
		expect(screen.getByText("cloud content")).toBeInTheDocument();
		expect(container.querySelector(".overflow-y-auto")).toContainElement(screen.getByText("cloud content"));
		expect(contentProps).toHaveBeenCalledWith(expect.objectContaining({ baseUrl: "https://cloud.test", client, orgId: "org-1", sessionId: "session-1", path: "README.md", scope: "committed", commitSha: "abc", initialEditing: true, initialMode: "rendered", initialRequestKey: 4, split: true }));
	});

	it("keeps the dirty-state callback stable across parent renders", () => {
		const onDirtyChange = vi.fn();
		const { rerender } = render(<CloudFileContentPane annotation={annotation} onDirtyChange={onDirtyChange} path="README.md" session={session} />);
		const firstCallback = contentProps.mock.lastCall?.[0].onDirtyChange;
		rerender(<CloudFileContentPane annotation={annotation} onDirtyChange={onDirtyChange} path="README.md" session={session} />);
		const secondCallback = contentProps.mock.lastCall?.[0].onDirtyChange;
		expect(secondCallback).toBe(firstCallback);
	});
});
