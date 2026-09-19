import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "./ui/tooltip";

const { updateProjectMock } = vi.hoisted(() => ({ updateProjectMock: vi.fn() }));

const project = {
	id: "cloud-project-1",
	orgId: "org-1",
	displayName: "Cloud Widgets",
	repositoryUrl: "https://github.com/octo/widgets.git",
	defaultBranch: "main",
	config: {
		unknownSetting: "retained",
		worker: { agent: "codex" as const, agentConfig: { model: "gpt-5", effort: "high" } },
		orchestrator: { agent: "claude-code" as const },
		reviewers: [{ harness: "cursor" as const }],
	},
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
};

vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({ client: { updateProject: updateProjectMock } }),
}));

vi.mock("../hooks/useCloudOrg", () => ({
	useCloudOrg: () => ({ org: { id: "org-1" } }),
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	cloudProjectsQueryKey: ["cloud-projects"],
	workspaceQueryKey: ["workspaces"],
	useCloudProjectsQuery: () => ({ data: [project], isFetching: false }),
}));

import { CloudProjectSettingsForm } from "./CloudProjectSettingsForm";

function renderSettings() {
	const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
	render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<CloudProjectSettingsForm projectId={project.id} section="agents" />
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

async function chooseOption(trigger: HTMLElement, optionName: string) {
	await userEvent.click(trigger);
	await userEvent.click(await screen.findByRole("menuitem", { name: optionName }));
}

describe("CloudProjectSettingsForm", () => {
	it("saves cloud agent and review settings without dropping unknown config", async () => {
		updateProjectMock.mockResolvedValue({ project });
		renderSettings();

		await chooseOption(await screen.findByRole("button", { name: "Default worker agent" }), "Claude Code");
		await userEvent.click(screen.getByRole("button", { name: "Edit Worker model" }));
		const model = screen.getByRole("textbox", { name: "Worker model" });
		await userEvent.clear(model);
		await userEvent.type(model, "claude-sonnet");
		fireEvent.blur(model);
		await userEvent.click(screen.getByRole("switch", { name: "Auto review PRs" }));

		fireEvent.submit(document.getElementById("project-settings-form")!);

		await waitFor(() => expect(updateProjectMock).toHaveBeenCalledTimes(1));
		expect(updateProjectMock).toHaveBeenCalledWith("org-1", project.id, {
			displayName: "Cloud Widgets",
			defaultBranch: "main",
			config: expect.objectContaining({
				unknownSetting: "retained",
				worker: { agent: "claude-code", agentConfig: { model: "claude-sonnet", effort: "high" } },
				autoReview: true,
			}),
		});
	});
});
