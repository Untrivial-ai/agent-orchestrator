import { describe, expect, it } from "vitest";
import { buildWindowsAppMenuTemplate } from "./menu";

type MenuItem = ReturnType<typeof buildWindowsAppMenuTemplate>[number];
type SubmenuItem = NonNullable<Extract<MenuItem["submenu"], readonly unknown[]>>[number];

function viewSubmenu(): readonly SubmenuItem[] {
	const viewMenu = buildWindowsAppMenuTemplate().find((item) => item.label === "View");
	if (!viewMenu || !Array.isArray(viewMenu.submenu)) {
		throw new Error("View menu not found");
	}
	return viewMenu.submenu;
}

function allSubmenuItems(): readonly SubmenuItem[] {
	return buildWindowsAppMenuTemplate().flatMap((item) =>
		Array.isArray(item.submenu) ? item.submenu : [],
	);
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

	it("does not bind copy/paste roles so Ctrl+C/Ctrl+V reach terminal panes", () => {
		const roles = allSubmenuItems().map((item) => item.role);

		expect(roles).not.toContain("copy");
		expect(roles).not.toContain("paste");
	});
});
