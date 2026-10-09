import { act, fireEvent, render as rtlRender, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import { TitlebarNav } from "./TitlebarNav";
import { TooltipProvider } from "./ui/tooltip";

function render(ui: ReactElement) {
	return rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
}

type HistoryUpdate = { location: { state: { __TSR_index: number } }; action: { type: string } };

const { history, router, availability } = vi.hoisted(() => {
	const history = {
		back: vi.fn(),
		forward: vi.fn(),
		location: { state: { __TSR_index: 0 } },
		subscribe: vi.fn((_listener: (update: HistoryUpdate) => void) => () => undefined),
	};
	return { history, router: { history }, availability: { canGoBack: false } };
});

vi.mock("@tanstack/react-router", () => ({
	useCanGoBack: () => availability.canGoBack,
	useNavigate: () => vi.fn(),
	useRouter: () => router,
}));

vi.mock("../lib/platform", () => ({
	isLinuxPlatform: () => false,
	isMacPlatform: () => true,
}));

describe("TitlebarNav", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		availability.canGoBack = false;
		useUiStore.setState({ isSidebarOpen: true });
	});

	it.each([false, true])("keeps the same header row with sidebar open=%s", (open) => {
		useUiStore.setState({ isSidebarOpen: open });
		const { container, rerender } = render(<TitlebarNav />);
		const nav = container.querySelector('[data-slot="titlebar-nav"]');
		expect(nav).toHaveClass("top-px", "h-traffic-light-clearance", "left-titlebar-cluster-left");
		rerender(<TooltipProvider><TitlebarNav isFullScreen /></TooltipProvider>);
		expect(nav).toHaveClass("top-px", "h-traffic-light-clearance", "left-titlebar-cluster-left-fullscreen");
		expect(screen.getByRole("button", { name: "Go back" })).toBeDisabled();
	});

	it("keeps history controls mounted after the brand appears and across sidebar toggles", async () => {
		render(<><div data-slot="sidebar-container" /><TitlebarNav /></>);
		const back = screen.getByRole("button", { name: "Go back" });
		const forward = screen.getByRole("button", { name: "Go forward" });
		await screen.findByText("Orchestrator.inc");
		expect(screen.getByRole("button", { name: "Go back" })).toBe(back);
		expect(screen.getByRole("button", { name: "Go forward" })).toBe(forward);
		expect(back).toBeVisible();
		expect(forward).toBeVisible();
		fireEvent.click(screen.getByRole("button", { name: "Collapse sidebar" }));
		expect(screen.queryByText("Orchestrator.inc")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Go back" })).toBe(back);
		fireEvent.click(screen.getByRole("button", { name: "Expand sidebar" }));
		await screen.findByText("Orchestrator.inc");
		expect(screen.getByRole("button", { name: "Go forward" })).toBe(forward);
	});

	it.each([false, true])("preserves history actions and locking with sidebar open=%s", async (open) => {
		useUiStore.setState({ isSidebarOpen: open });
		availability.canGoBack = true;
		const { rerender } = render(<><div data-slot="sidebar-container" /><TitlebarNav /></>);
		if (open) await screen.findByText("Orchestrator.inc");
		const back = screen.getByRole("button", { name: "Go back" });
		const forward = screen.getByRole("button", { name: "Go forward" });
		expect(forward).toBeDisabled();
		const update = history.subscribe.mock.calls[0][0];
		act(() => {
			update({ location: { state: { __TSR_index: 1 } }, action: { type: "PUSH" } });
			update({ location: { state: { __TSR_index: 0 } }, action: { type: "BACK" } });
		});
		fireEvent.click(back);
		fireEvent.click(forward);
		expect(history.back).toHaveBeenCalledOnce();
		expect(history.forward).toHaveBeenCalledOnce();
		rerender(<TooltipProvider><div data-slot="sidebar-container" /><TitlebarNav historyLocked /></TooltipProvider>);
		expect(back).toBeDisabled();
		expect(forward).toBeDisabled();
	});
});
