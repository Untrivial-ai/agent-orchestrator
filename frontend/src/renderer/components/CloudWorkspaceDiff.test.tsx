// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CloudFileContentPane, CloudWorkspaceDiff } from "./CloudWorkspaceDiff";
import { TooltipProvider } from "./ui/tooltip";
import type { WorkspaceSession } from "../types/workspace";

const { cloudState, getWorkspaceDiff, readWorkspaceDiffFile } = vi.hoisted(() => ({
	cloudState: { ready: true },
	getWorkspaceDiff: vi.fn(),
	readWorkspaceDiffFile: vi.fn(),
}));

vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({
		baseUrl: "https://cloud.example.test",
		client: { getWorkspaceDiff, readWorkspaceDiffFile },
		ready: cloudState.ready,
	}),
}));

const session: WorkspaceSession = {
	branch: "ao/cloud-diff",
	cloud: { orgId: "org-1", sandboxProvider: "docker" },
	id: "session-1",
	prs: [],
	provider: "claude-code",
	status: "working",
	title: "Cloud diff",
	updatedAt: "2026-09-20T00:00:00Z",
	workspaceId: "workspace-1",
	workspaceName: "Cloud workspace",
};

function renderDiff(onOpenFile = vi.fn(), sessionInput = session) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return {
		onOpenFile,
		...render(
			<QueryClientProvider client={queryClient}>
				<TooltipProvider>
					<CloudWorkspaceDiff onOpenFile={onOpenFile} session={sessionInput} />
				</TooltipProvider>
			</QueryClientProvider>,
		),
	};
}

describe("CloudWorkspaceDiff", () => {
	afterEach(() => {
		cloudState.ready = true;
		getWorkspaceDiff.mockReset();
		readWorkspaceDiffFile.mockReset();
	});

	it("does not present an empty diff while the cloud client is still resolving", () => {
		cloudState.ready = false;
		renderDiff();

		expect(screen.getByText("Loading files...")).toBeInTheDocument();
		expect(screen.queryByLabelText("Cloud diff summary")).not.toBeInTheDocument();
		expect(getWorkspaceDiff).not.toHaveBeenCalled();
	});

	it("opens a selected cloud diff file in the center file workspace", async () => {
		getWorkspaceDiff.mockResolvedValue({
			diffBaseRef: "HEAD",
			files: [{ additions: 3, binary: false, deletions: 1, path: "src/App.tsx", status: "modified" }],
			truncated: { combined: false, stats: false },
		});
		const { onOpenFile } = renderDiff();

		await userEvent.click(await screen.findByRole("button", { name: /src\/App\.tsx/ }));

		expect(onOpenFile).toHaveBeenCalledWith("src/App.tsx");
	});

	it("shows Coder workspace changes through the shared diff endpoint", async () => {
		getWorkspaceDiff.mockResolvedValue({
			diffBaseRef: "HEAD",
			files: [{ additions: 1, binary: false, deletions: 0, path: "src/Coder.ts", status: "added" }],
			truncated: { combined: false, stats: false },
		});
		const coderSession = { ...session, cloud: { orgId: "org-1", sandboxProvider: "coder" } };
		renderDiff(vi.fn(), coderSession);

		expect(await screen.findByRole("button", { name: /src\/Coder\.ts/ })).toBeInTheDocument();
		expect(getWorkspaceDiff).toHaveBeenCalledWith("org-1", "session-1");
	});

	it("switches a cloud center file between its complete content and diff", async () => {
		readWorkspaceDiffFile.mockResolvedValue({
			additions: 3,
			binary: false,
			content: "export const answer = 42;\n",
			contentTruncated: false,
			deleted: false,
			deletions: 1,
			diff: "@@ -1 +1 @@\n-old\n+export const answer = 42;",
			diffTruncated: false,
			path: "src/App.tsx",
			size: 26,
			status: "modified",
		});
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudFileContentPane path="src/App.tsx" session={session} />
			</QueryClientProvider>,
		);

		expect(await screen.findByText("export const answer = 42;")).toBeInTheDocument();
		expect(readWorkspaceDiffFile).toHaveBeenCalledWith("org-1", "session-1", "src/App.tsx");

		await userEvent.click(screen.getByRole("tab", { name: "Diff" }));
		expect(await screen.findByText(/export const answer = 42/)).toBeInTheDocument();
	});
});
