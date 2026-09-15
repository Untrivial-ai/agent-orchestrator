import userEvent from "@testing-library/user-event";
import { render as rtlRender, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { describe, expect, it, vi } from "vitest";
import { ChatWorkspace } from "./ChatWorkspace";
import {
	chatFixture,
	chatFixtureMcpFailed,
	chatFixtureReauth,
	chatFixtureRerouted,
	chatFixtureThreadError,
} from "../../lib/chat-fixture";
import type { ConversationSnapshot } from "../../types/conversation";
import { TooltipProvider } from "../ui/tooltip";

function render(ui: ReactElement) {
	const result = rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
	return {
		...result,
		rerender: (nextUi: ReactElement) => result.rerender(<TooltipProvider>{nextUi}</TooltipProvider>),
	};
}

// The surface-level wiring: which snapshot fields produce which chrome, and — the part
// that is easy to get wrong — which combinations stay quiet.

const MODELS = [
	{ id: "gpt-5.6-terra", displayName: "gpt-5.6-terra", default: true },
	{ id: "gpt-5.6-terra-mini", displayName: "gpt-5.6-terra-mini", default: false },
];

function withoutPendingApproval(snapshot: ConversationSnapshot): ConversationSnapshot {
	return {
		...snapshot,
		items: snapshot.items.filter(
			(item) =>
				!(item.kind === "activity" && item.activityKind === "approval" && item.status === "pending"),
		),
	};
}

describe("reasoning", () => {
	it("keeps provider reasoning out of the conversation chrome", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);
		expect(screen.queryByText(/Reading the worktree first/)).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /Reasoning/ })).not.toBeInTheDocument();
	});
});

describe("plan", () => {
	it("renders the turn's plan as one checklist", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);
		expect(screen.getByRole("region", { name: "Agent plan" })).toBeInTheDocument();
		expect(screen.getByText("Run the backend tests")).toBeInTheDocument();
	});

	// The daemon writes the plan twice — on the turn and as a row — from one event. Both
	// rendered would be the same list twice in the same turn.
	it("does not also render the plan row the turn already carries", () => {
		const withPlanRow: ConversationSnapshot = {
			...chatFixture,
			items: [
				...chatFixture.items,
				{
					kind: "activity",
					id: "plan-row",
					turnId: "turn-2",
					sequence: 20,
					revision: 0,
					activityKind: "plan",
					status: "running",
					summary: "Updated plan",
					detail: {
						event: "plan",
						steps: [{ text: "Run the backend tests", status: "in_progress" }],
					},
					createdAt: new Date().toISOString(),
				},
			],
			latestSequence: 20,
		};
		render(<ChatWorkspace snapshot={withPlanRow} />);
		expect(screen.getAllByRole("region", { name: "Agent plan" })).toHaveLength(1);
	});

	// The group's identity is reused across polls to keep the memo boundary from
	// re-rendering finished history. A plan and a diff are current state the daemon
	// overwrites while the turn's items stay put, so they have to take part in that
	// comparison — otherwise a checklist stops ticking until something else changes.
	it("ticks a step off on the next poll even though the items did not change", () => {
		const before = chatFixture;
		const { rerender } = render(<ChatWorkspace snapshot={before} />);
		expect(screen.getByLabelText("Run the backend tests — in progress")).toBeInTheDocument();

		const after: ConversationSnapshot = structuredClone(before);
		after.turns = after.turns.map((turn) =>
			turn.plan
				? {
						...turn,
						plan: {
							...turn.plan,
							steps: turn.plan.steps.map((step) =>
								step.text === "Run the backend tests"
									? { ...step, status: "completed" as const }
									: step,
							),
						},
					}
				: turn,
		);
		rerender(<ChatWorkspace snapshot={after} />);

		expect(screen.getByLabelText("Run the backend tests — done")).toBeInTheDocument();
	});

	// A plan whose turn AO never correlated — one from before this controller started.
	it("still renders a plan row whose turn carries none", () => {
		const orphan: ConversationSnapshot = {
			...chatFixture,
			turns: chatFixture.turns.map((turn) => ({ ...turn, plan: undefined })),
			items: [
				...chatFixture.items,
				{
					kind: "activity",
					id: "plan-orphan",
					sequence: 21,
					revision: 0,
					activityKind: "plan",
					status: "running",
					summary: "Updated plan",
					detail: { event: "plan", steps: [{ text: "Resumed plan step", status: "pending" }] },
					createdAt: new Date().toISOString(),
				},
			],
			latestSequence: 21,
		};
		render(<ChatWorkspace snapshot={orphan} />);
		expect(screen.getByText("Resumed plan step")).toBeInTheDocument();
	});
});

