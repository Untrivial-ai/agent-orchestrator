import { fireEvent, render, screen } from "@testing-library/react";
import type { ComponentProps } from "react";
import { describe, expect, it, vi } from "vitest";
import {
	ProjectGeneralSettingsView,
	ProjectModePickerView,
	ProjectSetupFormView,
} from "./ProjectViews";
import { canSubmitProjectSetup, validateProjectSettings } from "./project-models";

const modeLabels = {
	title: "Import",
	description: "What would you like to import?",
	workspace: "Workspace",
	workspaceDescription: "A folder containing repositories",
	project: "Project",
	projectDescription: "A single repository",
	close: "Close",
	workspaceExample: "my-workspace/",
	workspaceRepositories: ["web-app", "api-server", "shared-libs"] as [string, string, string],
	projectExample: "web-app",
	projectBranchExample: "main",
};

function ExternalLink(props: ComponentProps<"a">) {
	return <a {...props} />;
}

describe("project models", () => {
	it("validates settings in user-action order", () => {
		expect(
			validateProjectSettings({
				displayName: "",
				workerAgent: "",
				orchestratorAgent: "",
				intakeEnabled: true,
				intakeAssignee: "",
			}),
		).toBe("agents_required");
		expect(
			validateProjectSettings({
				displayName: "Project",
				workerAgent: "codex",
				orchestratorAgent: "claude-code",
				intakeEnabled: true,
				intakeAssignee: "",
			}),
		).toBe("intake_assignee_required");
	});

	it("gates project setup on agents and intake eligibility", () => {
		expect(canSubmitProjectSetup({ workerAgent: "codex", orchestratorAgent: "claude-code" })).toBe(true);
		expect(
			canSubmitProjectSetup({
				workerAgent: "codex",
				orchestratorAgent: "claude-code",
				intakeEnabled: true,
			}),
		).toBe(false);
	});

});

describe("project presentation", () => {
	it("presents the controlled project/workspace choice", () => {
		const onSelect = vi.fn();
		render(<ProjectModePickerView disabled={false} labels={modeLabels} onSelect={onSelect} />);

		fireEvent.click(screen.getByRole("button", { name: "Workspace" }));
		expect(onSelect).toHaveBeenCalledWith("workspace");
		expect(screen.getByText("my-workspace/")).toBeInTheDocument();
	});

	it("submits the controlled setup form and exposes setup feedback", () => {
		const onSubmit = vi.fn();
		render(
			<ProjectSetupFormView
				agentControls={{ worker: <span>Worker control</span>, orchestrator: <span>Orchestrator control</span> }}
				agents={{
					cacheMessage: "Cached",
					loading: false,
					loadingMessage: "Loading",
					onRefresh: vi.fn(),
					refreshLabel: "Refresh",
					refreshing: false,
					retryLabel: "Retry",
				}}
				canSubmit
				intakeControl={<span>Intake control</span>}
				isBusy={false}
				onCancel={vi.fn()}
				onSubmit={onSubmit}
				setupNotice={{ message: "Git setup required", warning: "Nested repository" }}
				submitLabel="Create and start"
				cancelLabel="Cancel"
			/>,
		);

		fireEvent.click(screen.getByRole("button", { name: "Create and start" }));
		expect(onSubmit).toHaveBeenCalledOnce();
		expect(screen.getByText("Nested repository")).toBeInTheDocument();
	});

	it("renders project identity and workspace repository summaries", () => {
		render(
			<ProjectGeneralSettingsView
				displayName="Workspace"
				externalLink={ExternalLink}
				labels={{
					title: "Identity",
					name: "Project name",
					id: "ID",
					kind: "Kind",
					path: "Path",
					repo: "Repository",
					workspaceRepos: "Workspace repositories",
					workspaceReposEmpty: "No repositories",
					editName: "Edit Project name",
				}}
				onDisplayNameChange={vi.fn()}
				project={{
					id: "workspace-1",
					kindLabel: "Workspace",
					path: "/repo",
					pathHref: "file:///repo",
					repo: "https://github.com/acme/workspace",
					repoHref: "https://github.com/acme/workspace",
					workspaceRepos: [{ name: "web", relativePath: "apps/web", repo: "acme/web" }],
				}}
			/>,
		);

		fireEvent.click(screen.getByRole("button", { name: "Edit Project name" }));
		expect(screen.getByLabelText("Project name")).toHaveValue("Workspace");
		expect(screen.getByRole("link", { name: "/repo" })).toHaveAttribute("title", "/repo");
		expect(screen.getByRole("link", { name: "https://github.com/acme/workspace" })).toHaveAttribute(
			"title",
			"https://github.com/acme/workspace",
		);
		expect(screen.getByText("apps/web · acme/web")).toBeInTheDocument();
	});

});
