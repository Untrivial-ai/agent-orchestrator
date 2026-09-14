import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SessionFileTabs } from "./SessionFileTabs";
import { TooltipProvider } from "./ui/tooltip";

describe("SessionFileTabs", () => {
	it("activates and closes language-aware file tabs", async () => {
		const onActivateFile = vi.fn();
		const onAddFeedback = vi.fn();
		const onCloseFile = vi.fn();
		render(
			<TooltipProvider>
				<div role="tablist">
					<SessionFileTabs
						state={{ openPaths: ["src/App.tsx"], activePath: "src/App.tsx" }}
						onAddFeedback={onAddFeedback}
						onActivateFile={onActivateFile}
						onCloseFile={onCloseFile}
					/>
				</div>
			</TooltipProvider>,
		);
		const tab = screen.getByRole("tab", { name: "App.tsx" });
		const languageIcon = tab.querySelector('[aria-hidden="true"]');
		expect(languageIcon).toBeInTheDocument();
		const closeButton = screen.getByRole("button", { name: "Close App.tsx" });
		const feedbackButton = screen.getByRole("button", { name: "Add feedback for file src/App.tsx" });
		expect(feedbackButton.parentElement).toHaveClass("pl-1");
		expect(closeButton.parentElement).toHaveClass("pr-1");
		expect(tab.closest("[data-terminal-tab-frame]")).toHaveClass("max-w-shell-tab-max");
		expect(tab.closest("[data-terminal-tab-frame]")).not.toHaveClass(
			"session-tab-icon-floor",
			"session-tab-icon-floor--closable",
			"pl-3",
			"pr-1.5",
		);
		await userEvent.click(languageIcon!);
		expect(onActivateFile).toHaveBeenCalledWith("src/App.tsx");
		await userEvent.click(feedbackButton);
		expect(onAddFeedback).toHaveBeenCalledWith("src/App.tsx");
		await userEvent.click(closeButton);
		expect(onCloseFile).toHaveBeenCalledWith("src/App.tsx");
	});

	it("replaces the file icon with a visible dirty dot until the file is saved", () => {
		render(
			<TooltipProvider>
				<div role="tablist">
					<SessionFileTabs
						dirtyPaths={new Set(["src/App.tsx"])}
						state={{ openPaths: ["src/App.tsx"], activePath: "src/App.tsx" }}
						onAddFeedback={vi.fn()}
						onActivateFile={vi.fn()}
						onCloseFile={vi.fn()}
					/>
				</div>
			</TooltipProvider>,
		);

		expect(screen.getByTestId("unsaved-tab-indicator")).toHaveClass("rounded-full", "bg-foreground");
		expect(screen.getByRole("button", { name: "Close App.tsx" })).toHaveClass("opacity-100");
	});
});