describe("provider state chrome", () => {
	it("puts the credential demand above everything else that is wrong", () => {
		render(<ChatWorkspace snapshot={chatFixtureReauth} />);
		expect(screen.getByRole("alert")).toHaveTextContent(/Sign in again to keep going/);
	});

	it("reports a provider-side thread fault while the controller is healthy", () => {
		render(<ChatWorkspace snapshot={chatFixtureThreadError} />);
		expect(screen.getByText(/thread hit an internal error/i)).toBeInTheDocument();
		// The controller banner is a separate claim and must not appear for this.
		expect(screen.queryByText(/agent controller stopped/i)).not.toBeInTheDocument();
	});

	it("surfaces a tool server that will never answer", () => {
		render(<ChatWorkspace snapshot={chatFixtureMcpFailed} />);
		expect(screen.getByText("2 tool servers did not start")).toBeInTheDocument();
	});

	it("says nothing about tool servers when they all started", () => {
		render(<ChatWorkspace snapshot={chatFixture} />);
		expect(screen.queryByText(/did not start/)).not.toBeInTheDocument();
	});

	it("disables the tool-server reload while a turn is running", () => {
		render(<ChatWorkspace snapshot={chatFixtureMcpFailed} onReloadMcpServers={vi.fn()} />);
		expect(screen.getByRole("button", { name: /Reload/ })).toBeDisabled();
	});
});

describe("model reroute", () => {
	it("names what answered rather than what was asked for", () => {
		render(
			<ChatWorkspace
				snapshot={withoutPendingApproval(chatFixtureRerouted)}
				models={MODELS}
				onChooseSettings={vi.fn()}
			/>,
		);
		// The control the user reads to know which model is in play now names the model
		// that replied, and says whose place it took.
		const trigger = screen.getByRole("button", {
			name: "Model and reasoning effort for the next turn",
		});
		expect(trigger.getAttribute("title")).toMatch(
			/answered with gpt-5\.6-terra-mini instead of gpt-5\.6-terra/,
		);
		expect(screen.getByLabelText(/Substituted for gpt-5\.6-terra$/)).toBeInTheDocument();
	});

	it("carries the provider's own reason", () => {
		render(
			<ChatWorkspace
				snapshot={chatFixtureRerouted}
				models={MODELS}
				onChooseSettings={vi.fn()}
			/>,
		);
		expect(
			screen.getByText(/The requested model is at capacity for this account tier/),
		).toBeInTheDocument();
	});

	it("keeps naming the chosen model when nothing was substituted", () => {
		render(
			<ChatWorkspace
				snapshot={withoutPendingApproval(chatFixture)}
				models={MODELS}
				onChooseSettings={vi.fn()}
			/>,
		);
		expect(screen.getByText(/gpt-5\.6-terra/)).toBeInTheDocument();
		expect(screen.queryByLabelText(/Substituted for/)).not.toBeInTheDocument();
	});
});


describe("empty project session permissions", () => {
	it.each(["orchestrator", "worker"] as const)("exposes permissions and project remembering for an empty %s", async (role) => {
		const user = userEvent.setup();
		const remember = vi.fn();
		render(<ChatWorkspace sessionRole={role} snapshot={{ ...chatFixture, items: [], turns: [],
			controller: { state: "ready" }, settings: { approvalMode: "auto" } }}
			onChooseSettings={vi.fn()} onRememberPermissions={remember} />);
		const picker = screen.getByRole("button", { name: "Approval policy for the next turn" });
		expect(picker).toBeEnabled();
		await user.click(picker);
		await user.click(screen.getByRole("menuitem", { name: "Remember for this project" }));
		expect(remember).toHaveBeenCalledWith("auto");
	});
});
