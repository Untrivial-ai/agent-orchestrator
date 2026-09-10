import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ShellTerminal } from "../hooks/useShellTerminals";
import { ShellTerminalTab } from "./ShellTerminalTab";
import { TooltipProvider } from "./ui/tooltip";

const { isWindowsPlatform } = vi.hoisted(() => ({ isWindowsPlatform: vi.fn(() => false) }));
vi.mock("../lib/platform", () => ({ isWindowsPlatform }));

afterEach(() => isWindowsPlatform.mockReturnValue(false));

const shell: ShellTerminal = {
	handleId: "shellterm-1",
	projectId: "ao",
	workingDir: "/repos/ao",
	title: "ao",
	createdAt: "2026-07-24T10:00:00Z",
};

function renderTab(overrides: Partial<Parameters<typeof ShellTerminalTab>[0]> = {}) {
	const onSelect = vi.fn();
	const onClose = vi.fn();
	const onRename = vi.fn();
	render(
		<TooltipProvider>
			<ShellTerminalTab
				isActive={false}
				onClose={onClose}
				onRename={onRename}
				onSelect={onSelect}
				shell={shell}
				{...overrides}
			/>
		</TooltipProvider>,
	);
	return { onSelect, onClose, onRename };
}

describe("ShellTerminalTab rename", () => {
	it("commits a new title on Enter", () => {
		const { onRename } = renderTab();
		fireEvent.doubleClick(screen.getByRole("tab", { name: "ao" }));
		const input = screen.getByRole("textbox", { name: /rename terminal/i });
		fireEvent.change(input, { target: { value: "deploy" } });
		fireEvent.keyDown(input, { key: "Enter" });
		expect(onRename).toHaveBeenCalledWith("deploy");
	});

	it("commits on blur", () => {
		const { onRename } = renderTab();
		fireEvent.doubleClick(screen.getByRole("tab", { name: "ao" }));
		const input = screen.getByRole("textbox", { name: /rename terminal/i });
		fireEvent.change(input, { target: { value: "logs" } });
		fireEvent.blur(input);
		expect(onRename).toHaveBeenCalledWith("logs");
	});

	it("discards on Escape and leaves the title unchanged", () => {
		const { onRename } = renderTab();
		fireEvent.doubleClick(screen.getByRole("tab", { name: "ao" }));
		const input = screen.getByRole("textbox", { name: /rename terminal/i });
		fireEvent.change(input, { target: { value: "throwaway" } });
		fireEvent.keyDown(input, { key: "Escape" });
		expect(onRename).not.toHaveBeenCalled();
		expect(screen.getByRole("tab", { name: "ao" })).toBeInTheDocument();
	});

	it("discards an empty or unchanged title", () => {
		const { onRename } = renderTab();
		fireEvent.doubleClick(screen.getByRole("tab", { name: "ao" }));
		const input = screen.getByRole("textbox", { name: /rename terminal/i });
		fireEvent.change(input, { target: { value: "   " } });
		fireEvent.keyDown(input, { key: "Enter" });
		expect(onRename).not.toHaveBeenCalled();
	});

	it("does not enter edit mode when rename is not wired", () => {
		renderTab({ onRename: undefined });
		fireEvent.doubleClick(screen.getByRole("tab", { name: "ao" }));
		expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
	});

	it("selects the tab on single click", () => {
		const { onSelect } = renderTab();
		fireEvent.click(screen.getByRole("tab", { name: "ao" }));
		expect(onSelect).toHaveBeenCalled();
	});

	it("makes the full connected tile the semantic selection button", () => {
		const { onSelect } = renderTab({ appearance: "connected" });
		const tab = screen.getByRole("tab", { name: "ao" });
		expect(tab).toHaveClass("h-full", "min-w-0", "px-2", "cursor-pointer", "focus-visible:outline-2");
		fireEvent.click(tab);
		expect(onSelect).toHaveBeenCalledOnce();
	});

	it("swaps the terminal glyph for close on hover without reserving a trailing close column", () => {
		renderTab({ appearance: "connected", isActive: true });

		const closeButton = screen.getByRole("button", { name: "Close terminal ao" });
		expect(closeButton.parentElement).toHaveClass("absolute", "left-2", "inset-y-0");
		expect(closeButton).toHaveClass(
			"opacity-0",
			"pointer-events-none",
			"group-hover:opacity-100",
		);
		expect(closeButton).not.toHaveClass("transition-opacity", "transition-[opacity,background,color]");
		expect(closeButton).not.toHaveClass("w-control-sm");
		expect(screen.getByRole("tab", { name: "ao" }).querySelector("svg")).toHaveClass(
			"group-hover:opacity-0",
		);
		expect(screen.getByRole("tab", { name: "ao" })).toHaveAttribute("aria-selected", "true");
	});

	it("hugs the truncated title instead of a fixed connected width", () => {
		renderTab({ appearance: "connected", isActive: false });

		const tab = screen.getByRole("tab", { name: "ao" });
		expect(tab.classList.contains("min-w-0")).toBe(true);
		expect(tab.classList.contains("text-left")).toBe(true);
		const frame = tab.closest("[data-terminal-tab-frame]");
		expect(frame?.classList.contains("inline-flex")).toBe(true);
		expect(frame?.classList.contains("shrink-0")).toBe(true);
		expect(frame?.classList.contains("max-w-shell-tab-max")).toBe(true);
		expect(frame?.classList.contains("w-shell-tab-connected")).toBe(false);
		expect(frame?.classList.contains("min-w-shell-tab-min")).toBe(false);
	});

	it("uses a neutral active surface with a strong foreground selection line", () => {
		renderTab({ appearance: "connected", isActive: true });

		expect(screen.getByRole("tab", { name: "ao" }).closest("[data-terminal-tab-frame]")).toHaveClass(
			"h-full",
			"self-stretch",
			"bg-overlay",
		);
		expect(screen.getByTestId("active-terminal-tab-indicator")).toHaveClass("bottom-0", "h-0.5");
		expect(screen.getByRole("tab", { name: "ao" }).closest("[data-terminal-tab-frame]")).not.toHaveClass("rounded-md", "before:bg-accent", "after:h-px");
	});

	it("matches the shared inactive tab hover surface", () => {
		renderTab({ appearance: "connected", isActive: false });

		expect(screen.getByRole("tab", { name: "ao" }).closest("[data-terminal-tab-frame]")).toHaveClass(
			"h-full",
			"self-stretch",
			"hover:bg-raised",
		);
	});

	it("places the active connected-terminal indicator along the bottom edge", () => {
		renderTab({ appearance: "connected", isActive: true });

		const indicator = screen.getByTestId("active-terminal-tab-indicator");
		expect(indicator).toHaveClass("bottom-0", "h-0.5");
	});

	it("closes from the glyph slot without selecting the tab", () => {
		const { onClose, onSelect } = renderTab({ appearance: "connected" });
		fireEvent.click(screen.getByRole("button", { name: "Close terminal ao" }));
		expect(onClose).toHaveBeenCalledOnce();
		expect(onSelect).not.toHaveBeenCalled();
	});

	it("optically centers the auxiliary terminal glyph with its label", () => {
		renderTab({ appearance: "connected" });

		expect(screen.getByRole("tab", { name: "ao" }).querySelector("svg")?.parentElement).not.toHaveClass("-translate-y-px");
	});
});

