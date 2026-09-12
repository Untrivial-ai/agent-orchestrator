import type { ReactNode } from "react";
import { cn } from "../lib/utils";
import { useWindowFullScreen } from "../hooks/useWindowFullScreen";
import { isLinuxPlatform, isMacPlatform } from "../lib/platform";
import { sidebarOccupiesLayout, useUiStore } from "../stores/ui-store";

/**
 * Shared inset center panel: sidebar-colored outer frame with a bordered inner
 * surface. Used by the shell's app routes (kanban / session), the welcome board,
 * and settings. Chrome lives in `styles.css` (`center-panel-shell` +
 * `center-panel-surface`).
 *
 * `titlebarAlign` (default true) pulls Board/Terminal titles up level with the
 * fixed TitlebarNav cluster on macOS and Linux. Windows keeps those controls in
 * its custom titlebar, so it does not need this clearance.
 */
export function CenterPanelShell({
	className,
	children,
	titlebarAlign = true,
}: {
	/** Extra classes on the outer frame. */
	className?: string;
	children: ReactNode;
	/** When false, keep the default panel insets (Settings). */
	titlebarAlign?: boolean;
}) {
	const isSidebarOpen = useUiStore(sidebarOccupiesLayout);
	const isFullScreen = useWindowFullScreen();
	const isMac = isMacPlatform();
	const isLinux = isLinuxPlatform();
	const align = titlebarAlign && isMac;
	const titlebarClearance = align && !isSidebarOpen;
	const linuxTitlebarClearance = titlebarAlign && isLinux && !isSidebarOpen;

	return (
		<div
			className={cn(
				"center-panel-shell",
				align && "center-panel-shell--mac",
				titlebarClearance && "center-panel-shell--titlebar-clearance",
				titlebarClearance && isFullScreen && "center-panel-shell--titlebar-clearance-fullscreen",
				linuxTitlebarClearance && "center-panel-shell--titlebar-clearance-linux",
				align && isFullScreen && "center-panel-shell--fullscreen",
				className,
			)}
		>
			<div className="center-panel-surface">{children}</div>
		</div>
	);
}
