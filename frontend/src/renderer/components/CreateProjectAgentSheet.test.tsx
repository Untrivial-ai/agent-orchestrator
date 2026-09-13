import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { agentReadinessQueryKey } from "../hooks/useAgentReadinessQuery";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { agentReadiness } from "../test/agent-readiness-fixtures";
import {
	CreateProjectAgentSheet,
	defaultAuthorizedAgent,
	defaultAuthorizedAgentForRole,
	RequiredAgentField,
} from "./CreateProjectAgentSheet";
import { TooltipProvider } from "./ui/tooltip";

function renderSheet(
	onSubmit = vi.fn().mockResolvedValue(undefined),
	queryClient?: QueryClient,
	options: { shake?: boolean } = {},
) {
	queryClient ??= new QueryClient({ defaultOptions: { queries: { retry: false } } });
	if (queryClient.getQueryData(agentReadinessQueryKey) === undefined) {
		queryClient.setQueryData(agentReadinessQueryKey, {
			agents: [agentReadiness("claude-code"), agentReadiness("codex")],
		});
	}
	if (queryClient.getQueryData(workspaceQueryKey) === undefined) {
		queryClient.setQueryData(workspaceQueryKey, []);
	}
	render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<CreateProjectAgentSheet
					isCreating={false}
					kind="single_repo"
					onOpenChange={() => undefined}
					onSubmit={onSubmit}
					open={true}
					path="/repo/new-project"
					shake={options.shake}
				/>
			</TooltipProvider>
		</QueryClientProvider>,
	);
	return onSubmit;
}

async function chooseOption(trigger: HTMLElement, optionName: string) {
	await userEvent.click(trigger);
	const escaped = optionName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
	await userEvent.click(await screen.findByRole("option", { name: new RegExp(escaped, "i") }));
}

