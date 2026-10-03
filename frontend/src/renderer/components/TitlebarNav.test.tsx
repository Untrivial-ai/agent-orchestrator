import { render as rtlRender, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import { TitlebarNav } from "./TitlebarNav";
import { TooltipProvider } from "./ui/tooltip";

function render(ui: ReactElement) {
	return rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
}

const { history } = vi.hoisted(() => ({
	history: {
		back: vi.fn(),
		forward: vi.fn(),
		location: { state: { __TSR_index: 0 } },
		subscribe: vi.fn(() => () => undefined),
	},
}));

vi.mock("@tanstack/react-router", () => ({
	useCanGoBack: () => false,
	useRouter: () => ({ history }),
}));

vi.mock("../lib/platform", () => ({
	isLinuxPlatform: () => false,
	isMacPlatform: () => true,
}));

describe("TitlebarNav", () => {
	beforeEach(() => {
		useUiStore.setState({ isSidebarOpen: true });
	});

	it.each([false, true])("keeps the same header row with sidebar open=%s", (open) => {
		useUiStore.setState({ isSidebarOpen: open });
		const { container, rerender } = render(<TitlebarNav />);
		const nav = container.querySelector('[data-slot="titlebar-nav"]');
		expect(nav).toHaveClass("top-0", "h-traffic-light-clearance", "left-titlebar-cluster-left");
		rerender(<TooltipProvider><TitlebarNav isFullScreen /></TooltipProvider>);
		expect(nav).toHaveClass("top-0", "h-traffic-light-clearance", "left-titlebar-cluster-left-fullscreen");
		expect(screen.getByRole("button", { name: "Go back" })).toBeDisabled();
	});
});
