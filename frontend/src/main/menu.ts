import type { MenuItemConstructorOptions } from "electron";

export function buildWindowsAppMenuTemplate(onToggleDevTools?: () => void): MenuItemConstructorOptions[] {
	const devtoolsItem: MenuItemConstructorOptions = onToggleDevTools
		? {
			label: "Toggle DevTools",
			accelerator: "Ctrl+Shift+I",
			click: onToggleDevTools,
		}
		: { role: "toggleDevTools" };
	return [
		{
			label: "Edit",
			submenu: [
				{ role: "undo" },
				{ role: "redo" },
				{ type: "separator" },
				{ role: "cut" },
				{ role: "selectAll" },
			],
		},
		{
			label: "View",
			submenu: [
				{ role: "reload" },
				devtoolsItem,
				{ type: "separator" },
				{ role: "resetZoom" },
				{ accelerator: "Ctrl+=", role: "zoomIn" },
				{ accelerator: "Ctrl+Plus", acceleratorWorksWhenHidden: true, role: "zoomIn", visible: false },
				{ accelerator: "Ctrl+-", role: "zoomOut" },
				{ type: "separator" },
				{ role: "togglefullscreen" },
			],
		},
		{
			label: "Window",
			submenu: [{ role: "minimize" }, { role: "close" }],
		},
	];
}
