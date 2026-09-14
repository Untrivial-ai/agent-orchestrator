import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CloudProjectSettingsForm } from "./CloudProjectSettingsForm";

const { cloudState, listProjectsMock } = vi.hoisted(() => ({
	cloudState: { ready: true, org: { id: "org-1" } as { id: string } | undefined },
	listProjectsMock: vi.fn(),
}));

vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({
		client: { listProjects: listProjectsMock },
		ready: cloudState.ready,
		baseUrl: "https://cp.example.com",
	}),
}));

vi.mock("../hooks/useCloudOrg", () => ({
	useCloudOrg: () => ({ org: cloudState.org, isLoading: false, error: undefined, ready: cloudState.ready }),
}));

function renderForm(projectId = "proj-1", section: "general" | "agents" | "workflow" | "intake" = "general") {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<CloudProjectSettingsForm projectId={projectId} section={section} />
		</QueryClientProvider>,
	);
}

describe("CloudProjectSettingsForm", () => {
	beforeEach(() => {
		cloudState.ready = true;
		cloudState.org = { id: "org-1" };
		listProjectsMock.mockReset().mockResolvedValue({
			items: [
				{
					id: "proj-1",
					orgId: "org-1",
					displayName: "My Cloud Project",
					repositoryUrl: "https://github.com/acme/repo",
					defaultBranch: "main",
					config: {},
					createdAt: "2026-01-01T00:00:00Z",
					updatedAt: "2026-01-01T00:00:00Z",
				},
			],
			page: {},
		});
	});

	it("loads the project from the control plane instead of the local daemon", async () => {
		renderForm();

		expect(await screen.findByText("My Cloud Project")).toBeInTheDocument();
		expect(screen.getByText("https://github.com/acme/repo")).toBeInTheDocument();
		expect(screen.getByText("main")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /edit/i })).not.toBeInTheDocument();
	});

	it("renders cloud project config in read-only project-spec sections", async () => {
		listProjectsMock.mockResolvedValueOnce({
			items: [{
				id: "proj-1", orgId: "org-1", displayName: "My Cloud Project",
				repositoryUrl: "https://github.com/acme/repo", defaultBranch: "main",
				config: {
					worker: { agent: "codex" }, orchestrator: { agent: "claude-code" },
					reviewers: [{ harness: "opencode" }], sessionPrefix: "AO", autoReview: true,
					trackerIntake: { enabled: true, repo: "acme/repo", assignee: "octo" },
				},
				createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z",
			}],
			page: {},
		});
		renderForm("proj-1", "agents");
		expect(await screen.findByText("codex")).toBeInTheDocument();
		expect(screen.getByText("claude-code")).toBeInTheDocument();
		expect(screen.getByText("opencode")).toBeInTheDocument();
	});
});
