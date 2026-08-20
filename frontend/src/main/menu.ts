import type { MenuItemConstructorOptions } from "electron";

// macOS keeps Electron's default menu unless the app installs its own, and that
// default wires View → Toggle Developer Tools to the built-in role. The role
// resolves the target as `focusedWindow.webContents`, but AO's focus normally
// lives on a WebContentsView (the Browser panel), not on the BrowserWindow — so
// the role reads `webContents` off `undefined` and takes down the main process.
//
// Installing our own menu is the fix: every other entry stays a role, and only
// DevTools routes through the same guarded toggle the toolbar and the renderer's
// menu:action channel already use. The app submenu is spelled out because
// replacing the default menu also replaces the standard Quit/Hide/Services
// items, and losing Cmd+Q would be a worse bug than the one being fixed.
export function buildMacAppMenuTemplate(
	appName: string,
	onToggleDevTools: () => void,
): MenuItemConstructorOptions[] {
	return [
		{
			label: appName,
			submenu: [
				{ role: "about" },
				{ type: "separator" },
				{ role: "services" },
				{ type: "separator" },
				{ role: "hide" },
				{ role: "hideOthers" },
				{ role: "unhide" },
				{ type: "separator" },
				{ role: "quit" },
			],
		},
		{
			label: "Edit",
			submenu: [
				{ role: "undo" },
				{ role: "redo" },
				{ type: "separator" },
				{ role: "cut" },
				{ role: "copy" },
				{ role: "paste" },
				{ role: "selectAll" },
			],
		},
		{
			label: "View",
			submenu: [
				{ role: "reload" },
				{
					label: "Toggle Developer Tools",
					accelerator: "Alt+Command+I",
					click: onToggleDevTools,
				},
				{ type: "separator" },
				{ role: "resetZoom" },
				{ role: "zoomIn" },
				{ role: "zoomOut" },
				{ type: "separator" },
				{ role: "togglefullscreen" },
			],
		},
		{
			label: "Window",
			submenu: [{ role: "minimize" }, { role: "zoom" }, { type: "separator" }, { role: "front" }],
		},
	];
}

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
				{ role: "copy" },
				{ role: "paste" },
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
