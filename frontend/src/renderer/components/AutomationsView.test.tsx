import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AutomationsView } from "./AutomationsView";

const mocks = vi.hoisted(() => ({
	automations: [] as Array<Record<string, unknown>>,
	create: vi.fn(),
	update: vi.fn(),
	runsError: null as Error | null,
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../hooks/useAgentReadinessQuery", () => ({ useAgentReadinessQuery: () => ({ data: { agents: [{ id: "codex", label: "Codex" }] } }) }));
vi.mock("../hooks/useWorkspaceQuery", () => ({ useWorkspaceQuery: () => ({ data: [{ id: "demo", name: "Demo" }] }) }));
vi.mock("../hooks/useAutomations", () => ({
	useAutomations: () => ({ data: mocks.automations, isLoading: false, error: null }),
	useCreateAutomation: () => ({ mutateAsync: mocks.create, isPending: false, error: null }),
	useUpdateAutomation: () => ({ mutateAsync: mocks.update, isPending: false, error: null }),
	useDeleteAutomation: () => ({ mutate: vi.fn(), isPending: false, error: null }),
	useAutomationRuns: () => ({ data: [], isLoading: false, error: mocks.runsError }),
}));

describe("AutomationsView", () => {
	beforeEach(() => { mocks.automations = []; mocks.runsError = null; mocks.create.mockReset(); mocks.update.mockReset(); });

	it("shows a discoverable empty state and create action", () => {
		render(<AutomationsView />);
		expect(screen.getByRole("heading", { name: "Automations" })).toBeInTheDocument();
		expect(screen.getByText("No automations yet")).toBeInTheDocument();
		expect(screen.getAllByRole("button", { name: /create automation/i })).not.toHaveLength(0);
	});

	it("offers the daemon agent catalog when creating an automation", async () => {
		render(<AutomationsView />);
		await userEvent.click(screen.getAllByRole("button", { name: /create automation/i })[0]);
		await userEvent.click(screen.getByRole("combobox", { name: "Agent" }));
		expect(await screen.findByRole("option", { name: "Project default" })).toBeInTheDocument();
		expect(screen.getByRole("option", { name: "Codex" })).toBeInTheDocument();
	});

	it("uses AO popup controls instead of native browser selects", async () => {
		const view = render(<AutomationsView />);
		await userEvent.click(screen.getAllByRole("button", { name: /create automation/i })[0]);

		const dialog = screen.getByRole("dialog", { name: "Create automation" });
		// Radix keeps an aria-hidden select for form semantics; only a visible
		// browser-native select would reintroduce the white Chromium popup.
		expect(dialog.querySelector('select:not([aria-hidden="true"])')).toBeNull();
		expect(within(dialog).getByRole("combobox", { name: "Project" })).toBeInTheDocument();
		expect(within(dialog).getByRole("combobox", { name: "Schedule" })).toBeInTheDocument();
		expect(view.container.querySelector('[data-slot="select-content"]')).toBeNull();
	});

	it("accepts and normalizes keyboard entry in AO-styled 24-hour fields", async () => {
		render(<AutomationsView />);
		await userEvent.click(screen.getAllByRole("button", { name: /create automation/i })[0]);

		const dialog = screen.getByRole("dialog", { name: "Create automation" });
		expect(dialog.querySelector('input[type="time"]')).toBeNull();
		const hour = within(dialog).getByRole("textbox", { name: "Hour" });
		const minute = within(dialog).getByRole("textbox", { name: "Minute" });
		await userEvent.clear(hour);
		await userEvent.type(hour, "7");
		await userEvent.tab();
		await userEvent.clear(minute);
		await userEvent.type(minute, "5");
		await userEvent.tab();

		expect(hour).toHaveValue("07");
		expect(minute).toHaveValue("05");
	});

	it("rejects out-of-range keyboard time values", async () => {
		render(<AutomationsView />);
		await userEvent.click(screen.getAllByRole("button", { name: /create automation/i })[0]);

		const hour = screen.getByRole("textbox", { name: "Hour" });
		const minute = screen.getByRole("textbox", { name: "Minute" });
		await userEvent.clear(hour);
		await userEvent.type(hour, "24");
		await userEvent.clear(minute);
		await userEvent.type(minute, "60");
		await userEvent.click(screen.getByRole("button", { name: "Create automation" }));

		expect(hour).toBeInvalid();
		expect(minute).toBeInvalid();
		expect(hour).toHaveAccessibleDescription("Enter an hour from 00 to 23.");
		expect(minute).toHaveAccessibleDescription("Enter minutes from 00 to 59.");
		expect(mocks.create).not.toHaveBeenCalled();
	});

	it("uses the shared AO settings-dialog frame", async () => {
		render(<AutomationsView />);
		await userEvent.click(screen.getAllByRole("button", { name: /create automation/i })[0]);

		expect(screen.getByRole("dialog", { name: "Create automation" })).toHaveClass(
			"border-[var(--color-border-settings-dialog)]",
			"bg-popover",
		);
	});

	it("shows AO-styled inline feedback when required fields are missing", async () => {
		const user = userEvent.setup();
		render(<AutomationsView />);
		await user.click(screen.getAllByRole("button", { name: /create automation/i })[0]);
		await user.click(screen.getByRole("button", { name: "Create automation" }));

		const dialog = screen.getByRole("dialog", { name: "Create automation" });
		const project = within(dialog).getByRole("combobox", { name: "Project" });
		expect(within(dialog).getByRole("alert")).toHaveTextContent("Complete the highlighted fields.");
		expect(project).toHaveAttribute("aria-invalid", "true");
		expect(project).toHaveAccessibleDescription("Select a project.");
		expect(within(dialog).getByRole("textbox", { name: "Name" })).toHaveAccessibleDescription("Enter a name.");
		expect(within(dialog).getByRole("textbox", { name: "Prompt" })).toHaveAccessibleDescription("Enter a prompt.");
		expect(mocks.create).not.toHaveBeenCalled();
	});

	it("clears a field error as soon as the field is corrected", async () => {
		const user = userEvent.setup();
		render(<AutomationsView />);
		await user.click(screen.getAllByRole("button", { name: /create automation/i })[0]);
		await user.click(screen.getByRole("button", { name: "Create automation" }));

		const project = screen.getByRole("combobox", { name: "Project" });
		expect(project).toHaveAttribute("aria-invalid", "true");
		await user.click(project);
		await user.click(screen.getByRole("option", { name: "Demo" }));

		expect(project).not.toHaveAttribute("aria-invalid");
		expect(screen.queryByText("Select a project.")).not.toBeInTheDocument();
	});

	it("keeps inline feedback until a text field has meaningful content", async () => {
		const user = userEvent.setup();
		render(<AutomationsView />);
		await user.click(screen.getAllByRole("button", { name: /create automation/i })[0]);
		await user.click(screen.getByRole("button", { name: "Create automation" }));

		const name = screen.getByRole("textbox", { name: "Name" });
		await user.type(name, "   ");
		expect(name).toHaveAccessibleDescription("Enter a name.");

		await user.type(name, "AO check");
		expect(name).not.toHaveAttribute("aria-invalid");
		expect(screen.queryByText("Enter a name.")).not.toBeInTheDocument();
	});

	it("exposes accessible toggle, delete, and run-history controls", () => {
		mocks.automations = [{ id: "automation-1", projectId: "demo", displayName: "Morning triage", prompt: "Review", kind: "worker", rrule: "FREQ=DAILY", timezone: "UTC", enabled: true, nextRunAt: "2026-08-27T09:00:00Z", createdAt: "2026-08-26T09:00:00Z", updatedAt: "2026-08-26T09:00:00Z" }];
		render(<AutomationsView />);
		expect(screen.getByRole("switch", { name: "Disable Morning triage" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Delete Morning triage" })).toHaveClass(
			"text-destructive",
			"hover:bg-destructive/10",
		);
		expect(screen.getByRole("button", { name: "Show run history for Morning triage" })).toBeInTheDocument();
	});

	it("shows run-history request failures instead of an empty history", async () => {
		mocks.automations = [{ id: "automation-1", projectId: "demo", displayName: "Morning triage", prompt: "Review", kind: "worker", rrule: "FREQ=DAILY", timezone: "UTC", enabled: true, nextRunAt: "2026-08-27T09:00:00Z", createdAt: "2026-08-26T09:00:00Z", updatedAt: "2026-08-26T09:00:00Z" }];
		mocks.runsError = new Error("Run history unavailable");
		render(<AutomationsView />);
		await userEvent.click(screen.getByRole("button", { name: "Show run history for Morning triage" }));
		expect(screen.getByRole("alert")).toHaveTextContent("Run history unavailable");
		expect(screen.queryByText("No runs yet.")).not.toBeInTheDocument();
	});

	it("starts with a clean form after cancellation", async () => {
		const user = userEvent.setup();
		render(<AutomationsView />);
		await user.click(screen.getAllByRole("button", { name: /create automation/i })[0]);
		await user.type(screen.getByRole("textbox", { name: "Name" }), "Temporary name");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await user.click(screen.getByRole("button", { name: "New automation" }));
		expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("");
	});
});
