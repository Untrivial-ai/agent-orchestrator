import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";
import { ProjectSummaryPanel } from "./ProjectSummaryPanel";

const navigate = vi.fn();
const writeDraft = vi.fn();
const refresh = vi.fn();
const useSummary = vi.fn();
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("../lib/chat-drafts", () => ({ writeChatComposerText: (...args: unknown[]) => writeDraft(...args) }));
vi.mock("../hooks/useProjectSummary", () => ({ useProjectSummary: (...args: unknown[]) => useSummary(...args) }));
vi.mock("../hooks/useWorkspaceQuery", () => ({ useWorkspaceQuery: () => ({ data: [{ id: "demo", sessions: [{ id: "demo-2", createdAt: "2026-09-14T09:00:00Z" }] }] }) }));

const orchestrator = {
	id: "demo-orch",
	workspaceId: "demo",
	workspaceName: "Demo",
	title: "Orchestrator",
	provider: "codex",
	kind: "orchestrator",
	mode: "chat",
	status: "working",
	createdAt: "2026-09-14T08:00:00Z",
	updatedAt: "2026-09-14T08:00:00Z",
	prs: [],
} as WorkspaceSession;

describe("ProjectSummaryPanel", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		useSummary.mockReturnValue({ data: { projectId: "demo", narrative: "Two workers are active.", activeWorkers: 2, completedWorkers: 1, generatedAt: "2026-09-14T08:00:00Z", sourceWatermark: "abc", needsAttention: [{ sessionId: "demo-2", sessionName: "API work", question: "Choose the response shape." }], outputs: [{ sessionId: "demo-3", sessionName: "UI work", kind: "pull_request", number: 42, url: "https://github.com/acme/demo/pull/42", state: "Checks passing" }] }, isLoading: false, isError: false, refresh: { mutate: refresh, isPending: false } });
	});

	it("navigates to workers and seeds canonical chat without sending", async () => {
		const user = userEvent.setup();
		const close = vi.fn();
		render(<ProjectSummaryPanel onClose={close} orchestrator={orchestrator} />);
		await user.click(screen.getByRole("button", { name: "API work" }));
		expect(navigate).toHaveBeenCalledWith({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId: "demo", sessionId: "demo-2" } });
		navigate.mockClear();
		await user.click(screen.getByRole("button", { name: "Discuss in chat" }));
		expect(writeDraft).toHaveBeenCalledWith({ sessionId: "demo-2", incarnation: "2026-09-14T09:00:00Z" }, "Regarding API work: Choose the response shape.");
		expect(navigate).toHaveBeenCalledWith({ to: "/projects/$projectId/sessions/$sessionId", params: { projectId: "demo", sessionId: "demo-2" } });
		expect(close).toHaveBeenCalled();
	});

	it("replaces project data and uses a full-width narrow-screen surface", () => {
		const { rerender } = render(<ProjectSummaryPanel onClose={() => {}} orchestrator={orchestrator} />);
		expect(screen.getByRole("complementary", { name: "Project summary" })).toHaveClass("absolute", "inset-0", "sm:relative");
		rerender(<ProjectSummaryPanel onClose={() => {}} orchestrator={{ ...orchestrator, workspaceId: "next", workspaceName: "Next" }} />);
		expect(useSummary).toHaveBeenLastCalledWith("next", true);
	});

	it("does not render a project output inventory", () => {
		render(<ProjectSummaryPanel onClose={() => {}} orchestrator={orchestrator} />);

		expect(screen.queryByText("Meaningful outputs")).not.toBeInTheDocument();
		expect(screen.queryByText("PR #42")).not.toBeInTheDocument();
	});
});