describe("CreateProjectAgentSheet", () => {
	it("shakes the active sheet when creation fails", () => {
		renderSheet(undefined, undefined, { shake: true });

		expect(screen.getByRole("dialog")).toHaveClass("modal-shake");
	});

	it("chooses the highest-priority authorized default agent", () => {
		expect(
			defaultAuthorizedAgent([
				agentReadiness("opencode", "OpenCode"),
				agentReadiness("codex", "Codex"),
			]),
		).toBe("codex");
	});

	it("chooses the most frequently used authorized agent by default", () => {
		expect(
			defaultAuthorizedAgent([
				agentReadiness("claude-code", "Claude Code", { usageCount: 1 }),
				agentReadiness("codex", "Codex", { usageCount: 3 }),
			]),
		).toBe("codex");
	});

	it("falls back to the alphabetically first authorized agent when no priority agent is authorized", () => {
		expect(
			defaultAuthorizedAgent([
				agentReadiness("goose", "Goose"),
				agentReadiness("devin", "Devin"),
			]),
		).toBe("devin");
	});

	it("chooses worker and orchestrator defaults from their own session history", () => {
		const agents = [
			agentReadiness("claude-code", "Claude Code"),
			agentReadiness("codex", "Codex"),
		];
		const sessions = [
			{ kind: "worker" as const, provider: "codex", createdAt: "2026-08-01T10:00:00Z" },
			{ kind: "worker" as const, provider: "codex", createdAt: "2026-08-02T10:00:00Z" },
			{ kind: "worker" as const, provider: "claude-code", createdAt: "2026-08-03T10:00:00Z" },
			{ kind: "orchestrator" as const, provider: "claude-code", createdAt: "2026-08-04T10:00:00Z" },
		];

		expect(defaultAuthorizedAgentForRole(agents, sessions, "worker")).toBe("codex");
		expect(defaultAuthorizedAgentForRole(agents, sessions, "orchestrator")).toBe("claude-code");
	});

	it("breaks equal role usage counts by the most recent matching session", () => {
		const agents = [
			agentReadiness("claude-code", "Claude Code"),
			agentReadiness("codex", "Codex"),
		];
		const sessions = [
			{ kind: "worker" as const, provider: "claude-code", createdAt: "2026-08-01T10:00:00Z" },
			{ kind: "worker" as const, provider: "codex", createdAt: "2026-08-02T10:00:00Z" },
		];

		expect(defaultAuthorizedAgentForRole(agents, sessions, "worker")).toBe("codex");
	});

	it("ignores unavailable historical winners and falls back to Claude Code with no eligible history", () => {
		const agents = [
			agentReadiness("claude-code", "Claude Code"),
			agentReadiness("codex", "Codex"),
		];
		const unavailableWinner = Array.from({ length: 4 }, (_, index) => ({
			kind: "worker" as const,
			provider: "goose",
			createdAt: `2026-08-0${index + 1}T10:00:00Z`,
		}));

		expect(defaultAuthorizedAgentForRole(agents, unavailableWinner, "worker")).toBe("claude-code");
		expect(
			defaultAuthorizedAgentForRole(
				[agentReadiness("codex", "Codex")],
				[],
				"worker",
			),
		).toBe("codex");
	});

	it("uses the compact trigger size for agent fields", () => {
		render(
			<RequiredAgentField
				id="agent"
				label="Agent"
				onChange={() => undefined}
				placeholder="Project default"
				value="claude-code"
			/>,
		);

		expect(screen.getByLabelText("Agent")).toHaveAttribute("data-size", "sm");
	});

	it("caps the agent menu height with a theme token", async () => {
		render(
			<RequiredAgentField id="agent" label="Agent" onChange={() => undefined} placeholder="Project default" value="" />,
		);

		await userEvent.click(screen.getByLabelText("Agent"));

		expect(await screen.findByRole("listbox")).toHaveClass("max-h-select-menu-max!");
	});

	it("creates without intake when the toggle is left off", async () => {
		const onSubmit = renderSheet();

		expect(screen.getByRole("dialog")).not.toHaveTextContent("/repo/new-project");
		expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Create and start" }));

		await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
		expect(onSubmit).toHaveBeenCalledWith({
			workerAgent: "claude-code",
			orchestratorAgent: "claude-code",
			trackerIntake: undefined,
		});
	});

	it("submits independent defaults from cached worker and orchestrator history", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		queryClient.setQueryData(workspaceQueryKey, [
			{
				sessions: [
					{ kind: "worker", provider: "codex", createdAt: "2026-08-01T10:00:00Z" },
					{ kind: "worker", provider: "codex", createdAt: "2026-08-02T10:00:00Z" },
					{ kind: "orchestrator", provider: "claude-code", createdAt: "2026-08-03T10:00:00Z" },
				],
			},
		]);
		const onSubmit = renderSheet(vi.fn().mockResolvedValue(undefined), queryClient);

		await userEvent.click(screen.getByRole("button", { name: "Create and start" }));

		await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
		expect(onSubmit).toHaveBeenCalledWith({
			workerAgent: "codex",
			orchestratorAgent: "claude-code",
			trackerIntake: undefined,
		});
	});

	it("does not replace a manually selected role when history refreshes", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		queryClient.setQueryData(workspaceQueryKey, [
			{
				sessions: [
					{ kind: "worker", provider: "claude-code", createdAt: "2026-08-01T10:00:00Z" },
				],
			},
		]);
		const onSubmit = renderSheet(vi.fn().mockResolvedValue(undefined), queryClient);
		await chooseOption(screen.getByLabelText("Worker agent"), "codex");

		queryClient.setQueryData(workspaceQueryKey, [
			{
				sessions: [
					{ kind: "worker", provider: "claude-code", createdAt: "2026-08-02T10:00:00Z" },
					{ kind: "worker", provider: "claude-code", createdAt: "2026-08-03T10:00:00Z" },
				],
			},
		]);
		await userEvent.click(screen.getByRole("button", { name: "Create and start" }));

		await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
		expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ workerAgent: "codex" }));
	});

	it("does not show a manual agent catalog refresh action", () => {
		renderSheet();

		expect(screen.queryByRole("button", { name: "Refresh agents" })).not.toBeInTheDocument();
	});

	it("blocks submit when intake is enabled with no assignee, then passes the intake payload once one is set", async () => {
		const onSubmit = renderSheet();
		await chooseOption(screen.getByLabelText("Worker agent"), "claude-code");
		await chooseOption(screen.getByLabelText("Orchestrator agent"), "codex");

		await userEvent.click(screen.getByLabelText("Automatically work on assigned issues"));
		// Enabled with no eligibility rule → submit stays disabled (compact sheet
		// carries no inline guard prose; gating is the disabled button).
		expect(screen.getByRole("button", { name: "Create and start" })).toBeDisabled();

		await userEvent.type(screen.getByLabelText("Assignee"), "octocat");
		await userEvent.click(screen.getByRole("button", { name: "Create and start" }));

		await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
		expect(onSubmit).toHaveBeenCalledWith({
			workerAgent: "claude-code",
			orchestratorAgent: "codex",
			trackerIntake: { enabled: true, assignee: "octocat" },
		});
	});

	it("keeps the create sheet minimal: no repo row or credential hint", async () => {
		renderSheet();
		// The compact setup control uses the shared switch styling; descriptive prose is not shown.
		expect(screen.getByLabelText("Automatically work on assigned issues")).toBeInTheDocument();
		expect(screen.queryByText(/Auto-spawn worker sessions from matching tracker issues/)).not.toBeInTheDocument();

		await userEvent.click(screen.getByLabelText("Automatically work on assigned issues"));
		expect(screen.queryByText("Repository")).not.toBeInTheDocument();
		expect(screen.queryByText(/Reads credentials from/)).not.toBeInTheDocument();
	});
});
