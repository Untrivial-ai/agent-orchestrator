import { render as rtlRender } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
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
	isLinuxPlatform: () => true,
	isMacPlatform: () => false,
}));

const { TitlebarNav } = await import("./TitlebarNav");

describe("TitlebarNav on Linux", () => {
	afterEach(() => {
		useUiStore.setState({ isSidebarOpen: true });
	});

	it("pins the collapse cluster to the Linux inset, not the macOS traffic-light offset", () => {
		const { container } = render(<TitlebarNav />);

		const nav = container.querySelector('[data-slot="titlebar-nav"]');
		expect(nav).toHaveClass("left-titlebar-cluster-left-linux", "top-0.75");
		expect(nav).not.toHaveClass("left-0");
		expect(nav).not.toHaveClass("left-titlebar-cluster-left");
	});

	it("shifts the cluster clear of the framed panel border when the sidebar is off-canvas", () => {
		useUiStore.setState({ isSidebarOpen: false });
		const { container } = render(<TitlebarNav />);

		const nav = container.querySelector('[data-slot="titlebar-nav"]');
		expect(nav).toHaveClass("left-titlebar-cluster-left-linux-panel");
		expect(nav).not.toHaveClass("left-titlebar-cluster-left-linux");
	});
});
