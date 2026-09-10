import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockIsMac = vi.hoisted(() => vi.fn(() => false));
const mockIsLinux = vi.hoisted(() => vi.fn(() => false));
const uiState = vi.hoisted(() => ({ isSidebarOpen: true }));
vi.mock("../lib/platform", () => ({
	isMacPlatform: mockIsMac,
	isLinuxPlatform: mockIsLinux,
}));

vi.mock("../hooks/useWindowFullScreen", () => ({ useWindowFullScreen: () => false }));
vi.mock("../stores/ui-store", () => ({
	sidebarOccupiesLayout: (state: { isSidebarOpen: boolean }) => state.isSidebarOpen,
	useUiStore: (sel: (s: { isSidebarOpen: boolean }) => unknown) => sel(uiState),
}));

const { CenterPanelShell } = await import("./CenterPanelShell");

describe("CenterPanelShell platform classes", () => {
	beforeEach(() => {
		mockIsMac.mockReturnValue(false);
		mockIsLinux.mockReturnValue(false);
		uiState.isSidebarOpen = true;
	});

	it("applies center-panel-shell--mac on macOS", () => {
		mockIsMac.mockReturnValue(true);
		const { container } = render(<CenterPanelShell>x</CenterPanelShell>);
		expect(container.firstElementChild!.classList.contains("center-panel-shell--mac")).toBe(true);
	});

	it("does not apply center-panel-shell--mac on Linux", () => {
		mockIsLinux.mockReturnValue(true);
		const { container } = render(<CenterPanelShell>x</CenterPanelShell>);
		expect(container.firstElementChild!.classList.contains("center-panel-shell--mac")).toBe(false);
	});

	it("does not apply center-panel-shell--mac on Windows", () => {
		const { container } = render(<CenterPanelShell>x</CenterPanelShell>);
		expect(container.firstElementChild!.classList.contains("center-panel-shell--mac")).toBe(false);
	});

	it("pads the framed titlebar past the Linux nav cluster when the sidebar is collapsed", () => {
		mockIsLinux.mockReturnValue(true);
		uiState.isSidebarOpen = false;
		const { container } = render(<CenterPanelShell>x</CenterPanelShell>);
		expect(container.firstElementChild!.classList.contains("center-panel-shell--titlebar-clearance-linux")).toBe(
			true,
		);
		expect(container.firstElementChild!.classList.contains("center-panel-shell--titlebar-clearance")).toBe(false);
	});
});
