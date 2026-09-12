import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

// The daemon answers a cold model-catalog read by running that agent's own CLI, so
// every entry here is a real subprocess on the user's machine. What the menu asks
// for is therefore a correctness question, not a performance one.
const requestedAgents: string[] = [];

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: async (_path: string, opts: { params: { path: { agent: string } } }) => {
			requestedAgents.push(opts.params.path.agent);
			return {
				data: { agentId: opts.params.path.agent, selectionMode: "catalog", models: [] },
				error: undefined,
			};
		},
		POST: async () => ({ data: {}, error: undefined }),
	},
	apiErrorMessage: () => "failed",
}));

import { ReviewerSelect } from "./ReviewerSelect";

function renderMenu(ui: ReactElement) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

// Give any stray fetch a chance to land before asserting on absence.
async function settle() {
	await new Promise((resolve) => setTimeout(resolve, 50));
}

describe("ReviewerSelect catalog reads", () => {
	beforeEach(() => {
		requestedAgents.length = 0;
	});

	// Opening the menu used to prefetch a catalog for all 21 reviewer harnesses,
	// running the CLI of every agent the user was not choosing — including agents
	// they had never signed in to.
	// https://github.com/Untrivial-ai/agent-orchestrator/issues/5307
	it("does not read a catalog for harnesses the user is not choosing", async () => {
		const user = userEvent.setup();
		renderMenu(<ReviewerSelect value="claude-code" onChange={vi.fn()} projectId="p1" />);

		await user.click(screen.getByRole("button", { name: "Default reviewer agent" }));
		await settle();

		expect(requestedAgents).not.toContain("kiro");
		expect(new Set(requestedAgents)).toEqual(new Set(["claude-code"]));
	});

	// The trigger has to name the selected model, so the harness in effect is the
	// one read AO is entitled to make.
	it("still reads the catalog of the harness in effect", async () => {
		renderMenu(<ReviewerSelect value="codex" onChange={vi.fn()} projectId="p1" />);
		await waitFor(() => expect(requestedAgents).toContain("codex"));
	});

	it("reads a harness catalog once its own submenu is opened", async () => {
		const user = userEvent.setup();
		renderMenu(<ReviewerSelect value="claude-code" onChange={vi.fn()} projectId="p1" />);

		await user.click(screen.getByRole("button", { name: "Default reviewer agent" }));
		await settle();
		expect(requestedAgents).not.toContain("kiro");

		// Hovering a sub-trigger is what opens it; clicking a harness that is not the
		// current one selects it instead.
		await user.hover(await screen.findByRole("menuitem", { name: /Kiro/ }));
		await waitFor(() => expect(requestedAgents).toContain("kiro"));
	});

	// Choosing a different reviewer must not become a reason to run some third
	// agent's binary.
	it("reads no further catalogs when a harness is selected", async () => {
		const onChange = vi.fn();
		const user = userEvent.setup();
		renderMenu(<ReviewerSelect value="claude-code" onChange={onChange} projectId="p1" />);

		await user.click(screen.getByRole("button", { name: "Default reviewer agent" }));
		await settle();
		requestedAgents.length = 0;

		await user.click(await screen.findByRole("menuitem", { name: /Codex/ }));
		await settle();

		expect(onChange).toHaveBeenCalledWith("codex");
		expect(requestedAgents).not.toContain("kiro");
	});
});
