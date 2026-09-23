import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";

vi.mock("@aoagents/product-ui", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@aoagents/product-ui")>();
	return {
		...actual,
		SessionCardView: ({ action }: { action?: ReactNode }) => <div>{action}</div>,
	};
});
vi.mock("../hooks/useSessionScmSummary", () => ({ useSessionScmSummary: () => ({ data: undefined }) }));
vi.mock("../hooks/useTerminateSession", () => ({
	clearTerminateSessionState: vi.fn(),
	useTerminateSessionState: () => ({ isPending: false }),
}));
vi.mock("../hooks/useReapplyPreservedEdits", () => ({ useReapplyPreservedEdits: () => vi.fn() }));

import { ArchivedSessionCardAdapter } from "./SessionsBoardAdapters";
import { TooltipProvider } from "./ui/tooltip";

const session: WorkspaceSession = {
	id: "session-1",
	workspaceId: "project-1",
	workspaceName: "Project",
	title: "Local worker",
	provider: "claude-code",
	status: "terminated",
	updatedAt: "2026-09-01T00:00:00Z",
	prs: [],
	hasPreservedEdits: true,
};

function renderArchivedCard(candidate: WorkspaceSession) {
	const queryClient = new QueryClient();
	return render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<ArchivedSessionCardAdapter
					isRestoreDisabled={false}
					isRestoring={false}
					restoreAction={vi.fn()}
					session={candidate}
				/>
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

describe("ArchivedSessionCardAdapter", () => {
	beforeEach(() => vi.clearAllMocks());

	it("does not expose saved-edit reapply for a live session", () => {
		renderArchivedCard({ ...session, isTerminated: false });
		expect(screen.queryByRole("button", { name: "Put saved edits back for Local worker" })).toBeNull();
	});

	it("exposes saved-edit reapply only for a terminated session", () => {
		renderArchivedCard({ ...session, isTerminated: true });
		expect(screen.getByRole("button", { name: "Put saved edits back for Local worker" })).toBeInTheDocument();
	});
});
