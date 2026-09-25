import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AutomationsView } from "./AutomationsView";

const mocks = vi.hoisted(() => ({
	automations: [] as Array<Record<string, unknown>>,
	create: vi.fn(),
	update: vi.fn(),
	runsError: null as Error | null,
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
const codexSnapshot = { id: "codex", label: "Codex", installation: { state: "installed", freshness: "fresh" }, authentication: { state: "authorized", freshness: "fresh" }, effectiveReadiness: "ready", usageCount: 3, lastUsedAt: null };
const cursorSnapshot = { id: "cursor", label: "Cursor", installation: { state: "not_installed", freshness: "fresh" }, authentication: { state: "unknown", freshness: "stale" }, effectiveReadiness: "not_ready", usageCount: 0, lastUsedAt: null };
vi.mock("../hooks/useAgentReadinessQuery", () => ({ useAgentReadinessQuery: () => ({ data: { agents: [codexSnapshot, cursorSnapshot] } }) }));
vi.mock("../hooks/useProjectDefaultWorker", () => ({ useProjectDefaultWorker: () => "codex" }));
vi.mock("../hooks/useWorkspaceQuery", () => ({ useWorkspaceQuery: () => ({ data: [{ id: "demo", name: "Demo" }] }) }));
vi.mock("../hooks/useAgentModelsQuery", () => ({
	agentModelsQueryKey: (agentId: string, projectId: string) => ["agent-models", agentId, projectId],
	agentModelsQueryOptions: (agentId: string, projectId: string) => ({
		queryKey: ["agent-models", agentId, projectId],
		queryFn: async () => ({
			models: [{ id: "gpt-5", label: "GPT-5", isDefault: true }],
			allowCustom: false,
			customModelEntry: "none",
			selectionMode: "catalog",
		}),
		enabled: agentId !== "",
	}),
	refreshAgentModels: vi.fn(),
	revalidateAgentModels: vi.fn(),
}));
vi.mock("../hooks/useAutomations", () => ({
	useAutomations: () => ({ data: mocks.automations, isLoading: false, error: null }),
	useCreateAutomation: () => ({ mutateAsync: mocks.create, isPending: false, error: null }),
	useUpdateAutomation: () => ({ mutateAsync: mocks.update, isPending: false, error: null }),
	useDeleteAutomation: () => ({ mutate: vi.fn(), isPending: false, error: null }),
	useAutomationRuns: () => ({ data: [], isLoading: false, error: mocks.runsError }),
}));

function renderView(ui: ReactNode = <AutomationsView />) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("AutomationsView", () => {
	beforeEach(() => { mocks.automations = []; mocks.runsError = null; mocks.create.mockReset(); mocks.update.mockReset(); });

	it("shows a discoverable empty state and create action", () => {
		renderView();
		expect(screen.getByRole("heading", { name: "Automations" })).toBeInTheDocument();
		expect(screen.getByText("No automations yet")).toBeInTheDocument();
		expect(screen.getAllByRole("button", { name: /create automation/i })).not.toHaveLength(0);
	});

	it("preselects the project's resolved default worker like New Task", async () => {
		renderView();
		await userEvent.click(screen.getAllByRole("button", { name: /create automation/i })[0]);

		const dialog = screen.getByRole("dialog", { name: "Create automation" });
		expect(within(dialog).getByRole("combobox", { name: "Agent" })).toHaveTextContent("Codex");
		expect(within(dialog).getByRole("button", { name: "Model" })).toBeInTheDocument();
		expect(screen.queryByRole("option", { name: "Project default" })).not.toBeInTheDocument();
		expect(screen.queryByRole("combobox", { name: "Session kind" })).not.toBeInTheDocument();
	});

	it("edits an automation from its card action", async () => {
		const user = userEvent.setup();
		mocks.automations = [{ id: "automation-1", projectId: "demo", displayName: "Morning triage", prompt: "Review", kind: "worker", harness: "codex", rrule: "FREQ=DAILY;BYHOUR=9;BYMINUTE=30;BYSECOND=0", timezone: "UTC", enabled: true, nextRunAt: "2026-08-27T09:00:00Z", createdAt: "2026-08-26T09:00:00Z", updatedAt: "2026-08-26T09:00:00Z" }];
		renderView();
		await user.click(screen.getByRole("button", { name: "Edit Morning triage" }));

		const dialog = screen.getByRole("dialog", { name: "Edit automation" });
		expect(within(dialog).getByRole("textbox", { name: "Name" })).toHaveValue("Morning triage");
		expect(within(dialog).getByRole("textbox", { name: "Prompt" })).toHaveValue("Review");
		expect(within(dialog).getByRole("combobox", { name: "Project" })).toBeDisabled();
		expect(within(dialog).getByRole("combobox", { name: "Agent" })).toHaveTextContent("Codex");
		expect(within(dialog).getByLabelText("Time")).toHaveValue("09:30");

		const name = within(dialog).getByRole("textbox", { name: "Name" });
		await user.clear(name);
		await user.type(name, "Evening triage");
		await user.click(within(dialog).getByRole("button", { name: "Save changes" }));

		expect(mocks.update).toHaveBeenCalledWith({
			id: "automation-1",
			body: expect.objectContaining({
				displayName: "Evening triage",
				prompt: "Review",
				harness: "codex",
				rrule: "FREQ=DAILY;BYHOUR=9;BYMINUTE=30;BYSECOND=0",
			}),
		});
		expect(mocks.update.mock.calls[0][0].body).not.toHaveProperty("projectId");
		expect(mocks.update.mock.calls[0][0].body).not.toHaveProperty("timezone");
	});

	it("preserves a custom recurrence rule when only non-schedule fields change", async () => {
		const user = userEvent.setup();
		const rrule = "DTSTART:20260826T090000Z\nRRULE:FREQ=DAILY;INTERVAL=2;BYHOUR=9;BYMINUTE=0;BYSECOND=0";
		mocks.automations = [{ id: "automation-1", projectId: "demo", displayName: "Every other day", prompt: "Review", kind: "worker", harness: "codex", rrule, timezone: "UTC", enabled: true, nextRunAt: "2026-08-27T09:00:00Z", createdAt: "2026-08-26T09:00:00Z", updatedAt: "2026-08-26T09:00:00Z" }];
		renderView();
		await user.click(screen.getByRole("button", { name: "Edit Every other day" }));

		const dialog = screen.getByRole("dialog", { name: "Edit automation" });
		expect(within(dialog).getByRole("combobox", { name: "Schedule" })).toHaveTextContent("Custom RRule");
		expect((within(dialog).getByRole("textbox", { name: "RRule" }) as HTMLInputElement).value).toContain("INTERVAL=2");
		const name = within(dialog).getByRole("textbox", { name: "Name" });
		await user.clear(name);
		await user.type(name, "Renamed");
		await user.click(within(dialog).getByRole("button", { name: "Save changes" }));

		expect(mocks.update).toHaveBeenCalledWith({
			id: "automation-1",
			body: expect.objectContaining({
				displayName: "Renamed",
				rrule,
			}),
		});
	});

	it("uses AO popup controls instead of native browser selects", async () => {
		const view = renderView();
		await userEvent.click(screen.getAllByRole("button", { name: /create automation/i })[0]);

		const dialog = screen.getByRole("dialog", { name: "Create automation" });
		expect(dialog.querySelector('select:not([aria-hidden="true"])')).toBeNull();
		expect(within(dialog).getByRole("combobox", { name: "Project" })).toBeInTheDocument();
		expect(within(dialog).getByRole("combobox", { name: "Schedule" })).toBeInTheDocument();
		expect(within(dialog).getByRole("combobox", { name: "Agent" })).toBeInTheDocument();
		expect(within(dialog).getByRole("button", { name: "Model" })).toBeInTheDocument();
		expect(view.container.querySelector('[data-slot="select-content"]')).toBeNull();
	});

	it("uses a 24-hour text time field that only accepts clock digits", async () => {
		const user = userEvent.setup();
		renderView();
		await user.click(screen.getAllByRole("button", { name: /create automation/i })[0]);

		const dialog = screen.getByRole("dialog", { name: "Create automation" });
		const time = within(dialog).getByLabelText("Time");
		expect(time).toHaveAttribute("type", "text");
		expect(time).toHaveAttribute("inputMode", "numeric");
		await user.clear(time);
		await user.type(time, "0705");
		expect(time).toHaveValue("07:05");
	});

	it("rejects out-of-range time digits while typing", async () => {
		const user = userEvent.setup();
		renderView();
		await user.click(screen.getAllByRole("button", { name: /create automation/i })[0]);

		const time = within(screen.getByRole("dialog", { name: "Create automation" })).getByLabelText("Time");
		await user.clear(time);
		await user.type(time, "9999");
		expect(time).toHaveValue("09");
		await user.clear(time);
		await user.type(time, "2560");
		expect(time).toHaveValue("20");
		await user.clear(time);
		await user.type(time, "2360");
		expect(time).toHaveValue("23:0");
		await user.clear(time);
		await user.type(time, "2359");
		expect(time).toHaveValue("23:59");
	});

	it("rejects an empty time before create", async () => {
		const user = userEvent.setup();
		renderView();
		await user.click(screen.getAllByRole("button", { name: /create automation/i })[0]);

		const time = screen.getByLabelText("Time");
		await user.clear(time);
		await user.click(screen.getByRole("button", { name: "Create automation" }));

		expect(time).toBeInvalid();
		expect(time).toHaveAccessibleDescription("Choose a valid time.");
		expect(mocks.create).not.toHaveBeenCalled();
	});

	it("uses the shared onboarding dialog frame without header/footer hairlines", async () => {
		renderView();
		await userEvent.click(screen.getAllByRole("button", { name: /create automation/i })[0]);

		const dialog = screen.getByRole("dialog", { name: "Create automation" });
		expect(dialog).toHaveClass("border-border", "bg-popover", "rounded-lg");
		expect(dialog.querySelector(".border-b")).toBeNull();
		expect(dialog.querySelector(".border-t")).toBeNull();
		expect(within(dialog).getByRole("heading", { name: "Create automation" })).toHaveClass(
			"settings-dialog-title",
			"px-4",
			"pt-3",
		);
	});

	it("shows AO-styled inline feedback when required fields are missing", async () => {
		const user = userEvent.setup();
		renderView();
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
		renderView();
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
		renderView();
		await user.click(screen.getAllByRole("button", { name: /create automation/i })[0]);
		await user.click(screen.getByRole("button", { name: "Create automation" }));

		const name = screen.getByRole("textbox", { name: "Name" });
		expect(name).toHaveAccessibleDescription("Enter a name.");
		await user.type(name, " ");
		expect(name).toHaveAccessibleDescription("Enter a name.");
		await user.type(name, "Morning");
		expect(screen.queryByText("Enter a name.")).not.toBeInTheDocument();
	});

	it("defaults create time to the current local clock", async () => {
		vi.useFakeTimers({ shouldAdvanceTime: true });
		vi.setSystemTime(new Date(2026, 8, 22, 14, 7, 0));
		renderView();
		await userEvent.click(screen.getAllByRole("button", { name: /create automation/i })[0]);
		expect(screen.getByLabelText("Time")).toHaveValue("14:07");
		vi.useRealTimers();
	});

	it("surfaces run-history fetch failures instead of an empty list", async () => {
		mocks.automations = [{ id: "automation-1", projectId: "demo", displayName: "Morning triage", prompt: "Review", kind: "worker", harness: "codex", rrule: "FREQ=DAILY", timezone: "UTC", enabled: true, nextRunAt: "2026-08-27T09:00:00Z", createdAt: "2026-08-26T09:00:00Z", updatedAt: "2026-08-26T09:00:00Z" }];
		mocks.runsError = new Error("daemon unavailable");
		renderView();
		await userEvent.click(screen.getByRole("button", { name: /show run history/i }));
		expect(screen.getByRole("alert")).toHaveTextContent("daemon unavailable");
	});
});