describe("ShellTerminalTab rename gesture per platform", () => {
	it("macOS/Linux: double-click enters edit, right-click does not", () => {
		renderTab(); // isWindowsPlatform() defaults to false
		const tab = screen.getByRole("tab", { name: "ao" });
		fireEvent.contextMenu(tab);
		expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
		fireEvent.doubleClick(tab);
		expect(screen.getByRole("textbox", { name: /rename terminal/i })).toBeInTheDocument();
	});

	it("macOS/Linux: two quick clicks enter edit even without a native dblclick (trackpad)", () => {
		renderTab();
		const tab = screen.getByRole("tab", { name: "ao" });
		// Two plain clicks, no dblclick event — mimics a trackpad double-tap that
		// the OS delivers as separate clicks.
		fireEvent.click(tab, { detail: 1 });
		expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
		fireEvent.click(tab, { detail: 1 });
		expect(screen.getByRole("textbox", { name: /rename terminal/i })).toBeInTheDocument();
	});

	it("Windows: right-click enters edit, double-click does not", () => {
		isWindowsPlatform.mockReturnValue(true);
		renderTab();
		const tab = screen.getByRole("tab", { name: "ao" });
		fireEvent.doubleClick(tab);
		expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
		fireEvent.contextMenu(tab);
		expect(screen.getByRole("textbox", { name: /rename terminal/i })).toBeInTheDocument();
	});
});
