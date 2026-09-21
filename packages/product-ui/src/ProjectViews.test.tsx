import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import type { ComponentProps } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	ProjectGeneralSettingsView,
	ProjectSourcePickerView,
	ProjectSetupFormView,
} from "./ProjectViews";
import { canSubmitProjectSetup, validateProjectSettings } from "./project-models";

const modeLabels = {
	title: "Import",
	description: "What would you like to import?",
	clone: "Clone from Git",
	cloneDescription: "Clone a remote repository",
	cloneExample: "github.com/acme/web-app",
	cloneBranchExample: "origin / main",
	local: "Open local repository",
	localDescription: "Choose a repository on this computer",
	localExample: "~/Development/web-app",
	localBranchExample: "main",
	workspace: "Workspace",
	workspaceDescription: "A folder containing repositories",
	close: "Close",
};

function ExternalLink(props: ComponentProps<"a">) {
	return <a {...props} />;
}

afterEach(() => cleanup());

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
	it("presents the controlled source choice with workspace as a secondary path", () => {
		const onSelect = vi.fn();
		render(<ProjectSourcePickerView disabled={false} labels={modeLabels} onSelect={onSelect} />);

		fireEvent.click(screen.getByRole("button", { name: "Clone from Git" }));
		expect(onSelect).toHaveBeenCalledWith("clone");
		fireEvent.click(screen.getByRole("button", { name: "Workspace" }));
		expect(onSelect).toHaveBeenCalledWith("workspace");
		expect(screen.getByText("github.com/acme/web-app")).toBeInTheDocument();
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
		expect(screen.queryByRole("button", { name: "Refresh" })).not.toBeInTheDocument();
	});

	it("can hide the normal refresh action while keeping an error retry", () => {
		const onRetry = vi.fn();
		render(
			<ProjectSetupFormView
				agentControls={{ worker: <span>Worker control</span>, orchestrator: <span>Orchestrator control</span> }}
				agents={{
					cacheMessage: "Cached",
					error: "Could not load agents",
					loading: false,
					loadingMessage: "Loading",
					onRetry,
					retrying: false,
					retryLabel: "Retry",
				}}
				canSubmit={false}
				intakeControl={<span>Intake control</span>}
				isBusy={false}
				onCancel={vi.fn()}
				onSubmit={vi.fn()}
				submitLabel="Create and start"
				cancelLabel="Cancel"
			/>,
		);

		expect(screen.queryByRole("button", { name: "Refresh" })).not.toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Retry" }));
		expect(onRetry).toHaveBeenCalledOnce();
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
