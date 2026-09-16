import type { MenuItemConstructorOptions } from "electron";

// Electron's built-in toggleDevTools role assumes the focused surface belongs
// to a BrowserWindow. AO uses BaseWindow with WebContentsView children, so the
// role can receive no focused window and crash the main process. Keep Electron's
// complete standard menus through their top-level roles, but replace View so
// DevTools routes through AO's guarded handler. Similarly, Electron's default
// fileMenu role binds Cmd+W to "Close Window", which kills the entire application
// window whenever a tab or terminal close races the native menu. Multi-tab
// macOS applications (Safari, Chrome, VS Code) bind Shift+Command+W to "Close Window"
// so Command+W remains scoped to tabs and terminals.
export function buildMacAppMenuTemplate(onToggleDevTools: () => void): MenuItemConstructorOptions[] {
	return [
		{ role: "appMenu" },
		{
			role: "fileMenu",
			submenu: [
				{
					role: "close",
					accelerator: "Shift+Command+W",
				},
			],
		},
		{ role: "editMenu" },
		{
			label: "View",
			submenu: [
				{ role: "reload" },
				{ role: "forceReload" },
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
		{ role: "windowMenu" },
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

export function buildLinuxAppMenuTemplate(onToggleDevTools?: () => void): MenuItemConstructorOptions[] {
	return buildWindowsAppMenuTemplate(onToggleDevTools);
}
