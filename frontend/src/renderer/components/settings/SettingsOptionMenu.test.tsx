import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { SettingsOptionMenu } from "./SettingsOptionMenu";

describe("SettingsOptionMenu", () => {
	it("filters searchable options by label and value", async () => {
		render(
			<SettingsOptionMenu
				aria-label="Worker model"
				value=""
				searchable
				options={[
					{ value: "anthropic/claude-sonnet", label: "Claude Sonnet" },
					{ value: "openai/gpt-5.5", label: "GPT Five" },
				]}
				onChange={() => {}}
			/>,
		);

		await userEvent.click(screen.getByRole("button", { name: "Worker model" }));
		const search = screen.getByRole("searchbox", { name: "Search worker model" });

		await userEvent.type(search, "sonnet");
		expect(screen.getByRole("menuitem", { name: "Claude Sonnet" })).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "GPT Five" })).not.toBeInTheDocument();

		await userEvent.clear(search);
		await userEvent.type(search, "openai/gpt");
		expect(screen.getByRole("menuitem", { name: "GPT Five" })).toBeInTheDocument();
		expect(screen.queryByRole("menuitem", { name: "Claude Sonnet" })).not.toBeInTheDocument();
	});

	it("keeps an action footer outside the shrinking options region", async () => {
		render(
			<SettingsOptionMenu
				aria-label="Agent"
				value=""
				options={Array.from({ length: 20 }, (_, index) => ({
					value: `agent-${index}`,
					label: `Agent ${index}`,
				}))}
				action={{ label: "Manage agents…", onSelect: () => {} }}
				onChange={() => {}}
			/>,
		);

		await userEvent.click(screen.getByRole("button", { name: "Agent" }));

		const firstOption = screen.getByRole("menuitem", { name: "Agent 0" });
		const action = screen.getByRole("menuitem", { name: "Manage agents…" });
		const scrollRegion = firstOption.parentElement?.parentElement;
		const actionRegion = action.parentElement;

		expect(scrollRegion).toHaveAttribute("data-slot", "settings-option-menu-scroll-region");
		expect(scrollRegion).toHaveClass("min-h-0", "flex-1", "overflow-hidden");
		expect(firstOption.parentElement).toHaveClass("min-h-0", "overflow-y-auto");
		expect(actionRegion).toHaveAttribute("data-slot", "settings-option-menu-action");
		expect(actionRegion).toHaveClass("shrink-0");
	});
});
