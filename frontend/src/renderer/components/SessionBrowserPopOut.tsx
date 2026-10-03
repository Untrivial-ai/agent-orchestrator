import { type ReactNode } from "react";
import { useWindowFullScreen } from "../hooks/useWindowFullScreen";
import { hidesShellTopbar } from "../lib/platform";
import { cn } from "../lib/utils";

export function SessionBrowserPopOut({ children, onTopbarHost, phase }: {
	children: ReactNode;
	onTopbarHost: (host: HTMLDivElement | null) => void;
	phase: "mounting" | "open";
}) {
	const isNativeFullScreen = useWindowFullScreen();
	const macWindowed = hidesShellTopbar() && !isNativeFullScreen;
	return <div className={cn("browser-popout-overlay", macWindowed && "browser-popout-overlay--mac-windowed")} data-phase={phase}>
		<div aria-hidden="true" className="browser-popout-backdrop" />
		<div className={cn("browser-popout-titlebar browser-panel__topbar-host", macWindowed && "browser-popout-titlebar--mac-windowed")} data-testid="browser-popout-topbar" ref={onTopbarHost} />
		<div className="browser-popout-frame">{children}</div>
	</div>;
}
