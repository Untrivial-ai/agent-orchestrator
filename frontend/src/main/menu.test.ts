import { describe, expect, it, vi } from "vitest";
import { buildMacAppMenuTemplate, buildWindowsAppMenuTemplate } from "./menu";

type MenuItem = ReturnType<typeof buildWindowsAppMenuTemplate>[number];
type SubmenuItem = NonNullable<Extract<MenuItem["submenu"], readonly unknown[]>>[number];

function viewSubmenu(): readonly SubmenuItem[] {
	const viewMenu = buildWindowsAppMenuTemplate().find((item) => item.label === "View");
	if (!viewMenu || !Array.isArray(viewMenu.submenu)) {
		throw new Error("View menu not found");
	}
	return viewMenu.submenu;
}

describe("buildWindowsAppMenuTemplate", () => {
	it("registers both plus key forms for zoom in", () => {
		const zoomInItems = viewSubmenu().filter((item) => item.role === "zoomIn");

		expect(zoomInItems).toEqual(
			expect.arrayContaining([
				expect.objectContaining({ accelerator: "Ctrl+=", role: "zoomIn" }),
				expect.objectContaining({ accelerator: "Ctrl+Plus", role: "zoomIn", visible: false }),
			]),
		);
	});

	it("keeps the direct minus accelerator for zoom out", () => {
		expect(viewSubmenu()).toContainEqual(expect.objectContaining({ accelerator: "Ctrl+-", role: "zoomOut" }));
	});
});

describe("buildMacAppMenuTemplate", () => {
	function macViewSubmenu(onToggleDevTools = () => undefined): readonly SubmenuItem[] {
		const viewMenu = buildMacAppMenuTemplate("AO", onToggleDevTools).find((item) => item.label === "View");
		if (!viewMenu || !Array.isArray(viewMenu.submenu)) throw new Error("View menu not found");
		return viewMenu.submenu;
	}

	// The whole point of installing a macOS menu: Electron's toggleDevTools role
	// reads webContents off the focused BrowserWindow, which is undefined while a
	// WebContentsView holds focus, and that takes down the main process. The item
	// must carry a click handler and must NOT fall back to the role.
	it("routes DevTools through a click handler instead of the crashing role", () => {
		const onToggleDevTools = vi.fn();
		const devtools = macViewSubmenu(onToggleDevTools).filter(
			(item) => item.label === "Toggle Developer Tools" || item.role === "toggleDevTools",
		);

		expect(devtools).toHaveLength(1);
		expect(devtools[0].role).toBeUndefined();
		expect(devtools[0].accelerator).toBe("Alt+Command+I");

		devtools[0].click?.(
			undefined as never,
			undefined as never,
			undefined as never,
		);
		expect(onToggleDevTools).toHaveBeenCalledTimes(1);
	});

	// Replacing the default menu also drops the standard app submenu, so Quit and
	// Hide have to be spelled out or Cmd+Q silently stops working.
	it("keeps the standard app submenu roles", () => {
		const appMenu = buildMacAppMenuTemplate("AO", () => undefined)[0];
		if (!Array.isArray(appMenu.submenu)) throw new Error("app menu not found");

		expect(appMenu.label).toBe("AO");
		expect(appMenu.submenu.map((item) => item.role)).toEqual(
			expect.arrayContaining(["quit", "hide", "hideOthers", "unhide", "services", "about"]),
		);
	});
});
