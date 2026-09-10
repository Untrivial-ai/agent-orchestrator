import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { aoBridge } from "../../lib/bridge";
import type { ConversationActivity } from "../../types/conversation";
import { ElicitationCard } from "./ElicitationCard";

function activity(detail: ConversationActivity["detail"]): ConversationActivity {
	return {
		kind: "activity",
		id: "question-1",
		sequence: 1,
		revision: 0,
		activityKind: "user_input",
		status: "pending",
		summary: "Choose a direction",
		requestId: "request-1",
		detail,
		createdAt: "2026-08-04T00:00:00Z",
	};
}

describe("ElicitationCard", () => {
	const claudeQuestions = {
		type: "object" as const,
		required: ["question_0", "question_1"],
		properties: {
			question_0: {
				type: "string",
				title: "Approach",
				oneOf: [
					{ const: "Native", title: "Native", description: "Use ACP directly" },
					{ const: "Bridge", title: "Bridge" },
				],
			},
			question_0_custom: { type: "string", title: "Other approach" },
			question_1: {
				type: "string",
				title: "Language",
				oneOf: [
					{ const: "Go", title: "Go" },
					{ const: "TypeScript", title: "TypeScript" },
				],
			},
			question_1_custom: { type: "string", title: "Other language" },
		},
	};

	it("shows one Claude question and its Other field at a time", () => {
		render(
			<ElicitationCard
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				onResolve={vi.fn()}
			/>,
		);

		expect(screen.getByRole("group", { name: /Approach/ })).toBeInTheDocument();
		expect(screen.getByLabelText("Other approach")).toBeInTheDocument();
		expect(screen.queryByRole("group", { name: /Language/ })).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Other language")).not.toBeInTheDocument();
	});

	it("validates the active Claude question before moving forward", async () => {
		const user = userEvent.setup();
		render(
			<ElicitationCard
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				onResolve={vi.fn()}
			/>,
		);

		await user.click(screen.getByRole("button", { name: "Next" }));

		expect(screen.getByText("Choose an answer.")).toBeInTheDocument();
		expect(screen.queryByRole("group", { name: /Language/ })).not.toBeInTheDocument();
	});

	it("navigates Claude questions and preserves answers when going back", async () => {
		const user = userEvent.setup();
		render(
			<ElicitationCard
				activity={activity({ inputMode: "form", schema: claudeQuestions })}
				onResolve={vi.fn()}
			/>,
		);

		await user.click(screen.getByRole("radio", { name: /Native/ }));
		await user.type(screen.getByLabelText("Other approach"), "Hybrid");
		await user.click(screen.getByRole("button", { name: "Next" }));
		expect(screen.getByRole("group", { name: /Language/ })).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Back" }));
		expect(screen.getByRole("radio", { name: /Native/ })).toBeChecked();
		expect(screen.getByLabelText("Other approach")).toHaveValue("Hybrid");
	});

	it("submits all Claude answers together from the final question", async () => {
		const user = userEvent.setup();
		const onResolve = vi.fn().mockResolvedValue(undefined);
		render(
			<ElicitationCard
				activity={activity({
					inputMode: "form",
					message: "Which implementation should we use?",
					schema: claudeQuestions,
				})}
				onResolve={onResolve}
			/>,
		);

		await user.click(screen.getByRole("radio", { name: /Native/ }));
		await user.type(screen.getByLabelText("Other approach"), "Hybrid");
		await user.click(screen.getByRole("button", { name: "Next" }));
		await user.click(screen.getByRole("radio", { name: "Go" }));
		await user.type(screen.getByLabelText("Other language"), "Rust");
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(onResolve).toHaveBeenCalledWith("request-1", "accept", {
			question_0: "Native",
			question_0_custom: "Hybrid",
			question_1: "Go",
			question_1_custom: "Rust",
		});
	});

	it("keeps generic MCP forms in the all-fields layout", () => {
		render(
			<ElicitationCard
				activity={activity({
					inputMode: "form",
					schema: {
						type: "object",
						properties: {
							name: { type: "string", title: "Name" },
							team: { type: "string", title: "Team" },
						},
					},
				})}
				onResolve={vi.fn()}
			/>,
		);

		expect(screen.getByLabelText("Name")).toBeInTheDocument();
		expect(screen.getByLabelText("Team")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Next" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Continue" })).toBeInTheDocument();
	});

	it("keeps required fields actionable instead of sending an invalid form", async () => {
		const user = userEvent.setup();
		const onResolve = vi.fn();
		render(
			<ElicitationCard
				activity={activity({
					inputMode: "form",
					schema: {
						type: "object",
						required: ["name"],
						properties: { name: { type: "string", title: "Name" } },
					},
				})}
				onResolve={onResolve}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(screen.getByText("This field is required.")).toBeInTheDocument();
		expect(onResolve).not.toHaveBeenCalled();
	});

	it("opens an external URL only after the user explicitly consents", async () => {
		const user = userEvent.setup();
		const openExternal = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		const onResolve = vi.fn().mockResolvedValue(undefined);
		render(
			<ElicitationCard
				activity={activity({ inputMode: "url", url: "https://console.anthropic.com/oauth", message: "Sign in" })}
				onResolve={onResolve}
			/>,
		);
		expect(openExternal).not.toHaveBeenCalled();
		expect(screen.getByText("https://console.anthropic.com/oauth")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Open console.anthropic.com" }));
		expect(openExternal).toHaveBeenCalledWith("https://console.anthropic.com/oauth");
		expect(onResolve).toHaveBeenCalledWith("request-1", "accept", undefined);
	});

	it("refuses unsafe URL schemes", () => {
		render(
			<ElicitationCard
				activity={activity({ inputMode: "url", url: "file:///Users/alice/.ssh/id_rsa" })}
				onResolve={vi.fn()}
			/>,
		);
		expect(screen.getByRole("alert")).toHaveTextContent(/unsafe or invalid URL/i);
		expect(screen.getByRole("button", { name: "Open link" })).toBeDisabled();
	});
});
